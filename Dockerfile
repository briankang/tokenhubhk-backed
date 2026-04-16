# Stage 1: Build
FROM golang:1.23-alpine AS builder

RUN apk update && apk add --no-cache git ca-certificates tzdata

# Go module proxy (多代理 fallback)
ENV GOPROXY=https://goproxy.cn,https://goproxy.io,https://proxy.golang.org,direct
ENV GONOSUMCHECK=*
ENV GONOSUMDB=*
ENV GOFLAGS=-insecure

WORKDIR /build

# Copy go.mod and go.sum first for better layer caching
COPY go.mod go.sum* ./

# Download dependencies with retry (network may be unstable)
RUN for i in 1 2 3 4 5; do \
      echo "=== Attempt $i ===" && \
      go mod download && break || sleep 3; \
    done

# Copy all source code
COPY . .

# Ensure dependencies are in sync (with retry for unstable network)
RUN for i in 1 2 3 4 5; do \
      echo "=== go mod tidy attempt $i ===" && \
      go mod tidy && break || sleep 5; \
    done

# Build binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o server ./cmd/server/

# Stage 2: Run (Debian slim 对 Chromium 兼容性更好)
FROM debian:bookworm-slim

RUN apt-get update && \
    (apt-get install -y --no-install-recommends \
    ca-certificates tzdata chromium fonts-wqy-zenhei wget || \
    apt-get install -y --fix-missing --no-install-recommends \
    ca-certificates tzdata chromium fonts-wqy-zenhei wget) && \
    rm -rf /var/lib/apt/lists/*

# 设置 Chromium 路径，供 go-rod 使用
ENV ROD_BROWSER_BIN=/usr/bin/chromium

WORKDIR /app

# Copy binary from builder
COPY --from=builder /build/server .

# Copy configs and locale files
COPY --from=builder /build/configs ./configs
COPY --from=builder /build/internal/pkg/i18n/locales ./internal/pkg/i18n/locales

# Create logs directory
RUN mkdir -p /app/logs

EXPOSE 8080

CMD ["./server"]
