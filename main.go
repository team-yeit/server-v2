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
}

// 간소화된 OCR 분석기
type OCRAnalyzer struct {
	tesseractPath string
	enabled       bool
	mu            sync.RWMutex
}

func NewOCRAnalyzer() (*OCRAnalyzer, error) {
	log.Println("🔵 Initializing OCR Analyzer...")

	analyzer := &OCRAnalyzer{
		enabled: false,
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

	log.Println("✅ OCR Analyzer initialized successfully")
	return analyzer, nil
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

// 메인 OCR 처리 함수
func (ocr *OCRAnalyzer) ExtractTexts(imagePath string) ([]TextElement, error) {
	ocr.mu.RLock()
	defer ocr.mu.RUnlock()

	if !ocr.enabled {
		return nil, fmt.Errorf("OCR not enabled")
	}

	log.Printf("🔍 Extracting texts from: %s", imagePath)
	startTime := time.Now()

	// 1. 이미지 로드
	img := gocv.IMRead(imagePath, gocv.IMReadColor)
	if img.Empty() {
		return nil, fmt.Errorf("failed to load image")
	}
	defer img.Close()

	// 2. 텍스트 영역 감지
	textRegions := ocr.detectTextRegions(img)
	log.Printf("📍 Found %d text regions", len(textRegions))

	// 3. 각 영역에서 텍스트 인식
	var results []TextElement
	for i, region := range textRegions {
		text := ocr.recognizeTextInRegion(img, region)
		if text != "" && ocr.isValidText(text) {
			results = append(results, TextElement{
				Text: strings.TrimSpace(text),
				X:    region.Min.X + region.Dx()/2,
				Y:    region.Min.Y + region.Dy()/2,
			})
			log.Printf("✅ [%d] '%s' at (%d, %d)", i+1, text, region.Min.X+region.Dx()/2, region.Min.Y+region.Dy()/2)
		}
	}

	log.Printf("🎯 Extracted %d texts in %v", len(results), time.Since(startTime))
	return results, nil
}

// 텍스트 영역 감지 (형태학적 연산 사용)
func (ocr *OCRAnalyzer) detectTextRegions(img gocv.Mat) []image.Rectangle {
	gray := gocv.NewMat()
	defer gray.Close()
	gocv.CvtColor(img, &gray, gocv.ColorBGRToGray)

	// 적응형 임계값 적용
	thresh := gocv.NewMat()
	defer thresh.Close()
	gocv.AdaptiveThreshold(gray, &thresh, 255, gocv.AdaptiveThresholdGaussian, gocv.ThresholdBinary, 11, 2)

	// 텍스트 라인 감지를 위한 수평 커널
	kernel := gocv.GetStructuringElement(gocv.MorphRect, image.Pt(20, 2))
	defer kernel.Close()

	textMask := gocv.NewMat()
	defer textMask.Close()
	gocv.MorphologyEx(thresh, &textMask, gocv.MorphClose, kernel)

	// 윤곽선 찾기
	contours := gocv.FindContours(textMask, gocv.RetrievalExternal, gocv.ChainApproxSimple)
	defer contours.Close()

	var regions []image.Rectangle
	for i := 0; i < contours.Size(); i++ {
		contour := contours.At(i)
		area := gocv.ContourArea(contour)

		// 텍스트 영역 필터링 (크기와 종횡비 고려)
		if area > 200 && area < 50000 {
			rect := gocv.BoundingRect(contour)
			aspectRatio := float64(rect.Dx()) / float64(rect.Dy())

			// 텍스트 특성: 가로가 세로보다 길고 적절한 크기
			if aspectRatio > 1.0 && rect.Dx() > 15 && rect.Dy() > 8 && rect.Dy() < 100 {
				// 패딩 추가
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

// 특정 영역에서 텍스트 인식
func (ocr *OCRAnalyzer) recognizeTextInRegion(img gocv.Mat, region image.Rectangle) string {
	// ROI 추출
	roi := img.Region(region)
	if roi.Empty() {
		return ""
	}
	defer roi.Close()

	// 이미지 전처리 (OCR 정확도 향상)
	processed := ocr.preprocessImage(roi)
	defer processed.Close()

	// 임시 파일로 저장
	tempFile := filepath.Join(os.TempDir(), fmt.Sprintf("ocr_%d.png", time.Now().UnixNano()))
	defer os.Remove(tempFile)

	if !gocv.IMWrite(tempFile, processed) {
		return ""
	}

	// Tesseract 실행
	return ocr.runTesseract(tempFile)
}

// 이미지 전처리 (OCR 정확도 향상)
func (ocr *OCRAnalyzer) preprocessImage(roi gocv.Mat) gocv.Mat {
	// 그레이스케일 변환
	gray := gocv.NewMat()
	defer gray.Close()
	gocv.CvtColor(roi, &gray, gocv.ColorBGRToGray)

	// 크기 확대 (OCR 정확도 향상)
	enlarged := gocv.NewMat()
	defer enlarged.Close()
	newSize := image.Pt(roi.Cols()*2, roi.Rows()*2)
	gocv.Resize(gray, &enlarged, newSize, 0, 0, gocv.InterpolationCubic)

	// 적응형 임계값
	result := gocv.NewMat()
	gocv.AdaptiveThreshold(enlarged, &result, 255, gocv.AdaptiveThresholdGaussian, gocv.ThresholdBinary, 11, 2)

	return result
}

// Tesseract 실행
func (ocr *OCRAnalyzer) runTesseract(imagePath string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 한국어 + 영어 동시 인식
	cmd := exec.CommandContext(ctx, ocr.tesseractPath, imagePath, "stdout",
		"-l", "kor+eng",
		"--psm", "8",
		"-c", "tessedit_char_whitelist=0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz가-힣ㄱ-ㅎㅏ-ㅣ.,!?:;()[]{}\"'-+=@ ")

	output, err := cmd.Output()
	if err != nil {
		log.Printf("Tesseract failed: %v", err)
		return ""
	}

	return strings.TrimSpace(string(output))
}

// 유효한 텍스트인지 검증
func (ocr *OCRAnalyzer) isValidText(text string) bool {
	if len(text) < 1 {
		return false
	}

	// 너무 짧거나 긴 텍스트 필터링
	runeCount := len([]rune(text))
	if runeCount < 1 || runeCount > 50 {
		return false
	}

	// 의미있는 문자 비율 확인
	validChars := 0
	for _, r := range text {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsSpace(r) || unicode.IsPunct(r) {
			validChars++
		}
	}

	validRatio := float64(validChars) / float64(len(text))
	return validRatio > 0.7
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

	// OCR 처리
	texts, err := analyzer.ExtractTexts(imagePath)
	if err != nil {
		log.Printf("❌ [%s] OCR failed: %v", requestID, err)
		c.JSON(http.StatusInternalServerError, OCRResponse{
			Success: false,
			Message: "OCR processing failed: " + err.Error(),
		})
		return
	}

	processingTime := time.Since(startTime)
	log.Printf("✅ [%s] Completed in %v - extracted %d texts", requestID, processingTime, len(texts))

	c.JSON(http.StatusOK, OCRResponse{
		Success:    true,
		TextList:   texts,
		TotalCount: len(texts),
		Message:    fmt.Sprintf("Successfully extracted %d texts", len(texts)),
	})
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
		"service":     "Simple OCR Text Extraction Service",
		"version":     "1.0.0",
		"description": "Upload image, get text coordinates - Simple and Fast!",
		"features": []string{
			"🔵 OCR text detection and recognition",
			"🌐 Korean + English support",
			"📍 Text coordinates extraction",
			"⚡ Fast and lightweight",
			"🎯 Simple API - just upload and get results",
		},
		"usage": map[string]string{
			"endpoint": "POST /extract",
			"input":    "multipart/form-data with 'image' field",
			"output":   "JSON with text_list: [{text, x, y}, ...]",
		},
		"example": "curl -X POST -F \"image=@screenshot.png\" http://localhost:8000/extract",
	})
}

func main() {
	log.Println("🚀 Starting Simple OCR Text Extraction Service...")

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

	log.Printf("✅ OCR Service ready on port %s", port)
	log.Printf("🔵 Tesseract enabled: %t", analyzer.enabled)
	log.Printf("🌐 Languages: Korean + English")
	log.Printf("📋 Usage: POST /extract with image file")
	log.Printf("🎯 Returns: [{\"text\":\"텍스트\", \"x\":100, \"y\":200}, ...]")

	if err := r.Run(":" + port); err != nil {
		log.Fatalf("❌ Server failed: %v", err)
	}
}
