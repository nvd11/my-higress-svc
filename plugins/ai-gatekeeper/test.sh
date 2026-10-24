#!/usr/bin/env bash
set -e
echo "🧪 Running ai-gatekeeper unit tests..."
CGO_ENABLED=0 go test -v ./...
