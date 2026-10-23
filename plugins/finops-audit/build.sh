#!/usr/bin/env bash
set -e
echo "📦 Building Wasm binary (tinygo wasi with -scheduler=none)..."
tinygo build -o plugin.wasm -scheduler=none -target=wasi .
echo "✅ Build completed: plugin.wasm ($(ls -lh plugin.wasm | awk '{print $5}'))"
