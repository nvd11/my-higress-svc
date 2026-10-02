package main

import (
	"encoding/json"
	"time"

	"github.com/higress-group/wasm-go/pkg/wrapper"
	"github.com/nvd11/my-higress-svc/plugins/finops-audit/pkg/finops"
)

// VictoriaLogsPayload 对应 VictoriaLogs JSONL 冷归档格式
type VictoriaLogsPayload struct {
	Time             string `json:"_time"`
	Stream           string `json:"_stream"`
	RequestID        string `json:"request_id"`
	ModelRequested   string `json:"model_requested"`
	ModelUsed        string `json:"model_used"`
	APIKeyAlias      string `json:"api_key_alias"`
	Prompt           string `json:"prompt"`
	Response         string `json:"response"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	ReasoningTokens  int    `json:"reasoning_tokens"`
	TotalTokens      int    `json:"total_tokens"`
	StatusCode       int    `json:"status_code"`
	LatencyMS        int    `json:"latency_ms"`
}

// SendToVictoriaLogs 异步将压缩后的报文发送给 StarFive 星光板上的 VictoriaLogs
func SendToVictoriaLogs(client wrapper.HttpClient, targetURL string, payload VictoriaLogsPayload) error {
	if targetURL == "" {
		return nil
	}

	payload.Time = time.Now().UTC().Format(time.RFC3339Nano)
	payload.Stream = `{app="higress-gateway", env="production"}`
	payload.Prompt = finops.CollapseBase64Images(payload.Prompt)
	payload.Response = finops.CollapseBase64Images(payload.Response)

	jsonBytes, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	// VictoriaLogs JSONL 格式要求末尾换行
	jsonBytes = append(jsonBytes, '\n')

	compressed, err := finops.CompressGzip(jsonBytes)
	if err != nil {
		return err
	}

	headers := [][2]string{
		{"Content-Type", "application/stream+json"},
		{"Content-Encoding", "gzip"},
	}

	// 使用 Higress wasm-go SDK 提供的异步非阻塞 Post 调用
	return client.Post(targetURL, headers, compressed, func(statusCode int, responseHeaders [][2]string, responseBody []byte) {
		// 异步旁路回调: 仅记录日志，绝不影响客户端响应
		if statusCode < 200 || statusCode >= 300 {
			// 可通过 proxywasm 日志记录错误
		}
	}, 5000)
}
