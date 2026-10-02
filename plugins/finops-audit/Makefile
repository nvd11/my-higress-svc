.PHONY: test build clean

# 本地秒级运行全部核心业务单元测试
test:
	@echo "🧪 Running unit tests for finops core..."
	CGO_ENABLED=0 go test -v ./pkg/finops

# 针对 WebAssembly wasip1 目标进行跨平台标准编译
build:
	@echo "📦 Compiling WebAssembly binary targeting wasip1/wasm..."
	GOOS=wasip1 GOARCH=wasm go build -o plugin.wasm .

clean:
	@rm -f plugin.wasm
