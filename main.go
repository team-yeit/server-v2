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
}

// 간소화된 OCR 분석기
type OCRAnalyzer struct {
	tesseractPath string
	enabled       bool
	mu            sync.RWMutex
	debugMode     bool
}

func NewOCRAnalyzer() (*OCRAnalyzer, error) {
	log.Println("🔵 Initializing OCR Analyzer...")

	analyzer := &OCRAnalyzer{
		enabled:   false,
		debugMode: true, // 디버그 모드 활성화
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
			analyzer.enabled = true
			log.Printf("✅ Tesseract found at: %s", path)
			break
		}
	}

	if !analyzer.enabled {
		return nil, fmt.Errorf("tesseract not found in standard locations")
	}

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

// Tesseract 설정 테스트
func (ocr *OCRAnalyzer) testTesseractConfig() {
	log.Println("🔧 Testing Tesseract configuration...")

	// 기본 명령어 테스트
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, ocr.tesseractPath, "--help")
	_, err := cmd.Output()
	if err != nil {
		log.Printf("⚠️ Tesseract help command failed: %v", err)
	} else {
		log.Printf("✅ Tesseract help command successful")
	}

	// PSM 모드 확인
	cmd2 := exec.CommandContext(ctx, ocr.tesseractPath, "--help-psm")
	output2, err := cmd2.Output()
	if err != nil {
		log.Printf("⚠️ PSM help failed: %v", err)
	} else {
		log.Printf("✅ PSM modes available")
		if ocr.debugMode {
			log.Printf("📋 PSM modes:\n%s", string(output2))
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

// 메인 OCR 처리 함수 (개선된 버전)
func (ocr *OCRAnalyzer) ExtractTexts(imagePath string) ([]TextElement, *DebugInfo, error) {
	ocr.mu.RLock()
	defer ocr.mu.RUnlock()

	if !ocr.enabled {
		return nil, nil, fmt.Errorf("OCR not enabled")
	}

	log.Printf("🔍 Extracting texts from: %s", imagePath)
	startTime := time.Now()

	debugInfo := &DebugInfo{}

	// 1. 이미지 로드 및 정보 확인
	img := gocv.IMRead(imagePath, gocv.IMReadColor)
	if img.Empty() {
		return nil, debugInfo, fmt.Errorf("failed to load image")
	}
	defer img.Close()

	debugInfo.ImageSize = fmt.Sprintf("%dx%d", img.Cols(), img.Rows())
	log.Printf("📏 Image size: %s", debugInfo.ImageSize)

	// 2. 전체 이미지에 대해 직접 OCR 시도 (간단한 방법)
	var results []TextElement

	// 방법 1: 전체 이미지 OCR
	fullText := ocr.recognizeFullImage(imagePath, debugInfo)
	if fullText != "" {
		// 중앙 좌표로 설정
		centerX := img.Cols() / 2
		centerY := img.Rows() / 2
		results = append(results, TextElement{
			Text: strings.TrimSpace(fullText),
			X:    centerX,
			Y:    centerY,
		})
		log.Printf("✅ Full image OCR: '%s' at center (%d, %d)", fullText, centerX, centerY)
	}

	// 방법 2: 텍스트 영역 감지 후 개별 인식
	textRegions := ocr.detectTextRegions(img)
	debugInfo.RegionsFound = len(textRegions)
	log.Printf("📍 Found %d text regions", len(textRegions))

	// 각 영역에서 텍스트 인식 시도
	for i, region := range textRegions {
		text := ocr.recognizeTextInRegion(img, region, debugInfo)
		if text != "" && ocr.isValidText(text) && !ocr.isDuplicateText(text, results) {
			results = append(results, TextElement{
				Text: strings.TrimSpace(text),
				X:    region.Min.X + region.Dx()/2,
				Y:    region.Min.Y + region.Dy()/2,
			})
			log.Printf("✅ [%d] '%s' at (%d, %d)", i+1, text, region.Min.X+region.Dx()/2, region.Min.Y+region.Dy()/2)
		}
	}

	// 중복 제거
	results = ocr.removeDuplicates(results)

	processingTime := time.Since(startTime)
	debugInfo.ProcessingTime = processingTime.String()
	log.Printf("🎯 Extracted %d unique texts in %v", len(results), processingTime)

	return results, debugInfo, nil
}

// 전체 이미지에 대한 OCR (가장 간단한 방법)
func (ocr *OCRAnalyzer) recognizeFullImage(imagePath string, debugInfo *DebugInfo) string {
	log.Printf("🖼️ Attempting full image OCR...")

	// 여러 PSM 모드로 시도
	psmModes := []string{"3", "6", "8", "11", "13"}

	for _, psm := range psmModes {
		text := ocr.runTesseractWithPSM(imagePath, psm, debugInfo)
		if text != "" && len(strings.TrimSpace(text)) > 0 {
			log.Printf("✅ Full image OCR successful with PSM %s: '%s'", psm, text)
			return text
		}
	}

	log.Printf("❌ Full image OCR failed with all PSM modes")
	return ""
}

// 텍스트 영역 감지 (더 관대한 설정)
func (ocr *OCRAnalyzer) detectTextRegions(img gocv.Mat) []image.Rectangle {
	gray := gocv.NewMat()
	defer gray.Close()
	gocv.CvtColor(img, &gray, gocv.ColorBGRToGray)

	var allRegions []image.Rectangle

	// 방법 1: 적응형 임계값
	thresh := gocv.NewMat()
	defer thresh.Close()
	gocv.AdaptiveThreshold(gray, &thresh, 255, gocv.AdaptiveThresholdGaussian, gocv.ThresholdBinary, 11, 2)

	regions1 := ocr.findContoursInMat(thresh, "adaptive")
	allRegions = append(allRegions, regions1...)

	// 방법 2: 일반 임계값
	thresh2 := gocv.NewMat()
	defer thresh2.Close()
	gocv.Threshold(gray, &thresh2, 0, 255, gocv.ThresholdBinary+gocv.ThresholdOtsu)

	regions2 := ocr.findContoursInMat(thresh2, "otsu")
	allRegions = append(allRegions, regions2...)

	// 방법 3: 에지 기반
	edges := gocv.NewMat()
	defer edges.Close()
	gocv.Canny(gray, &edges, 50, 150)

	// 형태학적 연산으로 텍스트 영역 연결
	kernel := gocv.GetStructuringElement(gocv.MorphRect, image.Pt(10, 2))
	defer kernel.Close()

	connected := gocv.NewMat()
	defer connected.Close()
	gocv.MorphologyEx(edges, &connected, gocv.MorphClose, kernel)

	regions3 := ocr.findContoursInMat(connected, "edge")
	allRegions = append(allRegions, regions3...)

	// 중복 제거 및 정렬
	uniqueRegions := ocr.removeOverlappingRegions(allRegions)
	log.Printf("📍 Total regions after deduplication: %d", len(uniqueRegions))

	return uniqueRegions
}

// 윤곽선에서 텍스트 영역 찾기
func (ocr *OCRAnalyzer) findContoursInMat(mat gocv.Mat, method string) []image.Rectangle {
	contours := gocv.FindContours(mat, gocv.RetrievalExternal, gocv.ChainApproxSimple)
	defer contours.Close()

	var regions []image.Rectangle
	for i := 0; i < contours.Size(); i++ {
		contour := contours.At(i)
		area := gocv.ContourArea(contour)

		// 더 관대한 필터링 (작은 텍스트도 포함)
		if area > 50 && area < 100000 {
			rect := gocv.BoundingRect(contour)

			// 기본적인 크기 검사만
			if rect.Dx() > 10 && rect.Dy() > 5 && rect.Dy() < 200 {
				// 패딩 추가
				padding := 3
				expandedRect := image.Rect(
					max(0, rect.Min.X-padding),
					max(0, rect.Min.Y-padding),
					min(mat.Cols(), rect.Max.X+padding),
					min(mat.Rows(), rect.Max.Y+padding),
				)
				regions = append(regions, expandedRect)
			}
		}
	}

	log.Printf("📊 Method '%s' found %d potential regions", method, len(regions))
	return regions
}

// 겹치는 영역 제거
func (ocr *OCRAnalyzer) removeOverlappingRegions(regions []image.Rectangle) []image.Rectangle {
	if len(regions) <= 1 {
		return regions
	}

	var unique []image.Rectangle
	for _, region := range regions {
		isOverlapping := false
		for _, existing := range unique {
			if ocr.calculateIOU(region, existing) > 0.3 {
				isOverlapping = true
				break
			}
		}
		if !isOverlapping {
			unique = append(unique, region)
		}
	}

	return unique
}

// IOU (Intersection over Union) 계산
func (ocr *OCRAnalyzer) calculateIOU(rect1, rect2 image.Rectangle) float64 {
	intersection := rect1.Intersect(rect2)
	if intersection.Empty() {
		return 0.0
	}

	intersectionArea := float64(intersection.Dx() * intersection.Dy())
	unionArea := float64(rect1.Dx()*rect1.Dy()+rect2.Dx()*rect2.Dy()) - intersectionArea

	if unionArea == 0 {
		return 0.0
	}

	return intersectionArea / unionArea
}

// 특정 영역에서 텍스트 인식 (개선된 버전)
func (ocr *OCRAnalyzer) recognizeTextInRegion(img gocv.Mat, region image.Rectangle, debugInfo *DebugInfo) string {
	// ROI 추출
	roi := img.Region(region)
	if roi.Empty() {
		return ""
	}
	defer roi.Close()

	// 여러 전처리 방법 시도
	preprocessMethods := []string{"basic", "enhanced", "contrast"}

	for _, method := range preprocessMethods {
		processed := ocr.preprocessImageWithMethod(roi, method)
		if processed.Empty() {
			continue
		}

		// 임시 파일로 저장
		tempFile := filepath.Join(os.TempDir(), fmt.Sprintf("ocr_region_%s_%d.png", method, time.Now().UnixNano()))

		if gocv.IMWrite(tempFile, processed) {
			text := ocr.runTesseractWithPSM(tempFile, "8", debugInfo)
			os.Remove(tempFile)

			if text != "" && ocr.isValidText(text) {
				log.Printf("✅ Region OCR successful with method '%s': '%s'", method, text)
				processed.Close()
				return text
			}
		}

		processed.Close()
	}

	return ""
}

// 다양한 전처리 방법
func (ocr *OCRAnalyzer) preprocessImageWithMethod(roi gocv.Mat, method string) gocv.Mat {
	switch method {
	case "basic":
		return ocr.basicPreprocess(roi)
	case "enhanced":
		return ocr.enhancedPreprocess(roi)
	case "contrast":
		return ocr.contrastPreprocess(roi)
	default:
		return ocr.basicPreprocess(roi)
	}
}

// 기본 전처리
func (ocr *OCRAnalyzer) basicPreprocess(roi gocv.Mat) gocv.Mat {
	gray := gocv.NewMat()
	defer gray.Close()
	gocv.CvtColor(roi, &gray, gocv.ColorBGRToGray)

	// 크기 확대
	enlarged := gocv.NewMat()
	defer enlarged.Close()
	newSize := image.Pt(roi.Cols()*2, roi.Rows()*2)
	gocv.Resize(gray, &enlarged, newSize, 0, 0, gocv.InterpolationCubic)

	// 적응형 임계값
	result := gocv.NewMat()
	gocv.AdaptiveThreshold(enlarged, &result, 255, gocv.AdaptiveThresholdGaussian, gocv.ThresholdBinary, 11, 2)

	return result
}

// 향상된 전처리
func (ocr *OCRAnalyzer) enhancedPreprocess(roi gocv.Mat) gocv.Mat {
	gray := gocv.NewMat()
	defer gray.Close()
	gocv.CvtColor(roi, &gray, gocv.ColorBGRToGray)

	// 노이즈 제거
	denoised := gocv.NewMat()
	defer denoised.Close()
	gocv.MedianBlur(gray, &denoised, 3)

	// 크기 확대
	enlarged := gocv.NewMat()
	defer enlarged.Close()
	newSize := image.Pt(roi.Cols()*3, roi.Rows()*3)
	gocv.Resize(denoised, &enlarged, newSize, 0, 0, gocv.InterpolationCubic)

	// Otsu 임계값
	result := gocv.NewMat()
	gocv.Threshold(enlarged, &result, 0, 255, gocv.ThresholdBinary+gocv.ThresholdOtsu)

	return result
}

// 대비 향상 전처리 (CLAHE 대신 히스토그램 균등화 사용)
func (ocr *OCRAnalyzer) contrastPreprocess(roi gocv.Mat) gocv.Mat {
	gray := gocv.NewMat()
	defer gray.Close()
	gocv.CvtColor(roi, &gray, gocv.ColorBGRToGray)

	// 히스토그램 균등화로 대비 향상
	enhanced := gocv.NewMat()
	defer enhanced.Close()
	gocv.EqualizeHist(gray, &enhanced)

	// 크기 확대
	enlarged := gocv.NewMat()
	defer enlarged.Close()
	newSize := image.Pt(roi.Cols()*2, roi.Rows()*2)
	gocv.Resize(enhanced, &enlarged, newSize, 0, 0, gocv.InterpolationCubic)

	// 적응형 임계값
	result := gocv.NewMat()
	gocv.AdaptiveThreshold(enlarged, &result, 255, gocv.AdaptiveThresholdMean, gocv.ThresholdBinary, 15, 4)

	return result
}

// PSM 모드를 지정하여 Tesseract 실행
func (ocr *OCRAnalyzer) runTesseractWithPSM(imagePath, psm string, debugInfo *DebugInfo) string {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// 더 간단한 명령어로 시도
	var cmd *exec.Cmd

	// 언어 설정을 더 간단하게
	availableLangs := ocr.getAvailableLanguages()
	langOption := "eng"

	// 한국어가 있으면 추가
	for _, lang := range availableLangs {
		if lang == "kor" {
			langOption = "kor+eng"
			break
		}
	}

	cmd = exec.CommandContext(ctx, ocr.tesseractPath, imagePath, "stdout",
		"-l", langOption,
		"--psm", psm)

	output, err := cmd.CombinedOutput() // stderr도 함께 가져오기
	if err != nil {
		errorMsg := fmt.Sprintf("PSM %s failed: %v, output: %s", psm, err, string(output))
		log.Printf("⚠️ %s", errorMsg)
		if debugInfo != nil {
			debugInfo.TesseractErrors = append(debugInfo.TesseractErrors, errorMsg)
		}
		return ""
	}

	result := strings.TrimSpace(string(output))
	if result != "" {
		log.Printf("✅ Tesseract PSM %s success: '%s'", psm, result)
	}

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

// 유효한 텍스트인지 검증 (더 관대하게)
func (ocr *OCRAnalyzer) isValidText(text string) bool {
	if len(text) < 1 {
		return false
	}

	// 공백 제거 후 체크
	trimmed := strings.TrimSpace(text)
	if len(trimmed) < 1 {
		return false
	}

	// 너무 긴 텍스트는 제외
	runeCount := len([]rune(trimmed))
	if runeCount > 100 {
		return false
	}

	// 의미있는 문자가 있는지 확인
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

// 메인 OCR 추출 핸들러 (디버그 정보 포함)
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

	// 임시 파일 저장
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

	// OCR 처리 (디버그 정보 포함)
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
	log.Printf("✅ [%s] Completed in %v - extracted %d texts", requestID, processingTime, len(texts))

	// 디버그 모드에서는 더 자세한 정보 제공
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
		"status":       status,
		"timestamp":    time.Now().Unix(),
		"ocr_enabled":  analyzer != nil && analyzer.enabled,
		"service_type": "OCR Text Extraction",
		"debug_mode":   analyzer != nil && analyzer.debugMode,
	}

	if analyzer != nil && analyzer.enabled {
		healthInfo["tesseract_path"] = analyzer.tesseractPath
		healthInfo["tesseract_version"] = analyzer.getTesseractVersion()
		healthInfo["available_languages"] = analyzer.getAvailableLanguages()
	}

	c.JSON(http.StatusOK, healthInfo)
}

// 루트 핸들러
func rootHandler(c *gin.Context) {
	c.JSON(http.StatusOK, map[string]interface{}{
		"service":     "Improved OCR Text Extraction Service",
		"version":     "1.1.0",
		"description": "Upload image, get text coordinates - Improved with debugging!",
		"features": []string{
			"🔵 Advanced OCR text detection and recognition",
			"🌐 Korean + English support",
			"📍 Text coordinates extraction",
			"⚡ Multiple preprocessing methods",
			"🔧 Debug mode for troubleshooting",
			"🎯 Multiple PSM modes for better accuracy",
		},
		"usage": map[string]string{
			"endpoint":   "POST /extract",
			"input":      "multipart/form-data with 'image' field",
			"output":     "JSON with text_list: [{text, x, y}, ...]",
			"debug_mode": "Add ?debug=true for detailed debug info",
		},
		"example": "curl -X POST -F \"image=@screenshot.png\" http://localhost:8000/extract?debug=true",
	})
}

func main() {
	log.Println("🚀 Starting Improved OCR Text Extraction Service...")

	// OCR 분석기 초기화
	var err error
	analyzer, err = NewOCRAnalyzer()
	if err != nil {
		log.Fatalf("❌ Failed to initialize OCR: %v", err)
	}

	// Gin 설정
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Logger())
	r.Use(gin.Recovery())

	// CORS 설정
	config := cors.DefaultConfig()
	config.AllowAllOrigins = true
	config.AllowMethods = []string{"GET", "POST", "OPTIONS"}
	config.AllowHeaders = []string{"Origin", "Content-Length", "Content-Type"}
	r.Use(cors.New(config))

	// 라우트 설정
	r.GET("/", rootHandler)
	r.GET("/health", healthHandler)
	r.POST("/extract", extractHandler)

	// 포트 설정
	port := os.Getenv("PORT")
	if port == "" {
		port = "8000"
	}

	log.Printf("✅ Improved OCR Service ready on port %s", port)
	log.Printf("🔵 Tesseract enabled: %t", analyzer.enabled)
	log.Printf("🌐 Languages: Korean + English")
	log.Printf("🔧 Debug mode: %t", analyzer.debugMode)
	log.Printf("📋 Usage: POST /extract with image file")
	log.Printf("🎯 Debug: Add ?debug=true for detailed info")

	if err := r.Run(":" + port); err != nil {
		log.Fatalf("❌ Server failed: %v", err)
	}
}
