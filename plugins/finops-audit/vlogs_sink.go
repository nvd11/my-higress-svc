package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/higress-group/wasm-go/pkg/wrapper"
	"github.com/nvd11/my-higress-svc/plugins/finops-audit/pkg/finops"
)

// VictoriaLogsPayload 严格对齐 my-litellm-service 的 JSONLine 协议
type VictoriaLogsPayload struct {
	Time             string  `json:"_time"`
	Stream           string  `json:"_stream"`
	Msg              string  `json:"_msg"`
	Env              string  `json:"env"`
	Service          string  `json:"service"`
	Type             string  `json:"type"`
	RequestID        string  `json:"request_id"`
	Model            string  `json:"model"`
	KeyAlias         string  `json:"key_alias"`
	StatusCode       int     `json:"status_code"`
	LatencyMS        int     `json:"latency_ms"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	ReasoningTokens  int     `json:"reasoning_tokens"`
	TotalTokens      int     `json:"total_tokens"`
	Spend            float64 `json:"spend"`
	ShardIndex       int     `json:"shard_index"`
	TotalShards      int     `json:"total_shards"`
	PromptChunk      string  `json:"prompt_chunk"`
	Prompt           string  `json:"prompt"`
	Response         string  `json:"response"`
}

// SendToVictoriaLogs 异步将压缩后的报文发送给 StarFive 星光板上的 VictoriaLogs
func SendToVictoriaLogs(client wrapper.HttpClient, targetURL string, payload VictoriaLogsPayload) error {
	if targetURL == "" {
		return nil
	}

	// 自动修正为标准 jsonline 端点
	url := strings.TrimRight(targetURL, "/")
	if !strings.HasSuffix(url, "/insert/jsonline") && !strings.HasSuffix(url, "/insert/jsonl") {
		url = url + "/insert/jsonline"
	} else if strings.HasSuffix(url, "/insert/jsonl") {
		url = strings.TrimSuffix(url, "/insert/jsonl") + "/insert/jsonline"
	}

	payload.Time = time.Now().UTC().Format(time.RFC3339Nano)
	payload.Stream = `{env="prod",service="litellm",type="payload"}`
	payload.Env = "prod"
	payload.Service = "litellm"
	payload.Type = "payload"
	payload.ShardIndex = 1
	payload.TotalShards = 1

	foldedPrompt := finops.CollapseBase64Images(payload.Prompt)
	foldedResponse := finops.CollapseBase64Images(payload.Response)
	payload.Prompt = foldedPrompt
	payload.PromptChunk = foldedPrompt
	payload.Response = foldedResponse
	payload.Msg = fmt.Sprintf("LLM 调用日志: request_id=%s, model=%s, status=%d, latency=%dms",
		payload.RequestID, payload.Model, payload.StatusCode, payload.LatencyMS)

	jsonBytes, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	jsonBytes = append(jsonBytes, '\n')

	compressed, err := finops.CompressGzip(jsonBytes)
	if err != nil {
		return err
	}

	headers := [][2]string{
		{"Content-Type", "application/stream+json"},
		{"Content-Encoding", "gzip"},
	}

	return client.Post(url, headers, compressed, func(statusCode int, responseHeaders [][2]string, responseBody []byte) {
		// 旁路异步回调
	}, 5000)
}
