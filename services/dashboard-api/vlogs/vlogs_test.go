package vlogs

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nvd11/my-higress-svc/services/dashboard-api/config"
)

// 1. 验证基础分块切片算法
func TestSplitIntoChunks(t *testing.T) {
	// 空文本
	emptyChunks := SplitIntoChunks("", 100)
	if len(emptyChunks) != 1 || emptyChunks[0] != "" {
		t.Errorf("expected 1 empty chunk, got: %v", emptyChunks)
	}

	// 短文本
	shortText := "Hello, VictoriaLogs!"
	shortChunks := SplitIntoChunks(shortText, 100)
	if len(shortChunks) != 1 || shortChunks[0] != shortText {
		t.Errorf("expected 1 chunk matching text, got: %v", shortChunks)
	}

	// 长文本切片验证 (250 字符，按 100 切片，应为 3 片: 100, 100, 50)
	longText := strings.Repeat("A", 100) + strings.Repeat("B", 100) + strings.Repeat("C", 50)
	chunks := SplitIntoChunks(longText, 100)
	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks, got: %d", len(chunks))
	}
	if chunks[0] != strings.Repeat("A", 100) || chunks[1] != strings.Repeat("B", 100) || chunks[2] != strings.Repeat("C", 50) {
		t.Errorf("chunk content mismatch")
	}

	joined := strings.Join(chunks, "")
	if joined != longText {
		t.Errorf("reconstructed text mismatch")
	}
}

// 2. 验证 WritePayload 写入端点 (Gzip 压缩传输与 JSONLine 格式)
func TestWritePayload(t *testing.T) {
	var receivedHeaders http.Header
	var uncompressedBody string

	// 内存模拟 VictoriaLogs 服务端
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHeaders = r.Header.Clone()
		if r.URL.Path != "/insert/jsonline" {
			w.WriteHeader(http.StatusNotFound)
			return
		}

		// 解压 Gzip 请求体
		zr, err := gzip.NewReader(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		defer zr.Close()

		b, _ := io.ReadAll(zr)
		uncompressedBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	cfg := &config.Config{VictoriaLogsURL: ts.URL}
	client := NewClient(cfg)

	promptObj := map[string]interface{}{"role": "user", "content": "Test write"}
	responseObj := map[string]interface{}{"reply": "Test reply"}
	meta := map[string]interface{}{
		"model":       "gemini-3.8-flash",
		"key_alias":   "cindy-test",
		"status_code": 200,
		"latency_ms":  120,
	}

	err := client.WritePayload(context.Background(), "chatcmpl-test-write", promptObj, responseObj, meta)
	if err != nil {
		t.Fatalf("WritePayload failed: %v", err)
	}

	if receivedHeaders.Get("Content-Encoding") != "gzip" {
		t.Errorf("expected Content-Encoding gzip, got: %s", receivedHeaders.Get("Content-Encoding"))
	}
	if !strings.Contains(uncompressedBody, `"request_id":"chatcmpl-test-write"`) {
		t.Errorf("body missing request_id: %s", uncompressedBody)
	}
	if !strings.Contains(uncompressedBody, `_stream`) {
		t.Errorf("body missing _stream: %s", uncompressedBody)
	}
}

// 3. 验证 ReadPayload 动态多分片重组还原与去重竞选算法
func TestReadPayloadReassembly(t *testing.T) {
	// 构造 2 个乱序分片：分片 2 优先返回，分片 1 随后返回
	shard1 := map[string]interface{}{
		"_time":        "2026-10-03T10:00:00Z",
		"shard_index":  1,
		"total_shards": 2,
		"prompt_chunk": `{"messages":[{"role":"user",`,
		"response":     `{"content":"Final Answer"}`,
		"model":        "gemini-3.8-flash",
	}
	shard2 := map[string]interface{}{
		"_time":        "2026-10-03T10:00:00Z",
		"shard_index":  2,
		"total_shards": 2,
		"prompt_chunk": `"content":"Full Query"}]}`,
		"response":     "",
		"model":        "gemini-3.8-flash",
	}

	b1, _ := json.Marshal(shard1)
	b2, _ := json.Marshal(shard2)

	// 故意乱序返回: 先给 shard 2，后给 shard 1
	mockResponseLines := fmt.Sprintf("%s\n%s\n", string(b2), string(b1))

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/select/logsql/query" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(mockResponseLines))
	}))
	defer ts.Close()

	cfg := &config.Config{VictoriaLogsURL: ts.URL}
	client := NewClient(cfg)

	pData, rData, err := client.ReadPayload(context.Background(), "chatcmpl-shard-test", "2026-10-03")
	if err != nil {
		t.Fatalf("ReadPayload failed: %v", err)
	}

	if pData == nil || rData == nil {
		t.Fatalf("expected non-nil prompt and response")
	}

	// 验证分片是否成功拼接还原成合法的 JSON 对象
	msgs, ok := pData["messages"].([]interface{})
	if !ok || len(msgs) != 1 {
		t.Fatalf("failed reassembling prompt messages: %v", pData)
	}
	firstMsg := msgs[0].(map[string]interface{})
	if firstMsg["content"] != "Full Query" {
		t.Errorf("prompt content mismatch: %v", firstMsg["content"])
	}

	// 验证 response 提取
	if rData["content"] != "Final Answer" {
		t.Errorf("response content mismatch: %v", rData["content"])
	}
}

// 4. 验证 SearchPayloads 全文检索能力
func TestSearchPayloads(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{\"request_id\":\"chatcmpl-search-1\"}\n{\"request_id\":\"chatcmpl-search-2\"}\n"))
	}))
	defer ts.Close()

	cfg := &config.Config{VictoriaLogsURL: ts.URL}
	client := NewClient(cfg)

	// 空关键词提前返回
	emptyRes, _ := client.SearchPayloads(context.Background(), "", 10)
	if len(emptyRes) != 0 {
		t.Errorf("expected empty result for empty keyword")
	}

	// 正常关键词检索
	rids, err := client.SearchPayloads(context.Background(), "WebAssembly", 10)
	if err != nil {
		t.Fatalf("SearchPayloads failed: %v", err)
	}
	if len(rids) != 2 || rids[0] != "chatcmpl-search-1" || rids[1] != "chatcmpl-search-2" {
		t.Errorf("unexpected search results: %v", rids)
	}
}
