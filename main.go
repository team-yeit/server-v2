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

// 간소화된 텍스트 요소 구조체
type TextElement struct {
	Text string `json:"text"`
	X    int    `json:"x"`
	Y    int    `json:"y"`
}

// API 응답 구조체
type OCRResponse struct {
	Success    bool          `json:"success"`
	TextList   []TextElement `json:"text_list"`
	TotalCount int           `json:"total_count"`
	Message    string        `json:"message,omitempty"`
	DebugInfo  *DebugInfo    `json:"debug_info,omitempty"`
}

// 디버깅 정보
type DebugInfo struct {
	RegionsFound    int      `json:"regions_found"`
	ProcessingTime  string   `json:"processing_time"`
	TesseractErrors []string `json:"tesseract_errors,omitempty"`
	ImageSize       string   `json:"image_size,omitempty"`
	TessdataPath    string   `json:"tessdata_path,omitempty"`
	CleanedTexts    int      `json:"cleaned_texts,omitempty"`
}

// 간소화된 OCR 분석기
type OCRAnalyzer struct {
	tesseractPath string
	tessdataPath  string
	enabled       bool
	mu            sync.RWMutex
	debugMode     bool
}

func NewOCRAnalyzer() (*OCRAnalyzer, error) {
	log.Println("🔵 Initializing OCR Analyzer...")

	analyzer := &OCRAnalyzer{
		enabled:   false,
		debugMode: true,
	}

	// Tesseract 경로 찾기
	tesseractPaths := []string{
		"/usr/bin/tesseract",
		"/usr/local/bin/tesseract",
		"tesseract",
	}

	for _, path := range tesseractPaths {
		if _, err := os.Stat(path); err == nil {
			analyzer.tesseractPath = path
			log.Printf("✅ Tesseract found at: %s", path)
			break
		}
	}

	if analyzer.tesseractPath == "" {
		return nil, fmt.Errorf("tesseract not found in standard locations")
	}

	// Tessdata 경로 찾기 및 설정
	if err := analyzer.findAndSetTessdataPath(); err != nil {
		return nil, fmt.Errorf("failed to find tessdata: %v", err)
	}

	analyzer.enabled = true

	// 버전 확인
	if version := analyzer.getTesseractVersion(); version != "" {
		log.Printf("📋 Tesseract version: %s", version)
	}

	// 언어 확인
	if langs := analyzer.getAvailableLanguages(); len(langs) > 0 {
		log.Printf("🌐 Available languages: %v", langs)
	}

	// Tesseract 설정 테스트
	analyzer.testTesseractConfig()

	log.Println("✅ OCR Analyzer initialized successfully")
	return analyzer, nil
}

// Tessdata 경로 찾기 및 환경변수 설정
func (ocr *OCRAnalyzer) findAndSetTessdataPath() error {
	// 가능한 tessdata 경로들
	tessdataPaths := []string{
		"/usr/share/tesseract-ocr/4.00/tessdata",
		"/usr/share/tesseract-ocr/5/tessdata",
		"/usr/share/tesseract-ocr/tessdata",
		"/usr/share/tessdata",
		"/usr/local/share/tessdata",
		"/usr/local/share/tesseract-ocr/tessdata",
		"/opt/homebrew/share/tessdata", // macOS Homebrew
	}

	// 환경변수에서 먼저 확인
	if envPath := os.Getenv("TESSDATA_PREFIX"); envPath != "" {
		if _, err := os.Stat(filepath.Join(envPath, "eng.traineddata")); err == nil {
			ocr.tessdataPath = envPath
			log.Printf("✅ Using TESSDATA_PREFIX: %s", envPath)
			return nil
		}
	}

	// 가능한 경로들 순차 확인
	for _, path := range tessdataPaths {
		// eng.traineddata 파일이 있는지 확인
		engFile := filepath.Join(path, "eng.traineddata")
		if _, err := os.Stat(engFile); err == nil {
			ocr.tessdataPath = path
			// 환경변수 설정
			os.Setenv("TESSDATA_PREFIX", path)
			log.Printf("✅ Found tessdata at: %s", path)
			log.Printf("📁 Set TESSDATA_PREFIX=%s", path)
			return nil
		}
	}

	return fmt.Errorf("tessdata directory not found in standard locations")
}

// Tesseract 설정 테스트
func (ocr *OCRAnalyzer) testTesseractConfig() {
	log.Println("🔧 Testing Tesseract configuration...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, ocr.tesseractPath, "--list-langs")
	output, err := cmd.Output()
	if err != nil {
		log.Printf("⚠️ Tesseract language list failed: %v", err)
		if ocr.tessdataPath != "" {
			os.Setenv("TESSDATA_PREFIX", ocr.tessdataPath)
			log.Printf("🔄 Retrying with TESSDATA_PREFIX=%s", ocr.tessdataPath)
		}
	} else {
		log.Printf("✅ Tesseract language test successful")
		if ocr.debugMode {
			log.Printf("📋 Available languages:\n%s", string(output))
		}
	}
}

func (ocr *OCRAnalyzer) getTesseractVersion() string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, ocr.tesseractPath, "--version")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}

	lines := strings.Split(string(output), "\n")
	if len(lines) > 0 {
		parts := strings.Fields(lines[0])
		if len(parts) >= 2 {
			return parts[1]
		}
	}
	return ""
}

func (ocr *OCRAnalyzer) getAvailableLanguages() []string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, ocr.tesseractPath, "--list-langs")
	output, err := cmd.Output()
	if err != nil {
		return []string{}
	}

	lines := strings.Split(string(output), "\n")
	var languages []string

	for i, line := range lines {
		if i == 0 {
			continue // 헤더 스킵
		}
		lang := strings.TrimSpace(line)
		if lang != "" {
			languages = append(languages, lang)
		}
	}

	return languages
}

// 메인 OCR 처리 함수 (텍스트 정리 개선)
func (ocr *OCRAnalyzer) ExtractTexts(imagePath string) ([]TextElement, *DebugInfo, error) {
	ocr.mu.RLock()
	defer ocr.mu.RUnlock()

	if !ocr.enabled {
		return nil, nil, fmt.Errorf("OCR not enabled")
	}

	log.Printf("🔍 Extracting texts from: %s", imagePath)
	startTime := time.Now()

	debugInfo := &DebugInfo{
		TessdataPath: ocr.tessdataPath,
	}

	// 환경변수 재설정 (안전장치)
	if ocr.tessdataPath != "" {
		os.Setenv("TESSDATA_PREFIX", ocr.tessdataPath)
	}

	// 1. 이미지 로드 및 정보 확인
	img := gocv.IMRead(imagePath, gocv.IMReadColor)
	if img.Empty() {
		return nil, debugInfo, fmt.Errorf("failed to load image")
	}
	defer img.Close()

	debugInfo.ImageSize = fmt.Sprintf("%dx%d", img.Cols(), img.Rows())
	log.Printf("📏 Image size: %s", debugInfo.ImageSize)

	var results []TextElement

	// 방법 1: 전체 이미지 OCR
	fullText := ocr.recognizeFullImageSafe(imagePath, debugInfo)
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
			log.Printf("✅ Full image OCR (cleaned): '%s' at center (%d, %d)", cleanedText, centerX, centerY)
		}
	}

	// 방법 2: 텍스트 영역 감지 후 개별 인식
	textRegions := ocr.detectTextRegions(img)
	debugInfo.RegionsFound = len(textRegions)
	log.Printf("📍 Found %d text regions", len(textRegions))

	// 각 영역에서 텍스트 인식 시도
	for i, region := range textRegions {
		text := ocr.recognizeTextInRegionSafe(img, region, debugInfo)
		cleanedText := ocr.cleanTesseractOutput(text)

		if cleanedText != "" && ocr.isValidText(cleanedText) && !ocr.isDuplicateText(cleanedText, results) {
			results = append(results, TextElement{
				Text: cleanedText,
				X:    region.Min.X + region.Dx()/2,
				Y:    region.Min.Y + region.Dy()/2,
			})
			log.Printf("✅ [%d] '%s' at (%d, %d)", i+1, cleanedText, region.Min.X+region.Dx()/2, region.Min.Y+region.Dy()/2)
		}
	}

	// 중복 제거 및 정리
	results = ocr.removeDuplicates(results)
	results = ocr.filterValidTexts(results)

	debugInfo.CleanedTexts = len(results)

	processingTime := time.Since(startTime)
	debugInfo.ProcessingTime = processingTime.String()
	log.Printf("🎯 Extracted %d clean texts in %v", len(results), processingTime)

	return results, debugInfo, nil
}

// Tesseract 출력 정리 함수
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

// 유효한 텍스트 필터링
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

// 의미있는 짧은 텍스트 판별
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

// 특수문자만 있는지 확인
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

// 반복 패턴 확인 (Go 호환 버전)
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

// 안전한 전체 이미지 OCR
func (ocr *OCRAnalyzer) recognizeFullImageSafe(imagePath string, debugInfo *DebugInfo) string {
	log.Printf("🖼️ Attempting full image OCR...")

	psmModes := []string{"3", "6"}

	for _, psm := range psmModes {
		text := ocr.runTesseractSafe(imagePath, psm, debugInfo)
		if text != "" && len(strings.TrimSpace(text)) > 0 {
			log.Printf("✅ Full image OCR successful with PSM %s", psm)
			return text
		}
	}

	log.Printf("❌ Full image OCR failed with all PSM modes")
	return ""
}

// 안전한 영역별 텍스트 인식
func (ocr *OCRAnalyzer) recognizeTextInRegionSafe(img gocv.Mat, region image.Rectangle, debugInfo *DebugInfo) string {
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

	return ocr.runTesseractSafe(tempFile, "8", debugInfo)
}

// 안전한 Tesseract 실행
func (ocr *OCRAnalyzer) runTesseractSafe(imagePath, psm string, debugInfo *DebugInfo) string {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if ocr.tessdataPath != "" {
		os.Setenv("TESSDATA_PREFIX", ocr.tessdataPath)
	}

	availableLangs := ocr.getAvailableLanguages()

	var langOptions []string

	for _, lang := range availableLangs {
		if lang == "eng" {
			langOptions = append(langOptions, "eng")
			break
		}
	}

	for _, lang := range availableLangs {
		if lang == "kor" {
			if len(langOptions) > 0 {
				langOptions = []string{"kor+eng"}
			} else {
				langOptions = []string{"kor"}
			}
			break
		}
	}

	if len(langOptions) == 0 {
		langOptions = []string{"eng"}
	}

	for _, langOption := range langOptions {
		cmd := exec.CommandContext(ctx, ocr.tesseractPath, imagePath, "stdout",
			"-l", langOption,
			"--psm", psm)

		cmd.Env = append(os.Environ(),
			fmt.Sprintf("TESSDATA_PREFIX=%s", ocr.tessdataPath))

		output, err := cmd.CombinedOutput()
		if err != nil {
			errorMsg := fmt.Sprintf("Lang %s PSM %s failed: %v", langOption, psm, err)
			if debugInfo != nil {
				debugInfo.TesseractErrors = append(debugInfo.TesseractErrors, errorMsg)
			}
			continue
		}

		result := strings.TrimSpace(string(output))
		if result != "" {
			log.Printf("✅ Tesseract successful with lang %s PSM %s", langOption, psm)
			return result
		}
	}

	return ""
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

// 기본 전처리
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

// 중복 텍스트 확인
func (ocr *OCRAnalyzer) isDuplicateText(text string, existing []TextElement) bool {
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

// 유효한 텍스트인지 검증
func (ocr *OCRAnalyzer) isValidText(text string) bool {
	if len(text) < 1 {
		return false
	}

	trimmed := strings.TrimSpace(text)
	if len(trimmed) < 1 {
		return false
	}

	runeCount := len([]rune(trimmed))
	if runeCount > 200 { // 더 관대하게
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

// HTTP 핸들러들
var analyzer *OCRAnalyzer

// 메인 OCR 추출 핸들러
func extractHandler(c *gin.Context) {
	requestID := uuid.New().String()[:8]
	startTime := time.Now()

	log.Printf("📥 [%s] OCR extraction request received", requestID)

	file, err := c.FormFile("image")
	if err != nil {
		log.Printf("❌ [%s] No image file: %v", requestID, err)
		c.JSON(http.StatusBadRequest, OCRResponse{
			Success: false,
			Message: "Image file required",
		})
		return
	}

	imagePath := filepath.Join(os.TempDir(), fmt.Sprintf("ocr_input_%s.png", requestID))
	defer os.Remove(imagePath)

	if err := c.SaveUploadedFile(file, imagePath); err != nil {
		log.Printf("❌ [%s] Failed to save file: %v", requestID, err)
		c.JSON(http.StatusInternalServerError, OCRResponse{
			Success: false,
			Message: "Failed to save image",
		})
		return
	}

	texts, debugInfo, err := analyzer.ExtractTexts(imagePath)
	if err != nil {
		log.Printf("❌ [%s] OCR failed: %v", requestID, err)
		c.JSON(http.StatusInternalServerError, OCRResponse{
			Success:   false,
			Message:   "OCR processing failed: " + err.Error(),
			DebugInfo: debugInfo,
		})
		return
	}

	processingTime := time.Since(startTime)
	log.Printf("✅ [%s] Completed in %v - extracted %d clean texts", requestID, processingTime, len(texts))

	showDebug := c.Query("debug") == "true" || analyzer.debugMode

	response := OCRResponse{
		Success:    true,
		TextList:   texts,
		TotalCount: len(texts),
		Message:    fmt.Sprintf("Successfully extracted %d texts", len(texts)),
	}

	if showDebug {
		response.DebugInfo = debugInfo
	}

	c.JSON(http.StatusOK, response)
}

// 건강 상태 체크
func healthHandler(c *gin.Context) {
	status := "healthy"
	if analyzer == nil || !analyzer.enabled {
		status = "unhealthy"
	}

	healthInfo := map[string]interface{}{
		"status":        status,
		"timestamp":     time.Now().Unix(),
		"ocr_enabled":   analyzer != nil && analyzer.enabled,
		"service_type":  "Clean OCR Text Extraction",
		"debug_mode":    analyzer != nil && analyzer.debugMode,
		"tessdata_path": "",
	}

	if analyzer != nil && analyzer.enabled {
		healthInfo["tesseract_path"] = analyzer.tesseractPath
		healthInfo["tesseract_version"] = analyzer.getTesseractVersion()
		healthInfo["available_languages"] = analyzer.getAvailableLanguages()
		healthInfo["tessdata_path"] = analyzer.tessdataPath
	}

	c.JSON(http.StatusOK, healthInfo)
}

// 루트 핸들러
func rootHandler(c *gin.Context) {
	c.JSON(http.StatusOK, map[string]interface{}{
		"service":     "Clean OCR Text Extraction Service",
		"version":     "1.3.0",
		"description": "Upload image, get clean text coordinates - Warning messages filtered!",
		"features": []string{
			"🔵 Advanced OCR text detection and recognition",
			"🌐 Korean + English support",
			"📍 Text coordinates extraction",
			"🧹 Clean output (filtered warnings)",
			"⚡ Tessdata path auto-detection",
			"🔧 Debug mode for troubleshooting",
			"🎯 Smart text filtering",
		},
		"usage": map[string]string{
			"endpoint":   "POST /extract",
			"input":      "multipart/form-data with 'image' field",
			"output":     "JSON with clean text_list: [{text, x, y}, ...]",
			"debug_mode": "Add ?debug=true for detailed debug info",
		},
		"example": "curl -X POST -F \"image=@screenshot.png\" http://localhost:8000/extract",
	})
}

func main() {
	log.Println("🚀 Starting Clean OCR Text Extraction Service...")

	var err error
	analyzer, err = NewOCRAnalyzer()
	if err != nil {
		log.Fatalf("❌ Failed to initialize OCR: %v", err)
	}

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Logger())
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

	log.Printf("✅ Clean OCR Service ready on port %s", port)
	log.Printf("🔵 Tesseract enabled: %t", analyzer.enabled)
	log.Printf("📁 Tessdata path: %s", analyzer.tessdataPath)
	log.Printf("🌐 Languages: Korean + English")
	log.Printf("🧹 Clean output: Warning messages filtered")
	log.Printf("📋 Usage: POST /extract with image file")

	if err := r.Run(":" + port); err != nil {
		log.Fatalf("❌ Server failed: %v", err)
	}
}
