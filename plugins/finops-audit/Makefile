.PHONY: test build clean

# 本地秒级运行全部核心业务单元测试
test:
	@echo "🧪 Running unit tests for finops core..."
	CGO_ENABLED=0 go test -v ./pkg/finops

# 针对 WebAssembly wasi 目标进行 TinyGo 编译（禁用 scheduler 防止 missing poll_oneoff）
build:
	@echo "📦 Compiling WebAssembly binary via TinyGo (target=wasi, scheduler=none)..."
	tinygo build -o plugin.wasm -scheduler=none -target=wasi .

clean:
	@rm -f plugin.wasm
