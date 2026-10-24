#!/usr/bin/env bash
set -e
echo "💲 Regenerating pricing table from ../../config.yaml (single source of truth)..."
CGO_ENABLED=0 go run internal/pricinggen/main.go -config ../../config.yaml -out pkg/finops/pricing_gen.go
echo "📦 Building Wasm binary (Higress official Go 1.24+ wasip1/c-shared)..."
CGO_ENABLED=0 GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -buildvcs=false -o plugin.wasm .
echo "✅ Build completed: plugin.wasm ($(ls -lh plugin.wasm | awk '{print $5}'))"
