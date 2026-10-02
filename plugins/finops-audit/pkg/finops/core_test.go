package finops

import (
	"bytes"
	"compress/gzip"
	"io"
	"testing"
)

// 1. 验证费用折算算法准确度 (涵盖 Google 原生 3.8 与思考 Tokens)
func TestCalculateCost(t *testing.T) {
	promptTokens := 10000
	completionTokens := 2000
	reasoningTokens := 1000
	fxRate := 7.2300

	costUSD, costCNY := CalculateCost("gemini-3.8-flash", promptTokens, completionTokens, reasoningTokens, fxRate)

	expectedUSD := 0.01875
	if costUSD != expectedUSD {
		t.Errorf("expected costUSD %f, got %f", expectedUSD, costUSD)
	}

	expectedCNY := 0.135563
	if costCNY != expectedCNY {
		t.Errorf("expected costCNY %f, got %f", expectedCNY, costCNY)
	}
}

// 2. 验证 Base64 图片正则折叠功能
func TestCollapseBase64Images(t *testing.T) {
	fakeBase64 := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
	longBase64 := fakeBase64 + fakeBase64 // > 64 chars
	rawContent := `{"messages": [{"role": "user", "content": [{"type": "image_url", "image_url": {"url": "data:image/png;base64,` + longBase64 + `"}}]}]}`

	collapsed := CollapseBase64Images(rawContent)

	if len(collapsed) >= len(rawContent) {
		t.Errorf("expected collapsed content to be shorter than raw, but got %d vs %d", len(collapsed), len(rawContent))
	}

	if !bytes.Contains([]byte(collapsed), []byte("[base64_image_collapsed: len=")) {
		t.Errorf("expected collapsed marker in output, got: %s", collapsed)
	}
}

// 3. 验证内存 Gzip 压缩与可读性
func TestCompressGzip(t *testing.T) {
	rawText := []byte("Hello, this is a large LLM prompt that needs to be compressed before sending to VictoriaLogs.")
	compressed, err := CompressGzip(rawText)
	if err != nil {
		t.Fatalf("CompressGzip failed: %v", err)
	}

	zr, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatalf("gzip.NewReader failed: %v", err)
	}
	defer zr.Close()

	decompressed, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("failed reading gzip stream: %v", err)
	}

	if string(decompressed) != string(rawText) {
		t.Errorf("decompressed text mismatch: got %s, want %s", string(decompressed), string(rawText))
	}
}
