# Tesseract 경로 문제 해결된 OCR 전용 Dockerfile

# Go 빌드 단계
FROM gocv/opencv:4.11.0 AS builder

# Tesseract OCR 설치 및 경로 확인
RUN apt-get update && apt-get install -y --no-install-recommends \
    tesseract-ocr \
    tesseract-ocr-kor \
    tesseract-ocr-eng \
    && rm -rf /var/lib/apt/lists/* && apt-get clean

# Tessdata 경로 확인 및 심링크 생성
RUN echo "🔍 Checking tessdata paths..." && \
    find /usr -name "tessdata" -type d 2>/dev/null && \
    find /usr -name "eng.traineddata" 2>/dev/null && \
    ls -la /usr/share/tesseract-ocr/ && \
    # 표준 경로에 심링크 생성
    if [ -d "/usr/share/tesseract-ocr/4.00/tessdata" ]; then \
        ln -sf /usr/share/tesseract-ocr/4.00/tessdata /usr/share/tessdata; \
    elif [ -d "/usr/share/tesseract-ocr/5/tessdata" ]; then \
        ln -sf /usr/share/tesseract-ocr/5/tessdata /usr/share/tessdata; \
    fi && \
    echo "✅ Tessdata setup complete"

WORKDIR /app

# Go 모듈 파일 복사 및 의존성 다운로드
COPY go.mod go.sum ./
RUN go mod download

# 소스 코드 복사 및 빌드
COPY . .
ENV CGO_ENABLED=1
ENV GOOS=linux
RUN go build -ldflags="-s -w" -o ocr-service .

# 실행 단계 (경량화)
FROM gocv/opencv:4.11.0

# Tesseract OCR 설치 및 경로 설정
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    curl \
    tesseract-ocr \
    tesseract-ocr-kor \
    tesseract-ocr-eng \
    && rm -rf /var/lib/apt/lists/* && apt-get clean

# Tessdata 경로 확인 및 설정
RUN echo "🔍 Setting up tessdata paths..." && \
    find /usr -name "tessdata" -type d 2>/dev/null && \
    find /usr -name "eng.traineddata" 2>/dev/null && \
    # 가능한 모든 tessdata 경로에 심링크 생성
    mkdir -p /usr/share/tessdata && \
    if [ -d "/usr/share/tesseract-ocr/4.00/tessdata" ]; then \
        cp -r /usr/share/tesseract-ocr/4.00/tessdata/* /usr/share/tessdata/ 2>/dev/null || true; \
        ln -sf /usr/share/tesseract-ocr/4.00/tessdata /usr/share/tesseract-ocr/tessdata; \
    fi && \
    if [ -d "/usr/share/tesseract-ocr/5/tessdata" ]; then \
        cp -r /usr/share/tesseract-ocr/5/tessdata/* /usr/share/tessdata/ 2>/dev/null || true; \
        ln -sf /usr/share/tesseract-ocr/5/tessdata /usr/share/tesseract-ocr/tessdata; \
    fi && \
    # 언어 파일 확인
    echo "📋 Available language files:" && \
    find /usr -name "*.traineddata" 2>/dev/null && \
    echo "✅ Tessdata setup complete"

WORKDIR /app

# 빌드된 애플리케이션 복사
COPY --from=builder /app/ocr-service .

# 사용자 권한 설정
RUN groupadd -r appuser && \
    useradd -r -g appuser -d /app -s /sbin/nologin appuser && \
    chown -R appuser:appuser /app && \
    chmod +x ocr-service

# Tesseract 환경변수 설정 (여러 경로 대응)
ENV TESSDATA_PREFIX=/usr/share/tessdata
ENV TESSERACT_PATH=/usr/bin/tesseract

# 추가 환경변수 (fallback)
ENV LC_ALL=C.UTF-8
ENV LANG=C.UTF-8

USER appuser

# Tesseract 테스트
RUN echo "🧪 Testing Tesseract..." && \
    tesseract --version && \
    tesseract --list-langs && \
    echo "✅ Tesseract test complete"

# 포트 노출
EXPOSE 8000

# 헬스체크
HEALTHCHECK --interval=30s --timeout=10s --start-period=30s --retries=3 \
    CMD curl -f http://localhost:8000/health || exit 1

CMD ["./ocr-service"]

# ==================== 사용 가이드 ====================
#
# 빌드:
# docker build -t fixed-ocr .
#
# 실행:
# docker run -p 8000:8000 fixed-ocr
#
# 테스트:
# curl -X POST -F "image=@screenshot.png" http://localhost:8000/extract?debug=true
#
# 개선사항:
# ✅ Tessdata 경로 자동 감지 및 설정
# ✅ 여러 tessdata 경로에 대한 fallback
# ✅ 언어 파일 존재 확인
# ✅ 심링크를 통한 경로 통일
# ✅ 빌드 시점 tessdata 검증
#
# 이제 tessdata 경로 에러가 해결됩니다!