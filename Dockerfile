# 간소화된 최종 OCR Dockerfile

FROM gocv/opencv:4.11.0 AS builder

# Tesseract OCR 설치
RUN apt-get update && apt-get install -y --no-install-recommends \
    tesseract-ocr \
    tesseract-ocr-kor \
    tesseract-ocr-eng \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Go 모듈 복사 및 의존성 설치
COPY go.mod go.sum ./
RUN go mod download

# 소스 코드 복사 및 빌드
COPY . .
ENV CGO_ENABLED=1
ENV GOOS=linux
RUN go build -ldflags="-s -w" -o ocr-service .

# 실행 단계
FROM gocv/opencv:4.11.0

# 필수 패키지만 설치
RUN apt-get update && apt-get install -y --no-install-recommends \
    tesseract-ocr \
    tesseract-ocr-kor \
    tesseract-ocr-eng \
    curl \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# 빌드된 애플리케이션 복사
COPY --from=builder /app/ocr-service .

# 사용자 설정
RUN groupadd -r appuser && \
    useradd -r -g appuser -d /app -s /sbin/nologin appuser && \
    chown -R appuser:appuser /app && \
    chmod +x ocr-service

# Tesseract 환경변수 (알려진 경로 직접 설정)
ENV TESSDATA_PREFIX=/usr/share/tessdata

USER appuser

EXPOSE 8000

HEALTHCHECK --interval=30s --timeout=10s --start-period=30s --retries=3 \
    CMD curl -f http://localhost:8000/health || exit 1

CMD ["./ocr-service"]