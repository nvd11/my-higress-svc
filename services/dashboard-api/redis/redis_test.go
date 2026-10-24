package redis

import (
	"bytes"
	"compress/gzip"
	"context"
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

// 验证 EncodeBlob / DecodeBlob 的往返一致性、Gzip 前缀与历史明文兼容
func TestEncodeDecodeBlob(t *testing.T) {
	orig := []byte(`{"record":{"request_id":"abc","prompt":"温州一家人"}}`)

	encoded := EncodeBlob(orig)
	if !strings.HasPrefix(encoded, "H4sI") {
		t.Errorf("expected gzip base64 prefix H4sI, got: %.10s", encoded)
	}
	if restored := DecodeBlob(encoded); string(restored) != string(orig) {
		t.Errorf("roundtrip mismatch: %s", restored)
	}

	// 历史明文 JSON 必须原样还原
	plain := `{"prompt":{"user_prompt":"hi"}}`
	if restored := DecodeBlob(plain); string(restored) != plain {
		t.Errorf("plaintext compat broken: %s", restored)
	}

	// 空串 / 非法 base64 不得 panic
	if got := DecodeBlob(""); got != nil {
		t.Errorf("expected nil for empty input, got %v", got)
	}
	if got := DecodeBlob("H4sI-not-valid-base64!!!"); got != nil {
		t.Errorf("expected nil for corrupt blob, got %v", got)
	}
}

// WAL 助手在 Redis 未初始化时必须全程 no-op（保证单测与降级启动不炸）
func TestWALHelpersNilClientSafe(t *testing.T) {
	Client = nil
	ctx := context.Background()

	if err := SetWAL(ctx, "req-1", []byte(`{}`)); err != nil {
		t.Errorf("SetWAL should be no-op, got: %v", err)
	}
	if _, found := GetWAL(ctx, "req-1"); found {
		t.Errorf("GetWAL should report not found")
	}
	if err := DelWAL(ctx, "req-1"); err != nil {
		t.Errorf("DelWAL should be no-op, got: %v", err)
	}
	if err := MoveWALToDead(ctx, "req-1", []byte(`{}`)); err != nil {
		t.Errorf("MoveWALToDead should be no-op, got: %v", err)
	}
	keys, err := ListWALKeys(ctx, 10)
	if err != nil || len(keys) != 0 {
		t.Errorf("ListWALKeys should be empty, got %v err=%v", keys, err)
	}

	// 空 requestID 同样不得写入非法 key
	if err := SetWAL(ctx, "", []byte(`{}`)); err != nil {
		t.Errorf("SetWAL empty id should be no-op, got: %v", err)
	}
}

// WAL key 前缀必须与热缓存前缀隔离，避免互相踩踏
func TestWALKeyPrefixIsolation(t *testing.T) {
	if WALPrefix == "litellm:payload:" || WALDeadPrefix == WALPrefix {
		t.Fatalf("WAL prefix collides with payload cache or dead prefix")
	}
	if !strings.HasPrefix(WALPrefix+"abc", "audit:wip:") {
		t.Errorf("unexpected WAL prefix: %s", WALPrefix)
	}
}
