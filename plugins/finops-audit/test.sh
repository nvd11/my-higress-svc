#!/usr/bin/env bash
set -e
echo "🧪 Running finops unit tests..."
CGO_ENABLED=0 go test -v ./pkg/finops
