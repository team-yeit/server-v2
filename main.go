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
	analyzer := &OCRAnalyzer{tesseractPath: "/usr/bin/tesseract", enabled: false}
	log.Printf("[OCR INITIALIZATION] Starting OCR analyzer initialization process with tesseract path: %s", analyzer.tesseractPath)

	if _, err := os.Stat(analyzer.tesseractPath); err != nil {
		log.Printf("[OCR INITIALIZATION ERROR] Tesseract binary not found at expected path %s, system check failed with error: %v", analyzer.tesseractPath, err)
		return nil, fmt.Errorf("tesseract not found")
	}

	tessDataPath := "/usr/share/tesseract-ocr/4.00/tessdata"
	os.Setenv("TESSDATA_PREFIX", tessDataPath)
	analyzer.enabled = true

	log.Printf("[OCR INITIALIZATION SUCCESS] OCR analyzer successfully initialized, tesseract binary found, TESSDATA_PREFIX set to %s, analyzer enabled status: %t", tessDataPath, analyzer.enabled)
	return analyzer, nil
}

func (ocr *OCRAnalyzer) ExtractTexts(imagePath string) ([]TextElement, error) {
	startTime := time.Now()
	log.Printf("[OCR EXTRACTION START] Beginning text extraction process for image: %s at timestamp %v", imagePath, startTime)

	ocr.mu.RLock()
	defer ocr.mu.RUnlock()

	if !ocr.enabled {
		log.Printf("[OCR EXTRACTION ERROR] OCR analyzer is not enabled, cannot proceed with text extraction for image: %s", imagePath)
		return nil, fmt.Errorf("OCR not enabled")
	}

	img := gocv.IMRead(imagePath, gocv.IMReadColor)
	if img.Empty() {
		log.Printf("[OCR EXTRACTION ERROR] Failed to load image from path %s, image appears to be empty or corrupted", imagePath)
		return nil, fmt.Errorf("failed to load image")
	}
	defer img.Close()

	log.Printf("[OCR EXTRACTION INFO] Successfully loaded image %s with dimensions %dx%d, channels: %d", imagePath, img.Cols(), img.Rows(), img.Channels())

	var results []TextElement

	fullTextStart := time.Now()
	fullText := ocr.recognizeFullImage(imagePath)
	fullTextDuration := time.Since(fullTextStart)
	log.Printf("[OCR FULL IMAGE] Full image OCR completed in %v, raw text length: %d characters", fullTextDuration, len(fullText))

	if fullText != "" {
		cleanedText := ocr.cleanTesseractOutput(fullText)
		log.Printf("[OCR FULL IMAGE] Text cleaning completed, original length: %d, cleaned length: %d", len(fullText), len(cleanedText))

		if cleanedText != "" && ocr.isValidText(cleanedText) {
			centerX, centerY := img.Cols()/2, img.Rows()/2
			results = append(results, TextElement{Text: cleanedText, X: centerX, Y: centerY})
			log.Printf("[OCR FULL IMAGE] Valid text found and added to results: '%s' at position (%d, %d)", cleanedText, centerX, centerY)
		}
	}

	regionDetectionStart := time.Now()
	textRegions := ocr.detectTextRegions(img)
	regionDetectionDuration := time.Since(regionDetectionStart)
	log.Printf("[OCR REGION DETECTION] Text region detection completed in %v, found %d potential text regions", regionDetectionDuration, len(textRegions))

	for i, region := range textRegions {
		regionStart := time.Now()
		text := ocr.recognizeTextInRegion(img, region)
		regionDuration := time.Since(regionStart)
		log.Printf("[OCR REGION %d] Region OCR completed in %v, region bounds: (%d,%d)-(%d,%d), raw text: '%s'",
			i+1, regionDuration, region.Min.X, region.Min.Y, region.Max.X, region.Max.Y, text)

		cleanedText := ocr.cleanTesseractOutput(text)

		if cleanedText != "" && ocr.isValidText(cleanedText) && !ocr.isDuplicateText(cleanedText, results) {
			centerX, centerY := region.Min.X+region.Dx()/2, region.Min.Y+region.Dy()/2
			results = append(results, TextElement{Text: cleanedText, X: centerX, Y: centerY})
			log.Printf("[OCR REGION %d] Valid unique text added: '%s' at position (%d, %d)", i+1, cleanedText, centerX, centerY)
		} else {
			log.Printf("[OCR REGION %d] Text rejected - cleaned: '%s', valid: %t, duplicate: %t",
				i+1, cleanedText, ocr.isValidText(cleanedText), ocr.isDuplicateText(cleanedText, results))
		}
	}

	initialCount := len(results)
	results = ocr.removeDuplicates(results)
	results = ocr.filterValidTexts(results)
	finalCount := len(results)

	totalDuration := time.Since(startTime)
	log.Printf("[OCR EXTRACTION COMPLETE] Text extraction completed in %v, initial results: %d, final results: %d, duplicates removed: %d",
		totalDuration, initialCount, finalCount, initialCount-len(ocr.removeDuplicates(results)))

	return results, nil
}

func (ocr *OCRAnalyzer) cleanTesseractOutput(rawText string) string {
	if rawText == "" {
		return ""
	}

	log.Printf("[OCR TEXT CLEANING] Starting text cleaning process for input length: %d characters", len(rawText))

	cleaned := rawText
	warningPatterns := []string{
		`Warning: Invalid resolution \d+ dpi\. Using \d+ instead\.`,
		`Estimating resolution as \d+`, `Warning:.*`, `Error:.*`,
	}

	for _, pattern := range warningPatterns {
		re := regexp.MustCompile(pattern)
		beforeLen := len(cleaned)
		cleaned = re.ReplaceAllString(cleaned, "")
		if beforeLen != len(cleaned) {
			log.Printf("[OCR TEXT CLEANING] Removed warning pattern '%s', text length reduced from %d to %d", pattern, beforeLen, len(cleaned))
		}
	}

	lines := strings.Split(cleaned, "\n")
	var validLines []string

	for i, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" && len(line) > 0 {
			validLines = append(validLines, line)
			log.Printf("[OCR TEXT CLEANING] Valid line %d preserved: '%s'", i+1, line)
		}
	}

	if len(validLines) == 0 {
		log.Printf("[OCR TEXT CLEANING] No valid lines found after cleaning, returning empty string")
		return ""
	}

	if len(validLines) == 1 {
		log.Printf("[OCR TEXT CLEANING] Single line result: '%s'", validLines[0])
		return validLines[0]
	}

	var processedLines []string
	var currentGroup []string

	for _, line := range validLines {
		if len(line) <= 10 {
			currentGroup = append(currentGroup, line)
			log.Printf("[OCR TEXT CLEANING] Short line added to group: '%s'", line)
		} else {
			if len(currentGroup) > 0 {
				grouped := strings.Join(currentGroup, " ")
				processedLines = append(processedLines, grouped)
				log.Printf("[OCR TEXT CLEANING] Group of %d short lines processed: '%s'", len(currentGroup), grouped)
				currentGroup = []string{}
			}
			processedLines = append(processedLines, line)
			log.Printf("[OCR TEXT CLEANING] Long line processed: '%s'", line)
		}
	}

	if len(currentGroup) > 0 {
		grouped := strings.Join(currentGroup, " ")
		processedLines = append(processedLines, grouped)
		log.Printf("[OCR TEXT CLEANING] Final group processed: '%s'", grouped)
	}

	result := strings.Join(processedLines, " | ")
	log.Printf("[OCR TEXT CLEANING] Text cleaning completed, final result: '%s'", result)
	return result
}

func (ocr *OCRAnalyzer) filterValidTexts(elements []TextElement) []TextElement {
	log.Printf("[OCR TEXT FILTERING] Starting text filtering process for %d elements", len(elements))
	var filtered []TextElement

	for i, elem := range elements {
		text := strings.TrimSpace(elem.Text)

		if len(text) < 1 {
			log.Printf("[OCR TEXT FILTERING] Element %d rejected: text too short (length < 1)", i+1)
			continue
		}

		if len(text) <= 2 && !ocr.isSignificantShortText(text) {
			log.Printf("[OCR TEXT FILTERING] Element %d rejected: short text '%s' not significant", i+1, text)
			continue
		}

		if ocr.isOnlySpecialChars(text) {
			log.Printf("[OCR TEXT FILTERING] Element %d rejected: only special characters '%s'", i+1, text)
			continue
		}

		if ocr.isRepeatingPattern(text) {
			log.Printf("[OCR TEXT FILTERING] Element %d rejected: repeating pattern '%s'", i+1, text)
			continue
		}

		filtered = append(filtered, elem)
		log.Printf("[OCR TEXT FILTERING] Element %d accepted: '%s' at position (%d, %d)", i+1, text, elem.X, elem.Y)
	}

	log.Printf("[OCR TEXT FILTERING] Text filtering completed, %d elements passed filter out of %d initial elements", len(filtered), len(elements))
	return filtered
}

func (ocr *OCRAnalyzer) isSignificantShortText(text string) bool {
	if regexp.MustCompile(`^\d+$`).MatchString(text) {
		return true
	}
	significantShorts := []string{"안", "좋", "나", "다", "를", "을", "의", "에", "로", "과", "와", "OK", "NO", "ON", "UP", "GO", "IN", "TO", "AT", "BY", "@", "#", "$", "%", "&", "*", "+", "-", "=", "?", "!"}
	for _, significant := range significantShorts {
		if text == significant {
			return true
		}
	}
	return false
}

func (ocr *OCRAnalyzer) isOnlySpecialChars(text string) bool {
	hasLetter, hasDigit := false, false
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
	for i := 1; i <= len(text)/3; i++ {
		pattern := text[:i]
		if strings.Repeat(pattern, len(text)/i) == text && len(text) >= i*3 {
			return true
		}
	}
	return false
}

func (ocr *OCRAnalyzer) recognizeFullImage(imagePath string) string {
	log.Printf("[OCR FULL RECOGNITION] Starting full image recognition for: %s", imagePath)
	psmModes := []string{"3", "6"}

	for i, psm := range psmModes {
		log.Printf("[OCR FULL RECOGNITION] Attempting PSM mode %s (attempt %d/%d)", psm, i+1, len(psmModes))
		text := ocr.runTesseract(imagePath, psm)
		if text != "" && len(strings.TrimSpace(text)) > 0 {
			log.Printf("[OCR FULL RECOGNITION] Success with PSM mode %s, extracted text length: %d", psm, len(text))
			return text
		}
		log.Printf("[OCR FULL RECOGNITION] PSM mode %s failed or returned empty result", psm)
	}

	log.Printf("[OCR FULL RECOGNITION] All PSM modes failed for image: %s", imagePath)
	return ""
}

func (ocr *OCRAnalyzer) recognizeTextInRegion(img gocv.Mat, region image.Rectangle) string {
	log.Printf("[OCR REGION RECOGNITION] Processing region (%d,%d)-(%d,%d), size: %dx%d",
		region.Min.X, region.Min.Y, region.Max.X, region.Max.Y, region.Dx(), region.Dy())

	roi := img.Region(region)
	if roi.Empty() {
		log.Printf("[OCR REGION RECOGNITION] Region ROI is empty, skipping recognition")
		return ""
	}
	defer roi.Close()

	processed := ocr.basicPreprocess(roi)
	defer processed.Close()

	tempFile := filepath.Join(os.TempDir(), fmt.Sprintf("ocr_region_%d.png", time.Now().UnixNano()))
	defer os.Remove(tempFile)

	if !gocv.IMWrite(tempFile, processed) {
		log.Printf("[OCR REGION RECOGNITION] Failed to save processed region to temporary file: %s", tempFile)
		return ""
	}

	log.Printf("[OCR REGION RECOGNITION] Saved processed region to temporary file: %s", tempFile)
	result := ocr.runTesseract(tempFile, "8")
	log.Printf("[OCR REGION RECOGNITION] Tesseract result for region: '%s'", result)
	return result
}

func (ocr *OCRAnalyzer) runTesseract(imagePath, psm string) string {
	log.Printf("[OCR TESSERACT] Executing tesseract with image: %s, PSM: %s", imagePath, psm)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, ocr.tesseractPath, imagePath, "stdout", "-l", "kor+eng", "--psm", psm)
	cmd.Env = append(os.Environ(), "TESSDATA_PREFIX=/usr/share/tesseract-ocr/4.00/tessdata")

	startTime := time.Now()
	output, err := cmd.CombinedOutput()
	duration := time.Since(startTime)

	if err != nil {
		log.Printf("[OCR TESSERACT] Tesseract execution failed after %v with error: %v, output: %s", duration, err, string(output))
		return ""
	}

	result := strings.TrimSpace(string(output))
	log.Printf("[OCR TESSERACT] Tesseract execution completed successfully in %v, output length: %d characters", duration, len(result))
	return result
}

func (ocr *OCRAnalyzer) detectTextRegions(img gocv.Mat) []image.Rectangle {
	log.Printf("[OCR REGION DETECTION] Starting text region detection for image size %dx%d", img.Cols(), img.Rows())

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

	log.Printf("[OCR REGION DETECTION] Found %d contours for analysis", contours.Size())

	var regions []image.Rectangle
	for i := 0; i < contours.Size(); i++ {
		contour := contours.At(i)
		area := gocv.ContourArea(contour)

		if area > 100 && area < 50000 {
			rect := gocv.BoundingRect(contour)

			if rect.Dx() > 15 && rect.Dy() > 8 && rect.Dy() < 100 {
				padding := 5
				expandedRect := image.Rect(max(0, rect.Min.X-padding), max(0, rect.Min.Y-padding), min(img.Cols(), rect.Max.X+padding), min(img.Rows(), rect.Max.Y+padding))
				regions = append(regions, expandedRect)
				log.Printf("[OCR REGION DETECTION] Valid region %d: area=%.0f, bounds=(%d,%d)-(%d,%d), expanded=(%d,%d)-(%d,%d)",
					len(regions), area, rect.Min.X, rect.Min.Y, rect.Max.X, rect.Max.Y,
					expandedRect.Min.X, expandedRect.Min.Y, expandedRect.Max.X, expandedRect.Max.Y)
			} else {
				log.Printf("[OCR REGION DETECTION] Rejected contour %d: area=%.0f, size=%dx%d (too small/large)", i+1, area, rect.Dx(), rect.Dy())
			}
		} else {
			log.Printf("[OCR REGION DETECTION] Rejected contour %d: area=%.0f (outside valid range 100-50000)", i+1, area)
		}
	}

	log.Printf("[OCR REGION DETECTION] Region detection completed, found %d valid text regions", len(regions))
	return regions
}

func (ocr *OCRAnalyzer) basicPreprocess(roi gocv.Mat) gocv.Mat {
	log.Printf("[OCR PREPROCESSING] Starting preprocessing for ROI size %dx%d", roi.Cols(), roi.Rows())

	gray := gocv.NewMat()
	defer gray.Close()
	gocv.CvtColor(roi, &gray, gocv.ColorBGRToGray)

	enlarged := gocv.NewMat()
	defer enlarged.Close()
	newSize := image.Pt(roi.Cols()*2, roi.Rows()*2)
	gocv.Resize(gray, &enlarged, newSize, 0, 0, gocv.InterpolationCubic)

	result := gocv.NewMat()
	gocv.AdaptiveThreshold(enlarged, &result, 255, gocv.AdaptiveThresholdGaussian, gocv.ThresholdBinary, 11, 2)

	log.Printf("[OCR PREPROCESSING] Preprocessing completed, original size: %dx%d, final size: %dx%d", roi.Cols(), roi.Rows(), result.Cols(), result.Rows())
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

	for i, elem := range elements {
		key := strings.ToLower(strings.TrimSpace(elem.Text))
		if !seen[key] && key != "" {
			seen[key] = true
			unique = append(unique, elem)
			log.Printf("[OCR DEDUPLICATION] Element %d kept: '%s'", i+1, elem.Text)
		} else {
			log.Printf("[OCR DEDUPLICATION] Element %d removed as duplicate: '%s'", i+1, elem.Text)
		}
	}

	log.Printf("[OCR DEDUPLICATION] Deduplication completed: %d unique elements from %d original elements", len(unique), len(elements))
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
	requestStart := time.Now()
	clientIP := c.ClientIP()
	userAgent := c.GetHeader("User-Agent")
	log.Printf("[HTTP REQUEST] OCR extraction request received from client IP: %s, User-Agent: %s, timestamp: %v", clientIP, userAgent, requestStart)

	file, err := c.FormFile("image")
	if err != nil {
		log.Printf("[HTTP REQUEST ERROR] Failed to retrieve image file from request, client IP: %s, error: %v", clientIP, err)
		c.JSON(http.StatusBadRequest, OCRResponse{Success: false, Message: "Image file required"})
		return
	}

	log.Printf("[HTTP REQUEST] Image file received: filename='%s', size=%d bytes, content-type='%s'", file.Filename, file.Size, file.Header.Get("Content-Type"))

	imagePath := filepath.Join(os.TempDir(), fmt.Sprintf("ocr_%s.png", uuid.New().String()[:8]))
	defer os.Remove(imagePath)

	if err := c.SaveUploadedFile(file, imagePath); err != nil {
		log.Printf("[HTTP REQUEST ERROR] Failed to save uploaded file to temporary path %s, client IP: %s, error: %v", imagePath, clientIP, err)
		c.JSON(http.StatusInternalServerError, OCRResponse{Success: false, Message: "Failed to save image"})
		return
	}

	log.Printf("[HTTP REQUEST] Image successfully saved to temporary file: %s, proceeding with OCR analysis", imagePath)

	texts, err := analyzer.ExtractTexts(imagePath)
	if err != nil {
		log.Printf("[HTTP REQUEST ERROR] OCR analysis failed for image %s, client IP: %s, error: %v", imagePath, clientIP, err)
		c.JSON(http.StatusInternalServerError, OCRResponse{Success: false, Message: "OCR failed"})
		return
	}

	requestDuration := time.Since(requestStart)
	response := OCRResponse{Success: true, TextList: texts, TotalCount: len(texts)}

	log.Printf("[HTTP REQUEST SUCCESS] OCR extraction completed successfully in %v, client IP: %s, extracted %d text elements", requestDuration, clientIP, len(texts))
	for i, text := range texts {
		log.Printf("[HTTP REQUEST SUCCESS] Text element %d: '%s' at position (%d, %d)", i+1, text.Text, text.X, text.Y)
	}

	c.JSON(http.StatusOK, response)
}

func healthHandler(c *gin.Context) {
	clientIP := c.ClientIP()
	log.Printf("[HTTP HEALTH] Health check request received from client IP: %s", clientIP)

	status := map[string]interface{}{"status": "ok", "ocr": analyzer != nil && analyzer.enabled}

	log.Printf("[HTTP HEALTH] Health check response: status=ok, ocr_enabled=%t, client IP: %s", analyzer != nil && analyzer.enabled, clientIP)
	c.JSON(http.StatusOK, status)
}

func main() {
	log.Printf("[APPLICATION START] Starting OCR service application initialization at %v", time.Now())

	var err error
	analyzer, err = NewOCRAnalyzer()
	if err != nil {
		log.Fatalf("[APPLICATION START ERROR] OCR analyzer initialization failed: %v", err)
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

	log.Printf("[APPLICATION START] OCR service ready to accept requests on port %s, analyzer enabled: %t, tesseract path: %s", port, analyzer.enabled, analyzer.tesseractPath)
	log.Printf("[APPLICATION START] Available endpoints: POST /extract (OCR processing), GET /health (service status)")
	log.Printf("[APPLICATION START] CORS enabled for all origins, request timeout: 15 seconds")

	if err := r.Run(":" + port); err != nil {
		log.Fatalf("[APPLICATION START ERROR] Failed to start HTTP server on port %s: %v", port, err)
	}
}
