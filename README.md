# OCR 서비스 API 명세서

## 개요

이미지에서 텍스트를 추출하고 AI 기반 필터링/정제 기능을 제공하는 OCR 서비스입니다. Tesseract OCR과 OpenAI GPT를 사용하여 한국어와 영어 텍스트를 인식하고 처리합니다.

## 기본 정보

- **Base URL**: `http://localhost:8000`
- **Content-Type**: `multipart/form-data` (이미지 업로드시), `application/json` (텍스트 처리시)
- **Response Type**: `application/json`

## 환경 변수

- `OPENAI_API_KEY`: OpenAI API 키 (필수)
- `PORT`: 서버 포트 (기본값: 8000)

---

## API 엔드포인트

### 1. 이미지 텍스트 추출 (OCR)

이미지 파일에서 텍스트를 추출하고 선택적으로 필터링합니다.

**Endpoint**: `POST /image/extract`

#### Request

- **Method**: POST
- **Content-Type**: multipart/form-data
- **Parameters**:
  - `image` (file, required): 분석할 이미지 파일
  - `type` (query, optional): 필터링 타입
    - 없음: 모든 텍스트 추출 (기본 동작)
    - `store`: 가게이름만 필터링
    - `food`: 음식이름만 필터링

#### Response

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

#### Examples

**기본 OCR (필터링 없음)**

```bash
curl -X POST \
  http://localhost:8000/image/extract \
  -F "image=@menu.jpg"
```

**가게이름만 필터링**

```bash
curl -X POST \
  http://localhost:8000/image/extract?type=store \
  -F "image=@storefront.jpg"
```

**음식이름만 필터링**

```bash
curl -X POST \
  http://localhost:8000/image/extract?type=food \
  -F "image=@menu.jpg"
```

#### Response Examples

**가게이름 필터링 결과**

```json
{
  "success": true,
  "text_list": [
    {
      "text": "맥도날드",
      "x": 150,
      "y": 80
    }
  ],
  "total_count": 1
}
```

**음식이름 필터링 결과**

```json
{
  "success": true,
  "text_list": [
    {
      "text": "빅맥세트",
      "x": 200,
      "y": 150
    },
    {
      "text": "치즈버거",
      "x": 200,
      "y": 200
    }
  ],
  "total_count": 2
}
```

---

### 2. 텍스트 정제 및 추출

더듬거리는 텍스트에서 원하는 정보를 추출합니다.

**Endpoint**: `POST /text/extract`

#### Request

- **Method**: POST
- **Content-Type**: application/json
- **Query Parameters**:
  - `type` (required): 추출 타입
    - `store`: 가게이름 추출
    - `number`: 숫자 추출
    - `food`: 음식이름 추출
- **Body**:

```json
{
  "text": "더듬거리는 텍스트"
}
```

#### Response

```json
{
  "result": "추출된 결과"
}
```

#### Examples

**가게이름 추출**

```bash
curl -X POST \
  http://localhost:8000/text/extract?type=store \
  -H "Content-Type: application/json" \
  -d '{"text": "아 그 교촌 어 교촌치킨"}'
```

Response:

```json
{
  "result": "교촌"
}
```

**숫자 추출**

```bash
curl -X POST \
  http://localhost:8000/text/extract?type=number \
  -H "Content-Type: application/json" \
  -d '{"text": "아 그 잠깐만 4번 어 4번"}'
```

Response:

```json
{
  "result": "4"
}
```

**음식이름 추출**

```bash
curl -X POST \
  http://localhost:8000/text/extract?type=food \
  -H "Content-Type: application/json" \
  -d '{"text": "어 그 뿌링클 어 치킨"}'
```

Response:

```json
{
  "result": "뿌링클"
}
```

---

### 3. 서비스 상태 확인

서비스의 동작 상태를 확인합니다.

**Endpoint**: `GET /health`

#### Request

- **Method**: GET

#### Response

```json
{
  "status": "ok",
  "ocr": true
}
```

#### Example

```bash
curl http://localhost:8000/health
```

---

## 에러 응답

모든 에러는 다음 형식으로 반환됩니다:

### 이미지 처리 에러

```json
{
  "success": false,
  "text_list": [],
  "total_count": 0,
  "message": "에러 메시지"
}
```

### 텍스트 처리 에러

```json
{
  "error": "에러 메시지"
}
```

## HTTP 상태 코드

- `200 OK`: 성공
- `400 Bad Request`: 잘못된 요청 (파일 누락, 잘못된 타입 등)
- `500 Internal Server Error`: 서버 오류 (OCR 처리 실패, OpenAI API 오류 등)

---

## 지원하는 이미지 형식

- PNG
- JPEG/JPG
- 기타 OpenCV에서 지원하는 이미지 형식

## 지원 언어

- 한국어 (kor)
- 영어 (eng)

---

## 사용 예시 시나리오

### 시나리오 1: 간판에서 가게이름 추출

1. 간판 사진을 `POST /image/extract?type=store`로 전송
2. 가게이름만 필터링된 결과 수신

### 시나리오 2: 메뉴판에서 음식이름 추출

1. 메뉴판 사진을 `POST /image/extract?type=food`로 전송
2. 음식이름만 필터링된 결과 수신

### 시나리오 3: 사용자 음성 인식 후 정제

1. 음성을 텍스트로 변환 (외부 STT)
2. 더듬거리는 텍스트를 `POST /text/extract?type=store`로 전송
3. 정제된 가게이름 수신

### 시나리오 4: 주문 번호 추출

1. 사용자 발화: "아 그 잠깐만 4번 어 4번"
2. `POST /text/extract?type=number`로 전송
3. "4" 수신

---

## 설치 및 실행

### 요구사항

- Go 1.19+
- Tesseract OCR
- OpenCV
- OpenAI API 키

### 환경변수 설정

```bash
export OPENAI_API_KEY="your_openai_api_key"
export PORT=8000
```

### 로컬 실행

```bash
go run main.go
```

### Docker 실행

```bash
docker build -t ocr-server .
docker run -p 8000:8000 -e OPENAI_API_KEY="your_api_key" ocr-server
```

---

## 특징

- 자동 텍스트 영역 검출
- 중복 텍스트 제거
- 텍스트 품질 필터링
- 좌표 정보 제공
- AI 기반 스마트 필터링
- 더듬거리는 텍스트 정제
- 상세한 로깅
