package main

import (
	"context"
	"fmt"
	"image"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"gocv.io/x/gocv"
)

// 텍스트 요소
type TextElement struct {
	Text string `json:"text"`
	X    int    `json:"x"`
	Y    int    `json:"y"`
}

// 간소화된 응답
type OCRResponse struct {
	Success    bool          `json:"success"`
	TextList   []TextElement `json:"text_list"`
	TotalCount int           `json:"total_count"`
}

// OCR 분석기
type OCRAnalyzer struct {
	tesseractPath string
	enabled       bool
	mu            sync.RWMutex
}

func NewOCRAnalyzer() (*OCRAnalyzer, error) {
	analyzer := &OCRAnalyzer{enabled: false}

	// Tesseract 경로 찾기
	paths := []string{"/usr/bin/tesseract", "/usr/local/bin/tesseract", "tesseract"}
	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			analyzer.tesseractPath = path
			analyzer.enabled = true
			break
		}
	}

	if !analyzer.enabled {
		return nil, fmt.Errorf("tesseract not found")
	}

	// Tessdata 환경변수 설정 (알려진 경로)
	os.Setenv("TESSDATA_PREFIX", "/usr/share/tessdata")

	return analyzer, nil
}

// 메인 OCR 처리
func (ocr *OCRAnalyzer) ExtractTexts(imagePath string) ([]TextElement, error) {
	ocr.mu.RLock()
	defer ocr.mu.RUnlock()

	if !ocr.enabled {
		return nil, fmt.Errorf("OCR not enabled")
	}

	img := gocv.IMRead(imagePath, gocv.IMReadColor)
	if img.Empty() {
		return nil, fmt.Errorf("failed to load image")
	}
	defer img.Close()

	var results []TextElement

	// 전체 이미지 OCR
	fullText := ocr.runOCR(imagePath, "3")
	if fullText != "" {
		cleanText := ocr.cleanText(fullText)
		if cleanText != "" {
			results = append(results, TextElement{
				Text: cleanText,
				X:    img.Cols() / 2,
				Y:    img.Rows() / 2,
			})
		}
	}

	// 텍스트 영역 감지 후 개별 인식
	regions := ocr.detectTextRegions(img)
	for _, region := range regions {
		text := ocr.recognizeRegion(img, region)
		cleanText := ocr.cleanText(text)

		if cleanText != "" && !ocr.isDuplicate(cleanText, results) {
			results = append(results, TextElement{
				Text: cleanText,
				X:    region.Min.X + region.Dx()/2,
				Y:    region.Min.Y + region.Dy()/2,
			})
		}
	}

	return ocr.removeDuplicates(results), nil
}

// OCR 실행
func (ocr *OCRAnalyzer) runOCR(imagePath, psm string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, ocr.tesseractPath, imagePath, "stdout",
		"-l", "kor+eng", "--psm", psm)
	cmd.Env = append(os.Environ(), "TESSDATA_PREFIX=/usr/share/tessdata")

	output, err := cmd.Output()
	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(output))
}

// 영역별 인식
func (ocr *OCRAnalyzer) recognizeRegion(img gocv.Mat, region image.Rectangle) string {
	roi := img.Region(region)
	if roi.Empty() {
		return ""
	}
	defer roi.Close()

	// 전처리
	processed := ocr.preprocessImage(roi)
	defer processed.Close()

	tempFile := filepath.Join(os.TempDir(), fmt.Sprintf("ocr_%d.png", time.Now().UnixNano()))
	defer os.Remove(tempFile)

	if !gocv.IMWrite(tempFile, processed) {
		return ""
	}

	return ocr.runOCR(tempFile, "8")
}

// 이미지 전처리
func (ocr *OCRAnalyzer) preprocessImage(roi gocv.Mat) gocv.Mat {
	gray := gocv.NewMat()
	defer gray.Close()
	gocv.CvtColor(roi, &gray, gocv.ColorBGRToGray)

	enlarged := gocv.NewMat()
	defer enlarged.Close()
	newSize := image.Pt(roi.Cols()*2, roi.Rows()*2)
	gocv.Resize(gray, &enlarged, newSize, 0, 0, gocv.InterpolationCubic)

	result := gocv.NewMat()
	gocv.AdaptiveThreshold(enlarged, &result, 255, gocv.AdaptiveThresholdGaussian, gocv.ThresholdBinary, 11, 2)

	return result
}

// 텍스트 영역 감지
func (ocr *OCRAnalyzer) detectTextRegions(img gocv.Mat) []image.Rectangle {
	gray := gocv.NewMat()
	defer gray.Close()
	gocv.CvtColor(img, &gray, gocv.ColorBGRToGray)

	edges := gocv.NewMat()
	defer edges.Close()
	gocv.Canny(gray, &edges, 50, 150)

	kernel := gocv.GetStructuringElement(gocv.MorphRect, image.Pt(10, 2))
	defer kernel.Close()

	connected := gocv.NewMat()
	defer connected.Close()
	gocv.MorphologyEx(edges, &connected, gocv.MorphClose, kernel)

	contours := gocv.FindContours(connected, gocv.RetrievalExternal, gocv.ChainApproxSimple)
	defer contours.Close()

	var regions []image.Rectangle
	for i := 0; i < contours.Size(); i++ {
		contour := contours.At(i)
		area := gocv.ContourArea(contour)

		if area > 100 && area < 50000 {
			rect := gocv.BoundingRect(contour)
			if rect.Dx() > 15 && rect.Dy() > 8 && rect.Dy() < 100 {
				padding := 5
				expandedRect := image.Rect(
					max(0, rect.Min.X-padding),
					max(0, rect.Min.Y-padding),
					min(img.Cols(), rect.Max.X+padding),
					min(img.Rows(), rect.Max.Y+padding),
				)
				regions = append(regions, expandedRect)
			}
		}
	}

	return regions
}

// 텍스트 정리
func (ocr *OCRAnalyzer) cleanText(rawText string) string {
	if rawText == "" {
		return ""
	}

	// Tesseract 경고 메시지 제거
	warningPatterns := []string{
		`Warning: Invalid resolution \d+ dpi\. Using \d+ instead\.`,
		`Estimating resolution as \d+`,
		`Warning:.*`,
		`Error:.*`,
	}

	cleaned := rawText
	for _, pattern := range warningPatterns {
		re := regexp.MustCompile(pattern)
		cleaned = re.ReplaceAllString(cleaned, "")
	}

	// 줄 정리
	lines := strings.Split(cleaned, "\n")
	var validLines []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" && ocr.isValidText(line) {
			validLines = append(validLines, line)
		}
	}

	if len(validLines) == 0 {
		return ""
	}

	return strings.Join(validLines, " | ")
}

// 유효한 텍스트 확인
func (ocr *OCRAnalyzer) isValidText(text string) bool {
	text = strings.TrimSpace(text)
	if len(text) < 1 || len([]rune(text)) > 200 {
		return false
	}

	hasValidChar := false
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			hasValidChar = true
			break
		}
	}

	return hasValidChar
}

// 중복 확인
func (ocr *OCRAnalyzer) isDuplicate(text string, existing []TextElement) bool {
	cleanText := strings.ToLower(strings.TrimSpace(text))
	for _, elem := range existing {
		if strings.ToLower(strings.TrimSpace(elem.Text)) == cleanText {
			return true
		}
	}
	return false
}

// 중복 제거
func (ocr *OCRAnalyzer) removeDuplicates(elements []TextElement) []TextElement {
	seen := make(map[string]bool)
	var unique []TextElement

	for _, elem := range elements {
		key := strings.ToLower(strings.TrimSpace(elem.Text))
		if !seen[key] && key != "" {
			seen[key] = true
			unique = append(unique, elem)
		}
	}

	return unique
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// 전역 변수
var analyzer *OCRAnalyzer

// HTTP 핸들러들
func extractHandler(c *gin.Context) {
	file, err := c.FormFile("image")
	if err != nil {
		c.JSON(http.StatusBadRequest, OCRResponse{
			Success: false,
		})
		return
	}

	imagePath := filepath.Join(os.TempDir(), fmt.Sprintf("ocr_%s.png", uuid.New().String()[:8]))
	defer os.Remove(imagePath)

	if err := c.SaveUploadedFile(file, imagePath); err != nil {
		c.JSON(http.StatusInternalServerError, OCRResponse{
			Success: false,
		})
		return
	}

	texts, err := analyzer.ExtractTexts(imagePath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, OCRResponse{
			Success: false,
		})
		return
	}

	c.JSON(http.StatusOK, OCRResponse{
		Success:    true,
		TextList:   texts,
		TotalCount: len(texts),
	})
}

func healthHandler(c *gin.Context) {
	status := "healthy"
	if analyzer == nil || !analyzer.enabled {
		status = "unhealthy"
	}

	c.JSON(http.StatusOK, map[string]interface{}{
		"status":      status,
		"ocr_enabled": analyzer != nil && analyzer.enabled,
	})
}

func rootHandler(c *gin.Context) {
	c.JSON(http.StatusOK, map[string]string{
		"service": "Simple OCR Service",
		"version": "2.0.0",
		"usage":   "POST /extract with image file",
	})
}

func main() {
	var err error
	analyzer, err = NewOCRAnalyzer()
	if err != nil {
		panic("Failed to initialize OCR: " + err.Error())
	}

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	config := cors.DefaultConfig()
	config.AllowAllOrigins = true
	config.AllowMethods = []string{"GET", "POST", "OPTIONS"}
	config.AllowHeaders = []string{"Origin", "Content-Length", "Content-Type"}
	r.Use(cors.New(config))

	r.GET("/", rootHandler)
	r.GET("/health", healthHandler)
	r.POST("/extract", extractHandler)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8000"
	}

	r.Run(":" + port)
}
