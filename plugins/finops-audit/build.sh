#!/usr/bin/env bash
set -e
echo "📦 Building Wasm binary (wasip1/wasm)..."
CGO_ENABLED=0 GOOS=wasip1 GOARCH=wasm go build -o plugin.wasm .
echo "✅ Build completed: plugin.wasm ($(ls -lh plugin.wasm | awk '{print $5}'))"
