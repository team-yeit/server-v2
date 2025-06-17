package main

import (
	"bytes"
	"context"
	"encoding/json"
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

type TextExtractRequest struct {
	Text string `json:"text" binding:"required"`
}

type TextExtractResponse struct {
	Result string `json:"result"`
}

type OpenAIRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature float64   `json:"temperature"`
	MaxTokens   int       `json:"max_tokens"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type OpenAIResponse struct {
	Choices []Choice `json:"choices"`
}

type Choice struct {
	Message Message `json:"message"`
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

func callOpenAI(prompt string) (string, error) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		return "", fmt.Errorf("OPENAI_API_KEY environment variable not set")
	}

	requestBody := OpenAIRequest{
		Model:       "gpt-4o-mini",
		Temperature: 0.1,
		MaxTokens:   150,
		Messages: []Message{
			{
				Role:    "system",
				Content: "You are a precise OCR text analysis specialist with expertise in Korean text recognition errors. Follow instructions exactly. Return only the requested information without explanations, formatting, or additional text. Handle OCR recognition errors intelligently.",
			},
			{
				Role:    "user",
				Content: prompt,
			},
		},
	}

	jsonData, err := json.Marshal(requestBody)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", "https://api.openai.com/v1/chat/completions", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var openAIResp OpenAIResponse
	if err := json.NewDecoder(resp.Body).Decode(&openAIResp); err != nil {
		return "", err
	}

	if len(openAIResp.Choices) == 0 {
		return "", fmt.Errorf("no response from OpenAI")
	}

	return strings.TrimSpace(openAIResp.Choices[0].Message.Content), nil
}

func extractStoreNameFromText(text string) (string, error) {
	prompt := fmt.Sprintf(`TASK: Extract the exact store/restaurant name from stuttered speech.

CONTEXT: Users often stutter when saying store names. Your job is to identify the core business name, removing filler words and repetitions.

RULES:
1. Extract ONLY the main store/brand name
2. Remove stutters, filler words (어, 아, 그, 음, 잠깐만, 뭐지, 등)
3. If multiple versions of same name appear, choose the shortest complete form
4. Return Korean store names in Korean, English names in English
5. Do not add quotes, punctuation, or explanations
6. If no clear store name exists, return "NONE"

EXAMPLES:
Input: "아 그 교촌 어 교촌치킨"
Output: 교촌

Input: "어어 아 어 할머니보쌈"
Output: 할머니보쌈

Input: "맥도... 맥도날... 맥도날드"
Output: 맥도날드

Input: "버거킹 어 버거 버거킹 햄버거"
Output: 버거킹

Input: "스타 스타벅스 커피"
Output: 스타벅스

Input: "그냥 배고파"
Output: NONE

INPUT TEXT: "%s"
OUTPUT:`, text)

	return callOpenAI(prompt)
}

func extractNumberFromText(text string) (string, error) {
	prompt := fmt.Sprintf(`TASK: Extract the specific number mentioned in stuttered speech.

CONTEXT: Users stutter when trying to say numbers. Extract the exact number they're attempting to communicate.

RULES:
1. Extract ONLY the number (digits)
2. Remove all filler words (아, 그, 어, 잠깐만, 번, 호, 등)
3. If same number repeated multiple times, return it once
4. Return only Arabic numerals (1, 2, 3, not 일, 이, 삼)
5. No decimal points unless clearly specified
6. If no number found, return "NONE"

EXAMPLES:
Input: "아 그 잠깐만 4번 어 4번"
Output: 4

Input: "5호 어 5 5호점"
Output: 5

Input: "이십 어 20 스무개"
Output: 20

Input: "한 하나 1개"
Output: 1

Input: "그냥 많이"
Output: NONE

INPUT TEXT: "%s"
OUTPUT:`, text)

	return callOpenAI(prompt)
}

func extractFoodNameFromText(text string) (string, error) {
	prompt := fmt.Sprintf(`TASK: Extract the exact food/menu item name from stuttered speech.

CONTEXT: Users stutter when ordering food. Extract the specific food/menu item they want to order.

RULES:
1. Extract ONLY the main food/menu item name
2. Remove stutters, filler words (어, 아, 그, 음, 잠깐만)
3. Keep food-specific terms (치킨, 피자, 버거, 라면, etc.)
4. If multiple versions of same food appear, choose the most complete form
5. Return Korean food names in Korean, English names in English
6. Do not include quantities, sizes, or modifiers unless part of the official name
7. If no clear food name exists, return "NONE"

EXAMPLES:
Input: "어 그 뿌링클 어 치킨"
Output: 뿌링클

Input: "불고기 어 불고기버거"
Output: 불고기버거

Input: "아 짜장 짜장면"
Output: 짜장면

Input: "핫윙 어 핫 핫윙스"
Output: 핫윙

Input: "그냥 배고파"
Output: NONE

INPUT TEXT: "%s"
OUTPUT:`, text)

	return callOpenAI(prompt)
}

func filterStoreNames(textList []TextElement) ([]TextElement, error) {
	if len(textList) == 0 {
		return []TextElement{}, nil
	}

	var allTexts []string
	for _, item := range textList {
		allTexts = append(allTexts, fmt.Sprintf("\"%s\"", item.Text))
	}

	prompt := fmt.Sprintf(`TASK: Identify store/restaurant names from OCR text results with error correction.

CONTEXT: This is text extracted from images (signs, menus, etc.) using OCR technology. OCR often makes recognition errors, especially with Korean text.

TEXT LIST: [%s]

RULES:
1. Identify text that represents store/restaurant/business names
2. Handle OCR recognition errors intelligently and provide corrected names
3. Exclude: prices, menu descriptions, addresses, phone numbers, hours, promotional text
4. Include: brand names, restaurant names, store names, franchise names
5. Return results as comma-separated values with corrected spelling
6. Keep original meaning but fix OCR errors
7. If no store names found, return "NONE"

OCR ERROR CORRECTION EXAMPLES:
- "맥도냘드" → "맥도날드"
- "스따벅스" → "스타벅스"
- "버거킹" → "버거킹"
- "교촌지킨" → "교촌치킨"
- "BBQ" → "BBQ"
- "롯떼리아" → "롯데리아"

ANALYSIS EXAMPLES:
Input: ["맥도날드", "빅맥세트", "5,500원", "영업시간", "02-123-4567"]
Output: 맥도날드

Input: ["스따벅스", "아메리카노", "4,500원", "카페라떼", "매장안내"]  
Output: 스타벅스

Input: ["BBQ", "황금올리브치킨", "반반치킨", "17,000원", "배달가능"]
Output: BBQ

OUTPUT:`, strings.Join(allTexts, ", "))

	result, err := callOpenAI(prompt)
	if err != nil {
		return nil, err
	}

	return filterTextItemsAdvanced(textList, result), nil
}

func filterFoodNames(textList []TextElement) ([]TextElement, error) {
	if len(textList) == 0 {
		return []TextElement{}, nil
	}

	var allTexts []string
	for _, item := range textList {
		allTexts = append(allTexts, fmt.Sprintf("\"%s\"", item.Text))
	}

	prompt := fmt.Sprintf(`TASK: Identify food/menu item names from OCR text results with error correction.

CONTEXT: This is text extracted from menu images using OCR technology. OCR often makes recognition errors, especially with Korean food names.

TEXT LIST: [%s]

RULES:
1. Identify text that represents food items, dishes, beverages, menu items
2. Handle OCR recognition errors intelligently and provide corrected names
3. Exclude: store names, prices, promotional text, descriptions, categories
4. Include: specific food names, drink names, dish names, menu items
5. Return results as comma-separated values with corrected spelling
6. Keep original meaning but fix OCR errors
7. If no food names found, return "NONE"

OCR ERROR CORRECTION EXAMPLES:
- "비맥세트" → "빅맥세트"
- "지즈버거" → "치즈버거"
- "아메리가노" → "아메리카노"
- "불고기버거" → "불고기버거"
- "뿌링끌" → "뿌링클"
- "콜라" → "콜라"
- "화이트모까" → "화이트모카"

ANALYSIS EXAMPLES:
Input: ["맥도날드", "비맥세트", "5,500원", "지즈버거", "콜라"]
Output: 빅맥세트, 치즈버거, 콜라

Input: ["스타벅스", "아메리가노", "4,500원", "까페라떼", "매장안내"]
Output: 아메리카노, 카페라떼

Input: ["BBQ", "황금올리브치킨", "뿌링끌", "17,000원", "배달가능"]
Output: 황금올리브치킨, 뿌링클

OUTPUT:`, strings.Join(allTexts, ", "))

	result, err := callOpenAI(prompt)
	if err != nil {
		return nil, err
	}

	return filterTextItemsAdvanced(textList, result), nil
}

func filterTextItems(originalItems []TextElement, filteredTexts string) []TextElement {
	if filteredTexts == "NONE" || strings.TrimSpace(filteredTexts) == "" {
		return []TextElement{}
	}

	var result []TextElement
	filteredList := strings.Split(filteredTexts, ",")

	for _, filteredText := range filteredList {
		cleanText := strings.TrimSpace(filteredText)
		cleanText = strings.Trim(cleanText, "\"'")

		for _, item := range originalItems {
			if strings.Contains(strings.ToLower(item.Text), strings.ToLower(cleanText)) ||
				strings.Contains(strings.ToLower(cleanText), strings.ToLower(item.Text)) ||
				strings.EqualFold(item.Text, cleanText) {
				result = append(result, item)
				break
			}
		}
	}

	return result
}

func filterTextItemsImproved(originalItems []TextElement, filteredTexts string) []TextElement {
	if filteredTexts == "NONE" || strings.TrimSpace(filteredTexts) == "" {
		return []TextElement{}
	}

	var result []TextElement
	seen := make(map[string]bool)

	filteredList := strings.Split(filteredTexts, ",")

	for _, filteredText := range filteredList {
		cleanText := strings.TrimSpace(filteredText)
		cleanText = strings.Trim(cleanText, "\"'")

		if cleanText == "" || seen[cleanText] {
			continue
		}

		for _, item := range originalItems {
			itemText := strings.TrimSpace(item.Text)

			if strings.EqualFold(itemText, cleanText) {
				result = append(result, TextElement{
					Text: cleanText,
					X:    item.X,
					Y:    item.Y,
				})
				seen[cleanText] = true
				break
			}

			if strings.Contains(strings.ToLower(itemText), strings.ToLower(cleanText)) {
				if isWordBoundaryMatch(cleanText, itemText) {
					result = append(result, TextElement{
						Text: cleanText,
						X:    item.X,
						Y:    item.Y,
					})
					seen[cleanText] = true
					break
				}
			}
		}
	}

	return result
}

func filterTextItemsAdvanced(originalItems []TextElement, filteredTexts string) []TextElement {
	if filteredTexts == "NONE" || strings.TrimSpace(filteredTexts) == "" {
		return []TextElement{}
	}

	var result []TextElement
	seen := make(map[string]bool)

	filteredList := strings.Split(filteredTexts, ",")

	for _, filteredText := range filteredList {
		cleanText := strings.TrimSpace(filteredText)
		cleanText = strings.Trim(cleanText, "\"'")

		if cleanText == "" || seen[cleanText] {
			continue
		}

		bestMatch := findBestMatchAdvanced(cleanText, originalItems)
		if bestMatch != nil {
			result = append(result, TextElement{
				Text: cleanText,
				X:    bestMatch.X,
				Y:    bestMatch.Y,
			})
			seen[cleanText] = true
		}
	}

	return result
}

func findBestMatchAdvanced(targetText string, items []TextElement) *TextElement {
	var bestMatch *TextElement
	maxScore := 0.0

	targetLower := strings.ToLower(targetText)

	for _, item := range items {
		itemLower := strings.ToLower(item.Text)
		score := calculateMatchScore(targetLower, itemLower)

		if score > maxScore && score > 0.3 {
			maxScore = score
			bestMatch = &item
		}
	}

	return bestMatch
}

func calculateMatchScore(target, source string) float64 {
	if target == source {
		return 1.0
	}

	if strings.Contains(source, target) || strings.Contains(target, source) {
		return 0.9
	}

	if isWordBoundaryMatch(target, source) {
		return 0.85
	}

	similarity := calculateTextSimilarity(target, source)

	lengthRatio := float64(min(len(target), len(source))) / float64(max(len(target), len(source)))

	return similarity * lengthRatio
}

func isWordBoundaryMatch(target, source string) bool {
	sourceWords := strings.FieldsFunc(source, func(c rune) bool {
		return c == ' ' || c == '|' || c == '-' || c == ',' || c == '.'
	})

	for _, word := range sourceWords {
		word = strings.TrimSpace(word)
		if strings.EqualFold(word, target) {
			return true
		}
		if calculateTextSimilarity(strings.ToLower(word), strings.ToLower(target)) > 0.8 {
			return true
		}
	}

	return false
}

func calculateTextSimilarity(str1, str2 string) float64 {
	if str1 == str2 {
		return 1.0
	}

	if len(str1) == 0 || len(str2) == 0 {
		return 0.0
	}

	distance := levenshteinDistance(str1, str2)
	maxLen := max(len(str1), len(str2))

	return 1.0 - (float64(distance) / float64(maxLen))
}

func levenshteinDistance(str1, str2 string) int {
	m, n := len(str1), len(str2)

	dp := make([][]int, m+1)
	for i := range dp {
		dp[i] = make([]int, n+1)
	}

	for i := 0; i <= m; i++ {
		dp[i][0] = i
	}
	for j := 0; j <= n; j++ {
		dp[0][j] = j
	}

	for i := 1; i <= m; i++ {
		for j := 1; j <= n; j++ {
			cost := 0
			if str1[i-1] != str2[j-1] {
				cost = 1
			}

			dp[i][j] = min3(
				dp[i-1][j]+1,
				dp[i][j-1]+1,
				dp[i-1][j-1]+cost,
			)
		}
	}

	return dp[m][n]
}

func min3(a, b, c int) int {
	if a < b && a < c {
		return a
	}
	if b < c {
		return b
	}
	return c
}

var analyzer *OCRAnalyzer

func imageExtractHandler(c *gin.Context) {
	requestStart := time.Now()
	clientIP := c.ClientIP()
	userAgent := c.GetHeader("User-Agent")
	log.Printf("[HTTP REQUEST] OCR extraction request received from client IP: %s, User-Agent: %s, timestamp: %v", clientIP, userAgent, requestStart)

	filterType := c.Query("type")
	log.Printf("[HTTP REQUEST] Filter type: '%s'", filterType)

	if filterType != "" && filterType != "store" && filterType != "food" {
		log.Printf("[HTTP REQUEST ERROR] Invalid filter type: %s", filterType)
		c.JSON(http.StatusBadRequest, OCRResponse{
			Success: false,
			Message: "type parameter must be 'store' or 'food'",
		})
		return
	}

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

	var finalTexts []TextElement
	if filterType == "" {
		finalTexts = texts
		log.Printf("[HTTP REQUEST] No filtering applied, returning %d text elements", len(finalTexts))
	} else {
		switch filterType {
		case "store":
			finalTexts, err = filterStoreNames(texts)
			if err != nil {
				log.Printf("[HTTP REQUEST ERROR] Store name filtering failed: %v", err)
				c.JSON(http.StatusInternalServerError, OCRResponse{Success: false, Message: "Store name filtering failed"})
				return
			}
			log.Printf("[HTTP REQUEST] Store name filtering applied, %d elements filtered from %d", len(finalTexts), len(texts))
		case "food":
			finalTexts, err = filterFoodNames(texts)
			if err != nil {
				log.Printf("[HTTP REQUEST ERROR] Food name filtering failed: %v", err)
				c.JSON(http.StatusInternalServerError, OCRResponse{Success: false, Message: "Food name filtering failed"})
				return
			}
			log.Printf("[HTTP REQUEST] Food name filtering applied, %d elements filtered from %d", len(finalTexts), len(texts))
		}
	}

	requestDuration := time.Since(requestStart)
	response := OCRResponse{Success: true, TextList: finalTexts, TotalCount: len(finalTexts)}

	log.Printf("[HTTP REQUEST SUCCESS] OCR extraction completed successfully in %v, client IP: %s, extracted %d text elements", requestDuration, clientIP, len(finalTexts))
	for i, text := range finalTexts {
		log.Printf("[HTTP REQUEST SUCCESS] Text element %d: '%s' at position (%d, %d)", i+1, text.Text, text.X, text.Y)
	}

	c.JSON(http.StatusOK, response)
}

func textExtractHandler(c *gin.Context) {
	requestStart := time.Now()
	clientIP := c.ClientIP()
	log.Printf("[HTTP TEXT REQUEST] Text extraction request received from client IP: %s", clientIP)

	extractType := c.Query("type")
	if extractType == "" {
		log.Printf("[HTTP TEXT REQUEST ERROR] Missing type parameter")
		c.JSON(http.StatusBadRequest, gin.H{"error": "type query parameter is required"})
		return
	}

	if extractType != "store" && extractType != "number" && extractType != "food" {
		log.Printf("[HTTP TEXT REQUEST ERROR] Invalid type: %s", extractType)
		c.JSON(http.StatusBadRequest, gin.H{"error": "type must be 'store', 'number', or 'food'"})
		return
	}

	var req TextExtractRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		log.Printf("[HTTP TEXT REQUEST ERROR] JSON binding failed: %v", err)
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	log.Printf("[HTTP TEXT REQUEST] Processing text: '%s', type: %s", req.Text, extractType)

	var result string
	var err error

	switch extractType {
	case "store":
		result, err = extractStoreNameFromText(req.Text)
	case "number":
		result, err = extractNumberFromText(req.Text)
	case "food":
		result, err = extractFoodNameFromText(req.Text)
	}

	if err != nil {
		log.Printf("[HTTP TEXT REQUEST ERROR] Text extraction failed: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	requestDuration := time.Since(requestStart)
	response := TextExtractResponse{Result: result}

	log.Printf("[HTTP TEXT REQUEST SUCCESS] Text extraction completed in %v, client IP: %s, result: '%s'", requestDuration, clientIP, result)
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

	r.POST("/image/extract", imageExtractHandler)
	r.POST("/text/extract", textExtractHandler)
	r.GET("/health", healthHandler)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8000"
	}

	log.Printf("[APPLICATION START] OCR service ready to accept requests on port %s, analyzer enabled: %t, tesseract path: %s", port, analyzer.enabled, analyzer.tesseractPath)
	log.Printf("[APPLICATION START] Available endpoints:")
	log.Printf("[APPLICATION START] - POST /image/extract (OCR processing, optional ?type=store or ?type=food)")
	log.Printf("[APPLICATION START] - POST /text/extract?type=store|number|food (Text processing)")
	log.Printf("[APPLICATION START] - GET /health (service status)")
	log.Printf("[APPLICATION START] CORS enabled for all origins, request timeout: 15 seconds")

	if err := r.Run(":" + port); err != nil {
		log.Fatalf("[APPLICATION START ERROR] Failed to start HTTP server on port %s: %v", port, err)
	}
}
