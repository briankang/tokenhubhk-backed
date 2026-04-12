# Stage 1: Build
FROM docker.1ms.run/library/golang:1.23-alpine AS builder

RUN apk update && apk add --no-cache git ca-certificates tzdata

# Use China Go proxy for faster downloads
ENV GOPROXY=https://goproxy.cn,direct

WORKDIR /build

# Copy all source code
COPY . .

# Download dependencies (generate go.sum if missing)
RUN go mod tidy && go mod download

# Build binary
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o server ./cmd/server/

# Stage 2: Run
FROM docker.1ms.run/library/alpine:3.19

RUN apk update && apk add --no-cache ca-certificates tzdata

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
