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

// 간소화된 텍스트 요소 구조체
type TextElement struct {
	Text string `json:"text"`
	X    int    `json:"x"`
	Y    int    `json:"y"`
}

// 간소화된 API 응답 구조체
type OCRResponse struct {
	Success    bool          `json:"success"`
	TextList   []TextElement `json:"text_list"`
	TotalCount int           `json:"total_count"`
}

// 간소화된 OCR 분석기
type OCRAnalyzer struct {
	tesseractPath string
	enabled       bool
	mu            sync.RWMutex
}

func NewOCRAnalyzer() (*OCRAnalyzer, error) {
	analyzer := &OCRAnalyzer{
		enabled: false,
	}

	// Tesseract 경로 찾기 (간소화)
	tesseractPaths := []string{
		"/usr/bin/tesseract",
		"/usr/local/bin/tesseract",
		"tesseract",
	}

	for _, path := range tesseractPaths {
		if _, err := os.Stat(path); err == nil {
			analyzer.tesseractPath = path
			analyzer.enabled = true
			break
		}
	}

	if !analyzer.enabled {
		return nil, fmt.Errorf("tesseract not found")
	}

	// 알려진 경로로 Tessdata 설정 (경로 찾기 로직 제거)
	os.Setenv("TESSDATA_PREFIX", "/usr/share/tessdata")

	return analyzer, nil
}

// 메인 OCR 처리 함수 (완전한 로직 유지)
func (ocr *OCRAnalyzer) ExtractTexts(imagePath string) ([]TextElement, error) {
	ocr.mu.RLock()
	defer ocr.mu.RUnlock()

	if !ocr.enabled {
		return nil, fmt.Errorf("OCR not enabled")
	}

	// 1. 이미지 로드 및 정보 확인
	img := gocv.IMRead(imagePath, gocv.IMReadColor)
	if img.Empty() {
		return nil, fmt.Errorf("failed to load image")
	}
	defer img.Close()

	var results []TextElement

	// 방법 1: 전체 이미지 OCR
	fullText := ocr.recognizeFullImageSafe(imagePath)
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

	// 방법 2: 텍스트 영역 감지 후 개별 인식
	textRegions := ocr.detectTextRegions(img)

	// 각 영역에서 텍스트 인식 시도
	for _, region := range textRegions {
		text := ocr.recognizeTextInRegionSafe(img, region)
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

// Tesseract 출력 정리 함수 (완전 유지)
func (ocr *OCRAnalyzer) cleanTesseractOutput(rawText string) string {
	if rawText == "" {
		return ""
	}

	cleaned := rawText

	// 1. Tesseract 경고 메시지 제거
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

	// 2. 줄바꿈 정리
	lines := strings.Split(cleaned, "\n")
	var validLines []string

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" && len(line) > 0 {
			validLines = append(validLines, line)
		}
	}

	// 3. 최종 텍스트 조합
	if len(validLines) == 0 {
		return ""
	}

	// 짧은 텍스트들은 공백으로 연결, 긴 텍스트는 줄바꿈 유지
	if len(validLines) == 1 {
		return validLines[0]
	}

	// 여러 줄인 경우, 짧은 것들은 합치고 긴 것들은 분리
	var processedLines []string
	var currentGroup []string

	for _, line := range validLines {
		if len(line) <= 10 { // 짧은 텍스트 (기호, 숫자 등)
			currentGroup = append(currentGroup, line)
		} else { // 긴 텍스트
			if len(currentGroup) > 0 {
				processedLines = append(processedLines, strings.Join(currentGroup, " "))
				currentGroup = []string{}
			}
			processedLines = append(processedLines, line)
		}
	}

	// 남은 그룹 처리
	if len(currentGroup) > 0 {
		processedLines = append(processedLines, strings.Join(currentGroup, " "))
	}

	return strings.Join(processedLines, " | ")
}

// 유효한 텍스트 필터링 (완전 유지)
func (ocr *OCRAnalyzer) filterValidTexts(elements []TextElement) []TextElement {
	var filtered []TextElement

	for _, elem := range elements {
		text := strings.TrimSpace(elem.Text)

		// 기본 필터링
		if len(text) < 1 {
			continue
		}

		// 너무 짧은 무의미한 텍스트 제거
		if len(text) <= 2 && !ocr.isSignificantShortText(text) {
			continue
		}

		// 특수문자만 있는 텍스트 제거
		if ocr.isOnlySpecialChars(text) {
			continue
		}

		// 반복 패턴 제거
		if ocr.isRepeatingPattern(text) {
			continue
		}

		filtered = append(filtered, elem)
	}

	return filtered
}

// 의미있는 짧은 텍스트 판별 (완전 유지)
func (ocr *OCRAnalyzer) isSignificantShortText(text string) bool {
	significantShorts := []string{
		// 숫자
		"0", "1", "2", "3", "4", "5", "6", "7", "8", "9",
		// 한글 단어
		"안", "좋", "나", "다", "를", "을", "의", "에", "로", "과", "와",
		// 영어 단어
		"OK", "NO", "ON", "UP", "GO", "IN", "TO", "AT", "BY",
		// 기호 (의미있는)
		"@", "#", "$", "%", "&", "*", "+", "-", "=", "?", "!",
	}

	for _, significant := range significantShorts {
		if text == significant {
			return true
		}
	}

	// 숫자로만 구성된 경우
	if regexp.MustCompile(`^\d+$`).MatchString(text) {
		return true
	}

	return false
}

// 특수문자만 있는지 확인 (완전 유지)
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

// 반복 패턴 확인 (Go 호환 버전, 완전 유지)
func (ocr *OCRAnalyzer) isRepeatingPattern(text string) bool {
	if len(text) < 3 {
		return false
	}

	// 같은 문자 반복 확인 (예: "---", "...", "aaaa")
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

	// 짧은 패턴 반복 (예: "ababab", "123123")
	for i := 1; i <= len(text)/3; i++ {
		pattern := text[:i]
		if strings.Repeat(pattern, len(text)/i) == text && len(text) >= i*3 {
			return true
		}
	}

	return false
}

// 안전한 전체 이미지 OCR (완전 유지)
func (ocr *OCRAnalyzer) recognizeFullImageSafe(imagePath string) string {
	psmModes := []string{"3", "6"}

	for _, psm := range psmModes {
		text := ocr.runTesseractSafe(imagePath, psm)
		if text != "" && len(strings.TrimSpace(text)) > 0 {
			return text
		}
	}

	return ""
}

// 안전한 영역별 텍스트 인식 (완전 유지)
func (ocr *OCRAnalyzer) recognizeTextInRegionSafe(img gocv.Mat, region image.Rectangle) string {
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

	return ocr.runTesseractSafe(tempFile, "8")
}

// 안전한 Tesseract 실행 (간소화)
func (ocr *OCRAnalyzer) runTesseractSafe(imagePath, psm string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// 언어 옵션 (간소화)
	langOption := "kor+eng"

	cmd := exec.CommandContext(ctx, ocr.tesseractPath, imagePath, "stdout",
		"-l", langOption,
		"--psm", psm)

	cmd.Env = append(os.Environ(), "TESSDATA_PREFIX=/usr/share/tessdata")

	output, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}

	result := strings.TrimSpace(string(output))
	return result
}

// 텍스트 영역 감지 (완전 유지)
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

// 기본 전처리 (완전 유지)
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

// 중복 텍스트 확인 (완전 유지)
func (ocr *OCRAnalyzer) isDuplicateText(text string, existing []TextElement) bool {
	cleanText := strings.ToLower(strings.TrimSpace(text))
	for _, elem := range existing {
		if strings.ToLower(strings.TrimSpace(elem.Text)) == cleanText {
			return true
		}
	}
	return false
}

// 중복 제거 (완전 유지)
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

// 유효한 텍스트인지 검증 (완전 유지)
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

// 전역 변수
var analyzer *OCRAnalyzer

// HTTP 핸들러들 (간소화된 응답만)
func extractHandler(c *gin.Context) {
	file, err := c.FormFile("image")
	if err != nil {
		c.JSON(http.StatusBadRequest, OCRResponse{
			Success: false,
		})
		return
	}

	imagePath := filepath.Join(os.TempDir(), fmt.Sprintf("ocr_input_%s.png", uuid.New().String()[:8]))
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
