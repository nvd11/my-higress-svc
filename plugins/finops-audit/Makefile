.PHONY: test build clean

# 本地秒级运行全部核心业务单元测试
test:
	@echo "🧪 Running unit tests for finops core..."
	CGO_ENABLED=0 go test -v ./pkg/finops

# 针对 WebAssembly wasip1 目标进行 Higress 官方 Go 1.24+ 原生编译
build:
	@echo "📦 Compiling WebAssembly binary targeting wasip1/wasm (c-shared)..."
	CGO_ENABLED=0 GOOS=wasip1 GOARCH=wasm go build -buildmode=c-shared -o plugin.wasm .

clean:
	@rm -f plugin.wasm
