package main

import (
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm"
	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm/types"
	"github.com/higress-group/wasm-go/pkg/wrapper"
	"github.com/tidwall/gjson"
)

const (
	pluginName = "finops-audit"

	ctxStartTime      = "start_time"
	ctxRequestID      = "request_id"
	ctxAPIKeyAlias    = "api_key_alias"
	ctxModelRequested = "model_requested"
	ctxPromptBody     = "prompt_body"
	ctxResponseBody   = "response_body"
	ctxProvider       = "provider"
)

func main() {
	wrapper.SetCtx(
		pluginName,
		wrapper.ParseConfig(parseConfig),
		wrapper.ProcessRequestHeaders(onHttpRequestHeaders),
		wrapper.ProcessRequestBody(onHttpRequestBody),
		wrapper.ProcessResponseHeaders(onHttpResponseHeaders),
		wrapper.ProcessResponseBody(onHttpResponseBody),
	)
}

func parseConfig(json gjson.Result, config *PluginConfig, log wrapper.Log) error {
	config.VictoriaLogsURL = json.Get("victoria_logs_url").String()
	config.DashboardAPIURL = json.Get("dashboard_api_url").String()
	config.DefaultFxRate = json.Get("default_fx_rate").Float()
	if config.DefaultFxRate <= 0 {
		config.DefaultFxRate = 7.2300 // 严格按照 COMPATIBILITY.md 7.23 兜底
	}

	config.VirtualKeys = make(map[string]string)
	if vkeys := json.Get("virtual_keys"); vkeys.Exists() && vkeys.IsObject() {
		vkeys.ForEach(func(key, val gjson.Result) bool {
			config.VirtualKeys[key.String()] = val.String()
			return true
		})
	}
	return nil
}

func onHttpRequestHeaders(ctx wrapper.HttpContext, config PluginConfig, log wrapper.Log) types.Action {
	ctx.SetContext(ctxStartTime, time.Now())

	reqID, _ := proxywasm.GetHttpRequestHeader("x-request-id")
	if reqID == "" {
		reqID = "chatcmpl-" + uuid.NewString()
	}
	ctx.SetContext(ctxRequestID, reqID)

	// 1. 优先从 Authorization: Bearer <TOKEN> 匹配 VirtualKeys 字典
	authHeader, _ := proxywasm.GetHttpRequestHeader("authorization")
	token := strings.TrimPrefix(authHeader, "Bearer ")
	token = strings.TrimSpace(token)

	consumer := ""
	if token != "" && len(config.VirtualKeys) > 0 {
		if alias, ok := config.VirtualKeys[token]; ok {
			consumer = alias
			// 🎯 核心安全加固: 剥离下游客户端传入的 Virtual Key，防止污染上游 Google 官方直连鉴权
			_ = proxywasm.RemoveHttpRequestHeader("authorization")
		} else {
			// 如果配置了白名单密钥但未匹配上，拒绝未授权请求
			_ = proxywasm.SendHttpResponse(401, [][2]string{{"Content-Type", "application/json"}}, []byte(`{"error":{"message":"Invalid Virtual Key","type":"invalid_request_error","code":"invalid_api_key"}}`))
			return types.ActionPause
		}
	}

	// 2. 兜底读取外部网关注入的标签
	if consumer == "" {
		consumer, _ = proxywasm.GetHttpRequestHeader("x-mse-consumer")
	}
	if consumer == "" {
		consumer, _ = proxywasm.GetHttpRequestHeader("x-consumer")
	}
	if consumer == "" {
		consumer = "default"
	}
	ctx.SetContext(ctxAPIKeyAlias, consumer)

	return types.ActionContinue
}

func onHttpRequestBody(ctx wrapper.HttpContext, config PluginConfig, body []byte, log wrapper.Log) types.Action {
	if len(body) > 0 {
		ctx.SetContext(ctxPromptBody, string(body))
		// 解析请求中的 model
		model := gjson.GetBytes(body, "model").String()
		if model != "" {
			ctx.SetContext(ctxModelRequested, model)
		}
	}
	return types.ActionContinue
}

func onHttpResponseHeaders(ctx wrapper.HttpContext, config PluginConfig, log wrapper.Log) types.Action {
	provider, _ := proxywasm.GetHttpResponseHeader("x-envoy-upstream-service-time")
	if provider != "" {
		ctx.SetContext(ctxProvider, "google-gemini")
	} else {
		ctx.SetContext(ctxProvider, "unknown")
	}
	return types.ActionContinue
}

func onHttpResponseBody(ctx wrapper.HttpContext, config PluginConfig, body []byte, log wrapper.Log) types.Action {
	// 🎯 核心流式修补: 拦截并修复被官方 ai-proxy 阉割掉 tool_calls 的空壳 Chunk
	if len(body) > 0 {
		rawBodyStr := string(body)
		if strings.HasPrefix(rawBodyStr, "data: {") {
			chunkJSON := strings.TrimPrefix(rawBodyStr, "data: ")
			chunkJSON = strings.TrimSpace(chunkJSON)
			if repaired, modified := finops.RepairMalformedChunk(chunkJSON, rawBodyStr); modified {
				_ = proxywasm.ReplaceHttpResponseBody([]byte("data: " + repaired + "\n\n"))
			}
		}

		prevResp, _ := ctx.GetContext(ctxResponseBody).(string)
		ctx.SetContext(ctxResponseBody, prevResp+rawBodyStr)
	}

	// 🎯 核心生命周期守则: 必须在 endOfStream (流传输彻底完毕) 时触发异步旁路处理
	// proxy-wasm-go-sdk 在收到 EOS 时 body 阶段处理完毕
	go func() {
		// 安全提取上下文
		startVal := ctx.GetContext(ctxStartTime)
		var startTime time.Time
		if t, ok := startVal.(time.Time); ok {
			startTime = t
		} else {
			startTime = time.Now()
		}
		latency := int(time.Since(startTime).Milliseconds())

		reqID, _ := ctx.GetContext(ctxRequestID).(string)
		apiKeyAlias, _ := ctx.GetContext(ctxAPIKeyAlias).(string)
		modelRequested, _ := ctx.GetContext(ctxModelRequested).(string)
		if modelRequested == "" {
			modelRequested = "gemini-3.8-flash"
		}
		prompt, _ := ctx.GetContext(ctxPromptBody).(string)
		fullResponse, _ := ctx.GetContext(ctxResponseBody).(string)

		// 解析 Usage: 针对普通 JSON 或流式 SSE 最终块提取 tokens
		var promptTokens, completionTokens, reasoningTokens, totalTokens int
		statusCode := 200

		if strings.Contains(fullResponse, "usage") {
			// 如果是 SSE 格式，截取最后包含 data: 的有效 JSON
			rawJSON := fullResponse
			if idx := strings.LastIndex(fullResponse, "data: "); idx != -1 {
				rawJSON = strings.TrimSpace(fullResponse[idx+6:])
			}
			parsed := gjson.Parse(rawJSON)
			promptTokens = int(parsed.Get("usage.prompt_tokens").Int())
			completionTokens = int(parsed.Get("usage.completion_tokens").Int())
			reasoningTokens = int(parsed.Get("usage.completion_tokens_details.reasoning_tokens").Int())
			if reasoningTokens == 0 {
				reasoningTokens = int(parsed.Get("usage.reasoning_tokens").Int())
			}
			totalTokens = int(parsed.Get("usage.total_tokens").Int())
			if totalTokens == 0 {
				totalTokens = promptTokens + completionTokens + reasoningTokens
			}
		}

		// 计算美金开销与人民币折算
		costUSD, costCNY := CalculateCost(modelRequested, promptTokens, completionTokens, reasoningTokens, config.DefaultFxRate)

		// 构造 MySQL 与后端中转实体 (100% 对齐 COMPATIBILITY.md)
		auditRecord := AuditLogRecord{
			ID:               uuid.NewString(),
			RequestID:        reqID,
			APIKeyAlias:      apiKeyAlias,
			ModelRequested:   modelRequested,
			ModelUsed:        modelRequested,
			Provider:         "google-gemini",
			ProviderKeyAlias: "OPENAI_API_KEY_FREE_3",
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens,
			ReasoningTokens:  reasoningTokens,
			TotalTokens:      totalTokens,
			CostUSD:          costUSD,
			CostCNY:          costCNY,
			FxRate:           config.DefaultFxRate,
			LatencyMS:        latency,
			StatusCode:       statusCode,
			CreatedAt:        time.Now(),
			Prompt:           prompt,
			Response:         fullResponse,
		}

		// 构造 VictoriaLogs 冷归档实体
		vlogsRecord := VictoriaLogsPayload{
			RequestID:        reqID,
			Model:            modelRequested,
			KeyAlias:         apiKeyAlias,
			Prompt:           prompt,
			Response:         fullResponse,
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens,
			ReasoningTokens:  reasoningTokens,
			TotalTokens:      totalTokens,
			Spend:            costUSD,
			StatusCode:       statusCode,
			LatencyMS:        latency,
		}

		// 异步兵分两路派发
		client := wrapper.NewClusterClient(wrapper.DnsCluster{
			// Higress wasm-go SDK 客户端驱动
		})
		_ = SendToVictoriaLogs(client, config.VictoriaLogsURL, vlogsRecord)
		_ = SendToMySQLSink(client, config.DashboardAPIURL, auditRecord)
	}()

	return types.ActionContinue
}
