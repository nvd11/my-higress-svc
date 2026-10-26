package main

import (
	"strconv"
	"strings"

	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm"
	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm/types"
	"github.com/higress-group/wasm-go/pkg/wrapper"
	"github.com/nvd11/my-higress-svc/plugins/finops-audit/pkg/finops"
	"github.com/tidwall/gjson"
)

const (
	pluginName = "finops-audit"

	ctxRequestID      = "request_id"
	ctxAPIKeyAlias    = "api_key_alias"
	ctxModelRequested = "model_requested"
	ctxPromptBody     = "prompt_body"
	ctxResponseBody   = "response_body"
	ctxProvider       = "provider"
	ctxStatusCode     = "status_code"
)

func main() {}

func init() {
	wrapper.SetCtx[PluginConfig](
		pluginName,
		wrapper.ParseConfig(parseConfig),
		wrapper.ProcessRequestHeaders(onHttpRequestHeaders),
		wrapper.ProcessRequestBody(onHttpRequestBody),
		wrapper.ProcessResponseHeaders(onHttpResponseHeaders),
		wrapper.ProcessStreamingResponseBody(onHttpStreamingResponseBody),
	)
}

func parseConfig(json gjson.Result, config *PluginConfig) error {
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

func onHttpRequestHeaders(ctx wrapper.HttpContext, config PluginConfig) types.Action {
	// 放行 OPTIONS 跨域预检请求与基础健康检查
	method, _ := proxywasm.GetHttpRequestHeader(":method")
	if strings.EqualFold(method, "OPTIONS") {
		return types.ActionContinue
	}
	path, _ := proxywasm.GetHttpRequestHeader(":path")
	if path == "/health" || path == "/healthz" || path == "/ping" {
		return types.ActionContinue
	}

	// 允许超大长文本上下文（支持高达 50MB 请求体，彻底根治 100k+ 超长 Token 413 Payload Too Large）
	ctx.SetRequestBodyBufferLimit(50 * 1024 * 1024)

	reqID, _ := proxywasm.GetHttpRequestHeader("x-request-id")
	forwarded, _ := proxywasm.GetHttpRequestHeader("x-forwarded-for")
	userAgent, _ := proxywasm.GetHttpRequestHeader("user-agent")

	// 用请求自带熵播种 PRNG: Wasm 沙箱没有系统随机源, 固定种子会在模块
	// 重新实例化后重放同一串 ID (历史事故: PRIMARY KEY 冲突导致丢单)
	finops.Seed(reqID, forwarded, userAgent)
	if reqID == "" {
		reqID = finops.GenerateRequestID(forwarded + "|" + userAgent)
	}
	ctx.SetContext(ctxRequestID, reqID)

	// 1. 从 Authorization: Bearer <TOKEN> 或 api-key / x-api-key 匹配 VirtualKeys 字典
	authHeader, _ := proxywasm.GetHttpRequestHeader("authorization")
	token := strings.TrimPrefix(authHeader, "Bearer ")
	token = strings.TrimSpace(token)
	if token == "" {
		token, _ = proxywasm.GetHttpRequestHeader("api-key")
		token = strings.TrimSpace(token)
	}
	if token == "" {
		token, _ = proxywasm.GetHttpRequestHeader("x-api-key")
		token = strings.TrimSpace(token)
	}

	consumer := ""
	if len(config.VirtualKeys) > 0 {
		if token == "" {
			// 未携带任何认证 Token，严防匿名免鉴权穿透
			_ = proxywasm.SendHttpResponse(401, [][2]string{{"Content-Type", "application/json"}}, []byte(`{"error":{"message":"Missing Bearer Token or API Key","type":"invalid_request_error","code":"unauthorized"}}`), -1)
			return types.ActionPause
		}
		if alias, ok := config.VirtualKeys[token]; ok {
			consumer = alias
		} else {
			// 如果配置了白名单密钥但未匹配上，拒绝未授权请求
			_ = proxywasm.SendHttpResponse(401, [][2]string{{"Content-Type", "application/json"}}, []byte(`{"error":{"message":"Invalid Virtual Key","type":"invalid_request_error","code":"invalid_api_key"}}`), -1)
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

func onHttpRequestBody(ctx wrapper.HttpContext, config PluginConfig, body []byte) types.Action {
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

// parseStatusCode parses HTTP status string into int, defaulting to 200
func parseStatusCode(statusStr string) int {
	if statusStr == "" {
		return 200
	}
	if code, err := strconv.Atoi(statusStr); err == nil && code > 0 {
		return code
	}
	return 200
}

// extractErrorMsg parses error message from response body when statusCode >= 400
func extractErrorMsg(statusCode int, fullResponse string) *string {
	if statusCode < 400 || fullResponse == "" {
		return nil
	}
	errMsg := fullResponse
	if gjson.Valid(fullResponse) {
		if msg := gjson.Get(fullResponse, "error.message").String(); msg != "" {
			errMsg = msg
		} else if msg := gjson.Get(fullResponse, "message").String(); msg != "" {
			errMsg = msg
		}
	}
	if len(errMsg) > 1024 {
		errMsg = errMsg[:1024]
	}
	return &errMsg
}

// fallbackModelName extracts model name from response JSON or defaults to "unknown"
func fallbackModelName(fullResponse string) string {
	if fullResponse != "" && gjson.Valid(fullResponse) {
		if respModel := gjson.Get(fullResponse, "model").String(); respModel != "" {
			return respModel
		}
	}
	return "unknown"
}

// resolveRequestedModel cascades through ctx, request headers, response body, or defaults to "unknown"
func resolveRequestedModel(ctx wrapper.HttpContext, fullResponse string) string {
	if m, _ := ctx.GetContext(ctxModelRequested).(string); m != "" {
		return m
	}
	if origModel, err := proxywasm.GetHttpRequestHeader("x-original-model"); err == nil && origModel != "" {
		return origModel
	}
	if llmModel, err := proxywasm.GetHttpRequestHeader("x-higress-llm-model"); err == nil && llmModel != "" {
		return llmModel
	}
	return fallbackModelName(fullResponse)
}

func onHttpResponseHeaders(ctx wrapper.HttpContext, config PluginConfig) types.Action {
	statusStr, _ := proxywasm.GetHttpResponseHeader(":status")
	statusCode := parseStatusCode(statusStr)
	ctx.SetContext(ctxStatusCode, statusCode)

	provider, _ := proxywasm.GetHttpResponseHeader("x-envoy-upstream-service-time")
	if provider != "" {
		ctx.SetContext(ctxProvider, "google-gemini")
	} else {
		ctx.SetContext(ctxProvider, "unknown")
	}

	// 提前补位: 若 Body 阶段未成功捕获模型 (如 4xx/5xx 提前终止)，尝试从请求头打捞网关门禁标记
	if m, _ := ctx.GetContext(ctxModelRequested).(string); m == "" {
		if origModel, err := proxywasm.GetHttpRequestHeader("x-original-model"); err == nil && origModel != "" {
			ctx.SetContext(ctxModelRequested, origModel)
		} else if llmModel, err := proxywasm.GetHttpRequestHeader("x-higress-llm-model"); err == nil && llmModel != "" {
			ctx.SetContext(ctxModelRequested, llmModel)
		}
	}

	return types.ActionContinue
}

func onHttpStreamingResponseBody(ctx wrapper.HttpContext, config PluginConfig, chunk []byte, isLastChunk bool) []byte {
	if len(chunk) > 0 {
		rawChunk := string(chunk)
		prev, _ := ctx.GetContext(ctxResponseBody).(string)
		ctx.SetContext(ctxResponseBody, prev+rawChunk)
	}

	if isLastChunk {
		fullResponse, _ := ctx.GetContext(ctxResponseBody).(string)
		latency := 0
		if val, err := proxywasm.GetHttpResponseHeader("x-envoy-upstream-service-time"); err == nil && val != "" {
			if ms, err := strconv.Atoi(val); err == nil {
				latency = ms
			}
		}

		reqID, _ := ctx.GetContext(ctxRequestID).(string)
		apiKeyAlias, _ := ctx.GetContext(ctxAPIKeyAlias).(string)
		modelRequested := resolveRequestedModel(ctx, fullResponse)
		prompt, _ := ctx.GetContext(ctxPromptBody).(string)

		var promptTokens, completionTokens, reasoningTokens, cacheReadTokens, totalTokens int
		statusCode := 200
		if sc, ok := ctx.GetContext(ctxStatusCode).(int); ok && sc > 0 {
			statusCode = sc
		}

		errorMsg := extractErrorMsg(statusCode, fullResponse)

		if strings.Contains(fullResponse, "usage") {
			rawJSON := fullResponse
			lines := strings.Split(fullResponse, "\n")
			for i := len(lines) - 1; i >= 0; i-- {
				line := strings.TrimSpace(lines[i])
				if strings.HasPrefix(line, "data: ") && strings.Contains(line, "usage") {
					rawJSON = strings.TrimPrefix(line, "data: ")
					break
				}
			}
			parsed := gjson.Parse(rawJSON)
			promptTokens = int(parsed.Get("usage.prompt_tokens").Int())
			completionTokens = int(parsed.Get("usage.completion_tokens").Int())
			reasoningTokens = int(parsed.Get("usage.completion_tokens_details.reasoning_tokens").Int())
			if reasoningTokens == 0 {
				reasoningTokens = int(parsed.Get("usage.reasoning_tokens").Int())
			}
			cacheReadTokens = int(parsed.Get("usage.prompt_tokens_details.cached_tokens").Int())
			totalTokens = int(parsed.Get("usage.total_tokens").Int())
			if totalTokens == 0 {
				totalTokens = promptTokens + completionTokens + reasoningTokens
			}
		}

		// 价目与渠道元数据全部来自 config.yaml (构建期生成), 未声明的模型不做价格兜底
		pricing, declared := finops.GetModelPricing(modelRequested)
		costUSD, costCNY, _ := finops.CalculateCost(modelRequested, promptTokens, completionTokens, cacheReadTokens, config.DefaultFxRate)

		provider, providerKeyAlias := "unknown", "unknown"
		modelUsed := modelRequested
		if declared {
			provider = pricing.Provider
			providerKeyAlias = pricing.ProviderKey
			modelUsed = pricing.UpstreamModel
		}

		// 行主键由 Dashboard-API 依据 (request_id, created_at) 派生, 插件不再生成随机 ID
		auditRecord := AuditLogRecord{
			RequestID:              reqID,
			APIKeyAlias:            apiKeyAlias,
			ModelRequested:         modelRequested,
			ModelUsed:              modelUsed,
			Provider:               provider,
			ProviderKeyAlias:       providerKeyAlias,
			PromptTokens:           promptTokens,
			CompletionTokens:       completionTokens,
			ReasoningTokens:        reasoningTokens,
			CacheReadTokens:        cacheReadTokens,
			TotalTokens:            totalTokens,
			PromptUnitPriceUSD:     pricing.PromptPer1M,
			CacheUnitPriceUSD:      pricing.CacheReadPer1M,
			CompletionUnitPriceUSD: pricing.CompletionPer1M,
			CostUSD:                costUSD,
			CostCNY:                costCNY,
			FxRate:                 config.DefaultFxRate,
			LatencyMS:              latency,
			StatusCode:             statusCode,
			ErrorMsg:               errorMsg,
			Prompt:                 prompt,
			Response:               fullResponse,
		}

		dashClient := wrapper.NewClusterClient(wrapper.DnsCluster{
			ServiceName: "dashboard-backend",
			Domain:      "higress-dashboard-backend.higress-system.svc.cluster.local",
			Port:        4000,
		})
		_ = SendToMySQLSink(dashClient, config.DashboardAPIURL, auditRecord)
	}

	return chunk
}
