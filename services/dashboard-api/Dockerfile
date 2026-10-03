# ==============================================================================
# Multi-stage Dockerfile for Higress Dashboard API (Go Edition)
# ==============================================================================

# Stage 1: Build static binary
FROM golang:1.24-alpine AS builder

WORKDIR /app

# 安装基础构建依赖
RUN apk add --no-cache git ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# 静态交叉编译 (剥离调试符号减小体积)
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o dashboard-api .

# Stage 2: Production runtime image
FROM alpine:3.20

WORKDIR /app

RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /app/dashboard-api /app/dashboard-api

# 预留静态页面挂载目录
RUN mkdir -p /app/static

EXPOSE 4000

ENV PORT=4000

ENTRYPOINT ["/app/dashboard-api"]
