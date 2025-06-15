# OCR 서비스 API 명세서

## 개요

이미지에서 텍스트를 추출하는 OCR(Optical Character Recognition) 서비스입니다.
Tesseract OCR 엔진과 OpenCV를 사용하여 한국어와 영어 텍스트를 인식합니다.

## 기본 정보

- **Base URL**: `http://localhost:8000`
- **Content-Type**: `multipart/form-data` (이미지 업로드시)
- **Response Type**: `application/json`

## API 엔드포인트

### 1. 텍스트 추출 (OCR)

이미지 파일에서 텍스트를 추출합니다.

#### `POST /extract`

**Request**

- **Method**: POST
- **Content-Type**: multipart/form-data
- **Parameters**:
  - `image` (file, required): 분석할 이미지 파일

**Response**

```json
{
  "success": true,
  "text_list": [
    {
      "text": "추출된 텍스트",
      "x": 100,
      "y": 200
    }
  ],
  "total_count": 1,
  "message": "optional error message"
}
```

**Response Fields**

- `success` (boolean): 요청 성공 여부
- `text_list` (array): 추출된 텍스트 요소들의 배열
  - `text` (string): 추출된 텍스트 내용
  - `x` (int): 텍스트의 X 좌표 (중심점)
  - `y` (int): 텍스트의 Y 좌표 (중심점)
- `total_count` (int): 추출된 텍스트 요소의 총 개수
- `message` (string, optional): 오류 메시지 (실패시에만 포함)

**예시 요청**

```bash
curl -X POST \
  http://localhost:8000/extract \
  -F "image=@/path/to/your/image.png"
```

### 2. 서비스 상태 확인

서비스의 동작 상태를 확인합니다.

#### `GET /health`

**Request**

- **Method**: GET

**Response**

```json
{
  "status": "ok",
  "ocr": true
}
```

**Response Fields**

- `status` (string): 서비스 상태 ("ok")
- `ocr` (boolean): OCR 엔진 활성화 상태

**예시 요청**

```bash
curl http://localhost:8000/health
```

## 에러 응답

모든 에러는 다음 형식으로 반환됩니다:

```json
{
  "success": false,
  "text_list": [],
  "total_count": 0,
  "message": "에러 메시지"
}
```

### HTTP 상태 코드

- `200 OK`: 성공
- `400 Bad Request`: 잘못된 요청 (이미지 파일 누락 등)
- `500 Internal Server Error`: 서버 오류 (OCR 처리 실패 등)

## 지원하는 이미지 형식

- PNG
- JPEG/JPG
- 기타 OpenCV에서 지원하는 이미지 형식

## 지원 언어

- 한국어 (kor)
- 영어 (eng)

## 특징

- 자동 텍스트 영역 검출
- 중복 텍스트 제거
- 텍스트 품질 필터링
- 좌표 정보 제공
- 상세한 로깅

## 설치 및 실행

### 요구사항

- Go 1.19+
- Tesseract OCR
- OpenCV

### 실행

```bash
# 개발 환경
go run main.go

# 빌드 후 실행
go build -o ocr-server main.go
./ocr-server
```

기본 포트는 8000번이며, `PORT` 환경변수로 변경 가능합니다.

### Docker 실행

```bash
docker build -t ocr-server .
docker run -p 8000:8000 ocr-server
```
