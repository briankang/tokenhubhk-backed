# syntax=docker/dockerfile:1
# ================================================================
# TokenHub HK — 多阶段构建
#
# 三个最终镜像目标：
#   lean     — Alpine，不含 Chromium（Gateway / Backend 使用）
#              压缩后约 40MB，比原来节省 ~460MB
#   worker   — Debian + Chromium，供 go-rod 价格爬虫使用
#              压缩后约 450MB（无法避免，Chromium 是必要依赖）
#   monolith — Debian + Chromium + 两个二进制（本地 docker-compose 使用）
#              等价于原来的单镜像，CMD=./server，向后兼容
#
# 构建命令：
#   docker build --target lean    -t server-lean:latest .    # Gateway/Backend
#   docker build --target worker  -t server-worker:latest .  # Worker
#   docker build                  -t server:latest .          # 本地单体（默认）
# ================================================================

# ----------------------------------------------------------------
# Stage 1: builder — 编译 Go 二进制
# 使用 BuildKit 缓存挂载，避免重复下载模块和重复编译
# ----------------------------------------------------------------
FROM golang:1.23-alpine AS builder

RUN apk add --no-cache git ca-certificates tzdata

# Go 代理（中国大陆优先；多源 fallback）
ENV GOPROXY=https://goproxy.cn,https://goproxy.io,https://proxy.golang.org,direct
ENV GONOSUMCHECK=*
ENV GONOSUMDB=*

WORKDIR /build

# 先复制依赖描述文件，利用 Docker 层缓存
# 只要 go.mod/go.sum 不变，就不重新下载模块
COPY go.mod go.sum ./

# 使用 BuildKit cache mount 缓存模块（不写入镜像层）
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod download

# 复制全部源码
COPY . .

# 一次性编译两个二进制（builder cache mount 复用上面的 go/pkg/mod）
# -s -w 去掉 debug 符号，减小二进制约 30%
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w" -o tokenhub ./cmd/tokenhub/ && \
    go build -ldflags="-s -w" -o server ./cmd/server/

# ----------------------------------------------------------------
# Stage 2: lean — Gateway / Backend 精简镜像
# Alpine 3.21 基础，不含 Chromium，压缩后约 40MB
# ----------------------------------------------------------------
FROM alpine:3.21 AS lean

# ca-certificates：Go HTTPS 请求必须；tzdata：时区支持
RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

COPY --from=builder /build/tokenhub .
COPY --from=builder /build/configs ./configs
COPY --from=builder /build/internal/pkg/i18n/locales ./internal/pkg/i18n/locales

RUN mkdir -p /app/logs

EXPOSE 8080
CMD ["./tokenhub"]

# ----------------------------------------------------------------
# Stage 3: worker — Worker 镜像（含 Chromium 供价格爬虫使用）
# Debian bookworm-slim，压缩后约 450MB
# ----------------------------------------------------------------
FROM debian:bookworm-slim AS worker

# --mount=type=cache 缓存 apt 包，避免每次 build 重新下载
RUN --mount=type=cache,target=/var/cache/apt,sharing=locked \
    --mount=type=cache,target=/var/lib/apt,sharing=locked \
    apt-get update && \
    apt-get install -y --no-install-recommends \
        ca-certificates \
        tzdata \
        chromium \
        fonts-wqy-zenhei \
        wget && \
    rm -rf /var/lib/apt/lists/*

# go-rod 使用此路径定位 Chromium
ENV ROD_BROWSER_BIN=/usr/bin/chromium

WORKDIR /app

COPY --from=builder /build/tokenhub .
COPY --from=builder /build/configs ./configs
COPY --from=builder /build/internal/pkg/i18n/locales ./internal/pkg/i18n/locales

RUN mkdir -p /app/logs

EXPOSE 8080
CMD ["./tokenhub"]

# ----------------------------------------------------------------
# Stage 4: monolith — 本地 docker-compose 单体模式（默认目标）
# 包含两个二进制，CMD=./server，与原来完全兼容
# ----------------------------------------------------------------
FROM debian:bookworm-slim AS monolith

RUN --mount=type=cache,target=/var/cache/apt,sharing=locked \
    --mount=type=cache,target=/var/lib/apt,sharing=locked \
    apt-get update && \
    apt-get install -y --no-install-recommends \
        ca-certificates \
        tzdata \
        chromium \
        fonts-wqy-zenhei \
        wget && \
    rm -rf /var/lib/apt/lists/*

ENV ROD_BROWSER_BIN=/usr/bin/chromium

WORKDIR /app

# 单体模式同时包含 server（旧入口）和 tokenhub（新入口）
COPY --from=builder /build/server .
COPY --from=builder /build/tokenhub .
COPY --from=builder /build/configs ./configs
COPY --from=builder /build/internal/pkg/i18n/locales ./internal/pkg/i18n/locales

RUN mkdir -p /app/logs

EXPOSE 8080
CMD ["./server"]
