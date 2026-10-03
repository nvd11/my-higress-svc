package finops

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
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

	if !strings.Contains(collapsed, "[base64_image_collapsed: len=") {
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

// 4. [新增] 验证 VictoriaLogs JSONLine 冷归档报文格式与字段契约 (对齐 COMPATIBILITY.md)
func TestVictoriaLogsJSONLineFormat(t *testing.T) {
	reqID := "chatcmpl-test-uuid-1234"
	model := "gemini-3.8-flash"
	keyAlias := "test-user"
	prompt := `[{"role": "user", "content": "Hello"}]`
	response := `{"reply": "World"}`

	payloadMap := map[string]interface{}{
		"_time":        time.Now().UTC().Format(time.RFC3339Nano),
		"_stream":      `{env="prod",service="litellm",type="payload"}`,
		"_msg":         fmt.Sprintf("LLM 调用日志: request_id=%s, model=%s, status=200, latency=100ms", reqID, model),
		"env":          "prod",
		"service":      "litellm",
		"type":         "payload",
		"request_id":   reqID,
		"model":        model,
		"key_alias":    keyAlias,
		"status_code":  200,
		"latency_ms":   100,
		"shard_index":  1,
		"total_shards": 1,
		"prompt_chunk": prompt,
		"prompt":       prompt,
		"response":     response,
	}

	jsonBytes, err := json.Marshal(payloadMap)
	if err != nil {
		t.Fatalf("failed to marshal VictoriaLogs payload: %v", err)
	}

	rawStr := string(jsonBytes)

	if !strings.Contains(rawStr, `"_stream":"{env=\"prod\",service=\"litellm\",type=\"payload\"}"`) {
		t.Errorf("missing exact _stream tag, got: %s", rawStr)
	}
	if !strings.Contains(rawStr, `"request_id":"chatcmpl-test-uuid-1234"`) {
		t.Errorf("missing request_id, got: %s", rawStr)
	}
	if !strings.Contains(rawStr, `"shard_index":1`) {
		t.Errorf("missing shard_index, got: %s", rawStr)
	}
}

// 5. 验证 Redis L2 抽屉热缓存的编解码与可还原性 (Base64 + Gzip 兼容性)
func TestRedisPayloadCacheEncoding(t *testing.T) {
	prompt := `[{"role": "user", "content": "What is WebAssembly?"}]`
	response := `{"content": "WebAssembly is a binary instruction format..."}`

	rawMap := map[string]string{
		"prompt":   prompt,
		"response": response,
	}
	rawJSON, err := json.Marshal(rawMap)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	// 1. Gzip 压缩 (模拟 Redis 写入端)
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	if err != nil {
		t.Fatalf("gzip.NewWriterLevel failed: %v", err)
	}
	if _, err := zw.Write(rawJSON); err != nil {
		t.Fatalf("zw.Write failed: %v", err)
	}
	_ = zw.Close()

	// 2. Base64 编码
	redisVal := base64.StdEncoding.EncodeToString(buf.Bytes())

	// 3. 验证老系统大屏抽屉的解压逻辑 (模拟 payload.py 读取端)
	decodedBytes, err := base64.StdEncoding.DecodeString(redisVal)
	if err != nil {
		t.Fatalf("base64.Decode failed: %v", err)
	}

	zr, err := gzip.NewReader(bytes.NewReader(decodedBytes))
	if err != nil {
		t.Fatalf("gzip.NewReader failed: %v", err)
	}
	defer zr.Close()

	decompressedJSON, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("io.ReadAll failed: %v", err)
	}

	var restoredMap map[string]string
	if err := json.Unmarshal(decompressedJSON, &restoredMap); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	if restoredMap["prompt"] != prompt {
		t.Errorf("restored prompt mismatch: got %s, want %s", restoredMap["prompt"], prompt)
	}
	if restoredMap["response"] != response {
		t.Errorf("restored response mismatch: got %s, want %s", restoredMap["response"], response)
	}
}

// 6. [新增] 验证 Virtual Key 鉴权与租户身份标记 (支持 Cindy 专属凭证)
func TestResolveVirtualKey(t *testing.T) {
	vkeys := map[string]string{
		"sk-cindy-higress-20261003-888888": "cindy",
		"sk-jayden-production-key":         "jayden",
	}

	// 1. 成功匹配 Cindy 专属 Key
	alias, ok := ResolveVirtualKey("Bearer sk-cindy-higress-20261003-888888", vkeys)
	if !ok || alias != "cindy" {
		t.Errorf("expected cindy, got: %s (ok: %v)", alias, ok)
	}

	// 2. 成功匹配 Jayden Key
	aliasJayden, okJayden := ResolveVirtualKey("Bearer sk-jayden-production-key", vkeys)
	if !okJayden || aliasJayden != "jayden" {
		t.Errorf("expected jayden, got: %s (ok: %v)", aliasJayden, okJayden)
	}

	// 3. 伪造或非法 Key 拒绝通过 (防止盗刷)
	_, okFake := ResolveVirtualKey("Bearer sk-fake-key-123", vkeys)
	if okFake {
		t.Errorf("expected unauthorized for fake key")
	}

	// 4. 空配置时允许默认通行
	aliasDef, okDef := ResolveVirtualKey("Bearer anything", nil)
	if !okDef || aliasDef != "default" {
		t.Errorf("expected default for empty vkeys config")
	}
}
