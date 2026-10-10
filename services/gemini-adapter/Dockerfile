# ==============================================================================
# Multi-stage Dockerfile for Gemini Official Go SDK Adapter Sidecar
# ==============================================================================

# Stage 1: Build static binary
FROM golang:alpine AS builder

WORKDIR /app

RUN apk add --no-cache git ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o gemini-adapter .

# Stage 2: Production runtime image
FROM alpine:3.20

WORKDIR /app

RUN apk add --no-cache ca-certificates tzdata

COPY --from=builder /app/gemini-adapter /app/gemini-adapter
COPY config.yaml /app/config.yaml

EXPOSE 8080

ENV PORT=8080

ENTRYPOINT ["/app/gemini-adapter"]
