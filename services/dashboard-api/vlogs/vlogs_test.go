package vlogs

import (
	"strings"
	"testing"
)

func TestSplitIntoChunks(t *testing.T) {
	// 1. 空文本
	emptyChunks := SplitIntoChunks("", 100)
	if len(emptyChunks) != 1 || emptyChunks[0] != "" {
		t.Errorf("expected 1 empty chunk, got: %v", emptyChunks)
	}

	// 2. 短文本 (<= chunkSize)
	shortText := "Hello, VictoriaLogs!"
	shortChunks := SplitIntoChunks(shortText, 100)
	if len(shortChunks) != 1 || shortChunks[0] != shortText {
		t.Errorf("expected 1 chunk matching text, got: %v", shortChunks)
	}

	// 3. 长文本切片验证 (例如 250 字符，按 100 切片，应为 3 片: 100, 100, 50)
	longText := strings.Repeat("A", 100) + strings.Repeat("B", 100) + strings.Repeat("C", 50)
	chunks := SplitIntoChunks(longText, 100)
	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks, got: %d", len(chunks))
	}
	if chunks[0] != strings.Repeat("A", 100) {
		t.Errorf("chunk 0 mismatch")
	}
	if chunks[1] != strings.Repeat("B", 100) {
		t.Errorf("chunk 1 mismatch")
	}
	if chunks[2] != strings.Repeat("C", 50) {
		t.Errorf("chunk 2 mismatch")
	}

	// 验证拼接还原无损
	joined := strings.Join(chunks, "")
	if joined != longText {
		t.Errorf("reconstructed text mismatch, len: %d vs %d", len(joined), len(longText))
	}
}
