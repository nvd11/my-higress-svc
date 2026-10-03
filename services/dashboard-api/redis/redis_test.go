package redis

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"io"
	"strings"
	"testing"
)

// 测试 Redis 序列化压缩算法与解压还原性
func TestRedisPayloadCompressionConsistency(t *testing.T) {
	promptObj := map[string]interface{}{
		"messages": []map[string]string{
			{"role": "user", "content": "Tell me a story about cloud native."},
		},
	}
	responseObj := map[string]interface{}{
		"content": "Once upon a time in a cloud native cluster...",
	}

	rawJSON, err := json.Marshal(map[string]interface{}{
		"prompt":   promptObj,
		"response": responseObj,
	})
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	// 1. 模拟写入端的 Gzip + Base64
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	if err != nil {
		t.Fatalf("gzip writer failed: %v", err)
	}
	_, _ = zw.Write(rawJSON)
	_ = zw.Close()

	compressedStr := base64.StdEncoding.EncodeToString(buf.Bytes())

	// 核心断言: Gzip 必须是 H4sI 开头
	if !strings.HasPrefix(compressedStr, "H4sI") {
		t.Errorf("expected Gzip Base64 to start with H4sI, got: %s", compressedStr[:10])
	}

	// 2. 模拟读取端的 Base64 解码 + Gzip 解压
	dec, err := base64.StdEncoding.DecodeString(compressedStr)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	zr, err := gzip.NewReader(bytes.NewReader(dec))
	if err != nil {
		t.Fatalf("gzip reader failed: %v", err)
	}
	defer zr.Close()

	restoredBytes, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}

	var restored map[string]interface{}
	if err := json.Unmarshal(restoredBytes, &restored); err != nil {
		t.Fatalf("unmarshal restored json failed: %v", err)
	}

	pMap, ok := restored["prompt"].(map[string]interface{})
	if !ok || len(pMap) == 0 {
		t.Errorf("restored prompt invalid: %v", restored["prompt"])
	}
}
