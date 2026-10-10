package vlogs

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/nvd11/my-higress-svc/services/dashboard-api/config"
)

const (
	DefaultChunkSize     = 800000
	SafeSingleEntryLimit = 1200000
)

type Client struct {
	endpoint   string
	httpClient *http.Client
}

func NewClient(cfg *config.Config) *Client {
	return &Client{
		endpoint: strings.TrimRight(cfg.VictoriaLogsURL, "/"),
		httpClient: &http.Client{
			Timeout: 45 * time.Second,
		},
	}
}

// SplitIntoChunks 按 chunk_size 将文本拆为分片
func SplitIntoChunks(text string, chunkSize int) []string {
	if text == "" {
		return []string{""}
	}
	runes := []rune(text)
	if len(runes) <= chunkSize {
		return []string{text}
	}

	var chunks []string
	for i := 0; i < len(runes); i += chunkSize {
		end := i + chunkSize
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, string(runes[i:end]))
	}
	return chunks
}

// WritePayload 写入 VictoriaLogs (支持 1.3MB 智能分块切片与 Gzip 传输)
func (c *Client) WritePayload(ctx context.Context, requestID string, promptObj, responseObj map[string]interface{}, meta map[string]interface{}) error {
	promptBytes, _ := json.Marshal(promptObj)
	responseBytes, _ := json.Marshal(responseObj)
	promptStr := string(promptBytes)
	responseStr := string(responseBytes)

	singleEntrySize := len(promptStr) + len(responseStr) + 500
	var promptChunks []string
	if singleEntrySize > SafeSingleEntryLimit {
		conservativeChunkSize := DefaultChunkSize - len(responseStr) - 500
		if conservativeChunkSize <= 0 {
			conservativeChunkSize = DefaultChunkSize / 2
		}
		promptChunks = SplitIntoChunks(promptStr, conservativeChunkSize)
	} else {
		promptChunks = SplitIntoChunks(promptStr, DefaultChunkSize)
	}

	totalShards := len(promptChunks)
	timestamp, _ := meta["timestamp"].(string)
	if timestamp == "" {
		timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}
	model, _ := meta["model"].(string)
	keyAlias, _ := meta["key_alias"].(string)
	statusCode, _ := meta["status_code"].(int)
	latencyMS, _ := meta["latency_ms"].(int)

	var jsonLines []string
	for idx, chunk := range promptChunks {
		shardIdx := idx + 1
		msg := fmt.Sprintf("LLM 调用日志: request_id=%s, model=%s, status=%d, latency=%dms",
			requestID, model, statusCode, latencyMS)
		if totalShards > 1 {
			msg += fmt.Sprintf(" [shard %d/%d]", shardIdx, totalShards)
		}

		entry := map[string]interface{}{
			"_time":        timestamp,
			"_stream":      `{env="prod",service="litellm",type="payload"}`,
			"_msg":         msg,
			"env":          "prod",
			"service":      "litellm",
			"type":         "payload",
			"request_id":   requestID,
			"model":        model,
			"key_alias":    keyAlias,
			"status_code":  statusCode,
			"latency_ms":   latencyMS,
			"shard_index":  shardIdx,
			"total_shards": totalShards,
			"prompt_chunk": chunk,
			"response":     "",
		}
		if shardIdx == 1 {
			entry["response"] = responseStr
		}

		b, _ := json.Marshal(entry)
		jsonLines = append(jsonLines, string(b))
	}

	rawBody := strings.Join(jsonLines, "\n") + "\n"

	// 内存 Gzip 压缩
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	_, _ = zw.Write([]byte(rawBody))
	_ = zw.Close()

	req, err := http.NewRequestWithContext(ctx, "POST", c.endpoint+"/insert/jsonline", &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/stream+json")
	req.Header.Set("Content-Encoding", "gzip")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("victorialogs write failed: HTTP %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

func getStringField(m map[string]interface{}, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	switch s := v.(type) {
	case string:
		if s == "<nil>" {
			return ""
		}
		return s
	default:
		b, err := json.Marshal(v)
		if err == nil {
			return string(b)
		}
		return fmt.Sprintf("%v", v)
	}
}

// ReadPayload 通过 LogsQL 查询并重组还原分片
func (c *Client) ReadPayload(ctx context.Context, requestID, dateStr string) (map[string]interface{}, map[string]interface{}, error) {
	query := fmt.Sprintf(`type: "payload" AND request_id: exact("%s")`, requestID)
	if dateStr != "" {
		query += fmt.Sprintf(` AND _time: %s`, dateStr)
	}
	query += " | fields shard_index, total_shards, prompt_chunk, prompt, response, model, _time"

	form := url.Values{}
	form.Set("query", query)
	form.Set("limit", "1000")

	req, err := http.NewRequestWithContext(ctx, "POST", c.endpoint+"/select/logsql/query", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("victorialogs query failed: HTTP %d", resp.StatusCode)
	}

	rawResp, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}

	lines := strings.Split(strings.TrimSpace(string(rawResp)), "\n")
	if len(lines) == 0 || lines[0] == "" {
		return nil, nil, nil
	}

	type ShardDoc struct {
		ShardIndex  int
		TotalShards int
		PromptChunk string
		Prompt      string
		Response    string
		Model       string
		Time        string
	}

	var docs []ShardDoc
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var rawMap map[string]interface{}
		if err := json.Unmarshal([]byte(line), &rawMap); err == nil {
			pChunk := getStringField(rawMap, "prompt_chunk")
			pFull := getStringField(rawMap, "prompt")
			respStr := getStringField(rawMap, "response")
			doc := ShardDoc{
				PromptChunk: pChunk,
				Prompt:      pFull,
				Response:    respStr,
				Model:       fmt.Sprintf("%v", rawMap["model"]),
				Time:        fmt.Sprintf("%v", rawMap["_time"]),
				ShardIndex:  1,
				TotalShards: 1,
			}
			// 兼容处理字符串与数字类型的 shard_index
			if si, ok := rawMap["shard_index"]; ok {
				switch siv := si.(type) {
				case float64:
					doc.ShardIndex = int(siv)
				case string:
					if n, err := strconv.Atoi(siv); err == nil {
						doc.ShardIndex = n
					}
				}
			}
			if ts, ok := rawMap["total_shards"]; ok {
				switch tsv := ts.(type) {
				case float64:
					doc.TotalShards = int(tsv)
				case string:
					if n, err := strconv.Atoi(tsv); err == nil {
						doc.TotalShards = n
					}
				}
			}
			docs = append(docs, doc)
		}
	}

	if len(docs) == 0 {
		return nil, nil, nil
	}

	// 严格按 shard_index 去重 (保留 response 更长或更新的记录)
	uniqueShards := make(map[int]ShardDoc)
	for _, doc := range docs {
		existing, ok := uniqueShards[doc.ShardIndex]
		if !ok || len(doc.Response) > len(existing.Response) || (len(doc.Response) == len(existing.Response) && doc.Time > existing.Time) {
			uniqueShards[doc.ShardIndex] = doc
		}
	}

	// 升序排列
	var sortedIndices []int
	for idx := range uniqueShards {
		sortedIndices = append(sortedIndices, idx)
	}
	sort.Ints(sortedIndices)

	var promptParts []string
	var finalResponseStr string
	for _, idx := range sortedIndices {
		shard := uniqueShards[idx]
		chunk := shard.PromptChunk
		if chunk == "" {
			chunk = shard.Prompt
		}
		promptParts = append(promptParts, chunk)
		if shard.Response != "" && finalResponseStr == "" {
			finalResponseStr = shard.Response
		}
	}

	fullPromptStr := strings.Join(promptParts, "")
	var promptObj map[string]interface{}
	if err := json.Unmarshal([]byte(fullPromptStr), &promptObj); err != nil {
		promptObj = map[string]interface{}{"raw_text": fullPromptStr}
	}

	var responseObj map[string]interface{}
	if err := json.Unmarshal([]byte(finalResponseStr), &responseObj); err != nil {
		responseObj = map[string]interface{}{"reply": finalResponseStr}
	}

	log.Printf("📖 ReadPayload restored for %s: promptLen=%d, respLen=%d", requestID, len(fullPromptStr), len(finalResponseStr))
	return promptObj, responseObj, nil
}

// SearchPayloads 全文倒排检索匹配的 request_id 列表
func (c *Client) SearchPayloads(ctx context.Context, keyword string, limit int) ([]string, error) {
	if strings.TrimSpace(keyword) == "" {
		return nil, nil
	}

	cleanKw := strings.TrimSpace(keyword)
	query := fmt.Sprintf(`_stream:{type="payload"} AND (prompt_chunk:~"(?i)%s" OR response:~"(?i)%s") | uniq by (request_id) | limit %d`,
		cleanKw, cleanKw, limit)

	form := url.Values{}
	form.Set("query", query)

	req, err := http.NewRequestWithContext(ctx, "POST", c.endpoint+"/select/logsql/query", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	var rids []string
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(line), &m); err == nil {
			if rid, ok := m["request_id"].(string); ok && rid != "" {
				rids = append(rids, rid)
			}
		}
	}
	return rids, nil
}
