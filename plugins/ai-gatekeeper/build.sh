#!/usr/bin/env bash
set -e
echo "💲 Regenerating gatekeeper models list from ../../config.yaml..."
CGO_ENABLED=0 go run -C ../finops-audit ./internal/pricinggen/main.go -config ../../config.yaml -out "" -gatekeeper-out ../ai-gatekeeper/models_gen.go
echo "📦 Building ai-gatekeeper Wasm binary (Higress official Go wasip1/c-shared)..."
CGO_ENABLED=0 GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -buildvcs=false -o plugin.wasm .
echo "✅ Build completed: plugin.wasm ($(ls -lh plugin.wasm | awk '{print $5}'))"
