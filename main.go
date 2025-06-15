package main

import (
	"context"
	"fmt"
	"image"
	"log"
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

type TextElement struct {
	Text string `json:"text"`
	X    int    `json:"x"`
	Y    int    `json:"y"`
}

type OCRResponse struct {
	Success    bool          `json:"success"`
	TextList   []TextElement `json:"text_list"`
	TotalCount int           `json:"total_count"`
	Message    string        `json:"message,omitempty"`
}

type OCRAnalyzer struct {
	tesseractPath string
	enabled       bool
	mu            sync.RWMutex
}

func NewOCRAnalyzer() (*OCRAnalyzer, error) {
	analyzer := &OCRAnalyzer{
		tesseractPath: "/usr/bin/tesseract",
		enabled:       false,
	}

	// Tesseract 존재 확인
	if _, err := os.Stat(analyzer.tesseractPath); err != nil {
		return nil, fmt.Errorf("tesseract not found")
	}

	// 고정 경로 설정
	os.Setenv("TESSDATA_PREFIX", "/usr/share/tesseract-ocr/4.00/tessdata")
	analyzer.enabled = true

	log.Println("OCR initialized")
	return analyzer, nil
}

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
	fullText := ocr.recognizeFullImage(imagePath)
	if fullText != "" {
		cleanedText := ocr.cleanTesseractOutput(fullText)
		if cleanedText != "" && ocr.isValidText(cleanedText) {
			centerX := img.Cols() / 2
			centerY := img.Rows() / 2
			results = append(results, TextElement{
				Text: cleanedText,
				X:    centerX,
				Y:    centerY,
			})
		}
	}

	// 영역별 OCR
	textRegions := ocr.detectTextRegions(img)
	for _, region := range textRegions {
		text := ocr.recognizeTextInRegion(img, region)
		cleanedText := ocr.cleanTesseractOutput(text)

		if cleanedText != "" && ocr.isValidText(cleanedText) && !ocr.isDuplicateText(cleanedText, results) {
			results = append(results, TextElement{
				Text: cleanedText,
				X:    region.Min.X + region.Dx()/2,
				Y:    region.Min.Y + region.Dy()/2,
			})
		}
	}

	// 중복 제거 및 정리
	results = ocr.removeDuplicates(results)
	results = ocr.filterValidTexts(results)

	return results, nil
}

func (ocr *OCRAnalyzer) cleanTesseractOutput(rawText string) string {
	if rawText == "" {
		return ""
	}

	cleaned := rawText

	// Tesseract 경고 메시지 제거
	warningPatterns := []string{
		`Warning: Invalid resolution \d+ dpi\. Using \d+ instead\.`,
		`Estimating resolution as \d+`,
		`Warning:.*`,
		`Error:.*`,
	}

	for _, pattern := range warningPatterns {
		re := regexp.MustCompile(pattern)
		cleaned = re.ReplaceAllString(cleaned, "")
	}

	// 줄바꿈 정리
	lines := strings.Split(cleaned, "\n")
	var validLines []string

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" && len(line) > 0 {
			validLines = append(validLines, line)
		}
	}

	if len(validLines) == 0 {
		return ""
	}

	if len(validLines) == 1 {
		return validLines[0]
	}

	// 여러 줄 처리
	var processedLines []string
	var currentGroup []string

	for _, line := range validLines {
		if len(line) <= 10 {
			currentGroup = append(currentGroup, line)
		} else {
			if len(currentGroup) > 0 {
				processedLines = append(processedLines, strings.Join(currentGroup, " "))
				currentGroup = []string{}
			}
			processedLines = append(processedLines, line)
		}
	}

	if len(currentGroup) > 0 {
		processedLines = append(processedLines, strings.Join(currentGroup, " "))
	}

	return strings.Join(processedLines, " | ")
}

func (ocr *OCRAnalyzer) filterValidTexts(elements []TextElement) []TextElement {
	var filtered []TextElement

	for _, elem := range elements {
		text := strings.TrimSpace(elem.Text)

		if len(text) < 1 {
			continue
		}

		if len(text) <= 2 && !ocr.isSignificantShortText(text) {
			continue
		}

		if ocr.isOnlySpecialChars(text) {
			continue
		}

		if ocr.isRepeatingPattern(text) {
			continue
		}

		filtered = append(filtered, elem)
	}

	return filtered
}

func (ocr *OCRAnalyzer) isSignificantShortText(text string) bool {
	// 숫자로만 구성된 경우
	if regexp.MustCompile(`^\d+$`).MatchString(text) {
		return true
	}

	significantShorts := []string{
		"안", "좋", "나", "다", "를", "을", "의", "에", "로", "과", "와",
		"OK", "NO", "ON", "UP", "GO", "IN", "TO", "AT", "BY",
		"@", "#", "$", "%", "&", "*", "+", "-", "=", "?", "!",
	}

	for _, significant := range significantShorts {
		if text == significant {
			return true
		}
	}

	return false
}

func (ocr *OCRAnalyzer) isOnlySpecialChars(text string) bool {
	hasLetter := false
	hasDigit := false

	for _, r := range text {
		if unicode.IsLetter(r) {
			hasLetter = true
		} else if unicode.IsDigit(r) {
			hasDigit = true
		}
	}

	return !hasLetter && !hasDigit && len(text) > 0
}

func (ocr *OCRAnalyzer) isRepeatingPattern(text string) bool {
	if len(text) < 3 {
		return false
	}

	// 같은 문자 반복 확인
	if len(text) >= 3 {
		firstChar := text[0]
		allSame := true
		for _, char := range text {
			if byte(char) != firstChar {
				allSame = false
				break
			}
		}
		if allSame {
			return true
		}
	}

	// 짧은 패턴 반복
	for i := 1; i <= len(text)/3; i++ {
		pattern := text[:i]
		if strings.Repeat(pattern, len(text)/i) == text && len(text) >= i*3 {
			return true
		}
	}

	return false
}

func (ocr *OCRAnalyzer) recognizeFullImage(imagePath string) string {
	psmModes := []string{"3", "6"}

	for _, psm := range psmModes {
		text := ocr.runTesseract(imagePath, psm)
		if text != "" && len(strings.TrimSpace(text)) > 0 {
			return text
		}
	}

	return ""
}

func (ocr *OCRAnalyzer) recognizeTextInRegion(img gocv.Mat, region image.Rectangle) string {
	roi := img.Region(region)
	if roi.Empty() {
		return ""
	}
	defer roi.Close()

	processed := ocr.basicPreprocess(roi)
	defer processed.Close()

	tempFile := filepath.Join(os.TempDir(), fmt.Sprintf("ocr_region_%d.png", time.Now().UnixNano()))
	defer os.Remove(tempFile)

	if !gocv.IMWrite(tempFile, processed) {
		return ""
	}

	return ocr.runTesseract(tempFile, "8")
}

func (ocr *OCRAnalyzer) runTesseract(imagePath, psm string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, ocr.tesseractPath, imagePath, "stdout",
		"-l", "kor+eng",
		"--psm", psm)

	cmd.Env = append(os.Environ(),
		"TESSDATA_PREFIX=/usr/share/tesseract-ocr/4.00/tessdata")

	output, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(output))
}

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

func (ocr *OCRAnalyzer) basicPreprocess(roi gocv.Mat) gocv.Mat {
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

func (ocr *OCRAnalyzer) isDuplicateText(text string, existing []TextElement) bool {
	cleanText := strings.ToLower(strings.TrimSpace(text))
	for _, elem := range existing {
		if strings.ToLower(strings.TrimSpace(elem.Text)) == cleanText {
			return true
		}
	}
	return false
}

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

func (ocr *OCRAnalyzer) isValidText(text string) bool {
	if len(text) < 1 {
		return false
	}

	trimmed := strings.TrimSpace(text)
	if len(trimmed) < 1 {
		return false
	}

	runeCount := len([]rune(trimmed))
	if runeCount > 200 {
		return false
	}

	hasValidChar := false
	for _, r := range trimmed {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			hasValidChar = true
			break
		}
	}

	return hasValidChar
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

var analyzer *OCRAnalyzer

func extractHandler(c *gin.Context) {
	file, err := c.FormFile("image")
	if err != nil {
		c.JSON(http.StatusBadRequest, OCRResponse{
			Success: false,
			Message: "Image file required",
		})
		return
	}

	imagePath := filepath.Join(os.TempDir(), fmt.Sprintf("ocr_%s.png", uuid.New().String()[:8]))
	defer os.Remove(imagePath)

	if err := c.SaveUploadedFile(file, imagePath); err != nil {
		c.JSON(http.StatusInternalServerError, OCRResponse{
			Success: false,
			Message: "Failed to save image",
		})
		return
	}

	texts, err := analyzer.ExtractTexts(imagePath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, OCRResponse{
			Success: false,
			Message: "OCR failed",
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
	c.JSON(http.StatusOK, map[string]interface{}{
		"status": "ok",
		"ocr":    analyzer != nil && analyzer.enabled,
	})
}

func main() {
	var err error
	analyzer, err = NewOCRAnalyzer()
	if err != nil {
		log.Fatal("OCR init failed:", err)
	}

	gin.SetMode(gin.ReleaseMode)
	r := gin.Default()

	config := cors.DefaultConfig()
	config.AllowAllOrigins = true
	r.Use(cors.New(config))

	r.POST("/extract", extractHandler)
	r.GET("/health", healthHandler)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8000"
	}

	log.Printf("OCR service ready on port %s", port)
	r.Run(":" + port)
}
