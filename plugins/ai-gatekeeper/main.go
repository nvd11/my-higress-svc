package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm"
	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm/types"
	"github.com/higress-group/wasm-go/pkg/wrapper"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	pluginName = "ai-gatekeeper"

	ctxRequestID        = "request_id"
	ctxConsumer         = "consumer"
	ctxModelRequested   = "model_requested"
	ctxOriginalModel    = "original_model"
	ctxIsModelsEndpoint = "is_models_endpoint"

	maxBodyBufferSize = 50 * 1024 * 1024 // 50MB 缓冲上限 (R-1 阻塞防护)
)

func main() {}

func init() {
	wrapper.SetCtx[PluginConfig](
		pluginName,
		wrapper.ParseConfig(parseConfig),
		wrapper.ProcessRequestHeaders(onHttpRequestHeaders),
		wrapper.ProcessRequestBody(onHttpRequestBody),
	)
}

func parseConfig(json gjson.Result, config *PluginConfig) error {
	config.DashboardAPIURL = json.Get("dashboard_api_url").String()

	config.VirtualKeys = make(map[string]string)
	if vkeys := json.Get("virtual_keys"); vkeys.Exists() && vkeys.IsObject() {
		vkeys.ForEach(func(key, val gjson.Result) bool {
			config.VirtualKeys[key.String()] = val.String()
			return true
		})
	}

	config.CanonicalModels = nil
	if cmodels := json.Get("canonical_models"); cmodels.Exists() && cmodels.IsArray() {
		for _, m := range cmodels.Array() {
			config.CanonicalModels = append(config.CanonicalModels, m.String())
		}
	}
	if len(config.CanonicalModels) == 0 {
		config.CanonicalModels = defaultCanonicalModels
	}

	config.Aliases = make(map[string]string)
	if aliases := json.Get("aliases"); aliases.Exists() && aliases.IsObject() {
		aliases.ForEach(func(key, val gjson.Result) bool {
			config.Aliases[key.String()] = val.String()
			return true
		})
	}
	if len(config.Aliases) == 0 {
		config.Aliases = defaultAliases
	}

	config.UpstreamKeys = make(map[string]string)
	if ukeys := json.Get("upstream_keys"); ukeys.Exists() && ukeys.IsObject() {
		ukeys.ForEach(func(key, val gjson.Result) bool {
			config.UpstreamKeys[key.String()] = val.String()
			return true
		})
	}

	return nil
}

// cleanRequestID 清洗多跳代理产生的逗号拼接 x-request-id (M-1 规范)
func cleanRequestID(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return uuid.New().String()
	}
	if idx := strings.Index(raw, ","); idx != -1 {
		first := strings.TrimSpace(raw[:idx])
		if first != "" {
			return first
		}
	}
	return raw
}

// extractBearerToken 提取 Authorization: Bearer <Key>
func extractBearerToken(authHeader string) string {
	authHeader = strings.TrimSpace(authHeader)
	if len(authHeader) > 7 && strings.EqualFold(authHeader[:7], "Bearer ") {
		return strings.TrimSpace(authHeader[7:])
	}
	return authHeader
}

// setRequestHeader 幂等地设置请求头 (先 Remove 再 Add)。
// 不能用 proxywasm.ReplaceHttpRequestHeader: Envoy 的 replaceHeaderMapValue
// 当 key 不存在时返回 NotFound 且**不会创建**该 header, 导致注入静默失效。
func setRequestHeader(key, value string) error {
	// 先尝试删除已存在的同名头 (不存在会返回 NotFound, 忽略即可),
	// 保证上游只看到唯一一个由网关写入的值, 顺带防止客户端伪造。
	_ = proxywasm.RemoveHttpRequestHeader(key)
	return proxywasm.AddHttpRequestHeader(key, value)
}

// isModelInList 检查模型名是否在白名单切片中
func isModelInList(model string, list []string) bool {
	for _, m := range list {
		if m == model {
			return true
		}
	}
	return false
}

// validateAndResolveModel 检验模型准入并完成别名解析
func validateAndResolveModel(model string, canonicalModels []string, aliases map[string]string) (resolvedModel string, isAlias bool, allowed bool) {
	if canonical, ok := aliases[model]; ok {
		return canonical, true, true
	}
	if isModelInList(model, canonicalModels) {
		return model, false, true
	}
	return model, false, false
}

// buildModelsResponse 构造标准 OpenAI /v1/models JSON 响应
func buildModelsResponse(models []string) string {
	type modelItem struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	}
	type modelListResponse struct {
		Object string      `json:"object"`
		Data   []modelItem `json:"data"`
	}

	resp := modelListResponse{
		Object: "list",
		Data:   make([]modelItem, 0, len(models)),
	}
	for _, m := range models {
		resp.Data = append(resp.Data, modelItem{
			ID:      m,
			Object:  "model",
			Created: 1740000000,
			OwnedBy: "system",
		})
	}
	b, _ := json.Marshal(resp)
	return string(b)
}

// rewriteBodyModel 改写 JSON 请求体中的 model 字段
func rewriteBodyModel(body []byte, targetModel string) ([]byte, error) {
	return sjson.SetBytes(body, "model", targetModel)
}

// sendRejectRecord 异步发送阻断安全审计记录 (G7 安全可观测)
func sendRejectRecord(config PluginConfig, reqID, consumer, modelRequested string, statusCode int, errMsg string) {
	if config.DashboardAPIURL == "" {
		return
	}
	rec := GatekeeperRejectRecord{
		RequestID:        reqID,
		APIKeyAlias:      consumer,
		ModelRequested:   modelRequested,
		ModelUsed:        "none",
		Provider:         "higress-gatekeeper",
		ProviderKeyAlias: "gatekeeper",
		StatusCode:       statusCode,
		ErrorMsg:         &errMsg,
		CreatedAt:        time.Now(),
	}
	dashClient := wrapper.NewClusterClient(wrapper.DnsCluster{
		ServiceName: "dashboard-backend",
		Domain:      "higress-dashboard-backend.higress-system.svc.cluster.local",
		Port:        4000,
	})
	_ = SendRejectSink(dashClient, config.DashboardAPIURL, rec)
}

func onHttpRequestHeaders(ctx wrapper.HttpContext, config PluginConfig) types.Action {
	method, _ := proxywasm.GetHttpRequestHeader(":method")
	if strings.EqualFold(method, "OPTIONS") {
		return types.ActionContinue
	}
	path, _ := proxywasm.GetHttpRequestHeader(":path")

	// 1. 声明 50MB 缓冲上限 (R-1 阻塞防护，杜绝 413)
	ctx.SetRequestBodyBufferLimit(maxBodyBufferSize)

	// 2. 清洗多跳 x-request-id (M-1 彻底闭环)
	reqID, _ := proxywasm.GetHttpRequestHeader("x-request-id")
	cleanReqID := cleanRequestID(reqID)
	if reqID != cleanReqID {
		_ = proxywasm.ReplaceHttpRequestHeader("x-request-id", cleanReqID)
	}
	if reqID == "" {
		_ = proxywasm.AddHttpRequestHeader("x-request-id", cleanReqID)
	}
	ctx.SetContext(ctxRequestID, cleanReqID)

	// 3. Virtual Keys 强鉴权 (FAIL_CLOSE: 空配置默认一票否决)
	if len(config.VirtualKeys) == 0 {
		errMsg := "Authentication unconfigured"
		sendRejectRecord(config, cleanReqID, "unknown", "", 401, errMsg)
		_ = proxywasm.SendHttpResponse(401, [][2]string{
			{"Content-Type", "application/json"},
			{"x-request-id", cleanReqID},
		}, []byte(`{"error":{"message":"Authentication unconfigured","type":"invalid_request_error","code":"unauthorized"}}`), -1)
		return types.ActionPause
	}

	authHeader, _ := proxywasm.GetHttpRequestHeader("authorization")
	token := extractBearerToken(authHeader)
	consumer, ok := config.VirtualKeys[token]
	if !ok || consumer == "" {
		errMsg := "Invalid or missing API key"
		sendRejectRecord(config, cleanReqID, "unknown", "", 401, errMsg)
		_ = proxywasm.SendHttpResponse(401, [][2]string{
			{"Content-Type", "application/json"},
			{"x-request-id", cleanReqID},
		}, []byte(`{"error":{"message":"Invalid or missing API key","type":"invalid_request_error","code":"unauthorized"}}`), -1)
		return types.ActionPause
	}

	// 鉴权通过: 注入 x-consumer (同样需 Remove+Add, 客户端不会自带该头)
	if err := setRequestHeader("x-consumer", consumer); err != nil {
		proxywasm.LogErrorf("[ai-gatekeeper] inject x-consumer failed: %v", err)
	}
	ctx.SetContext(ctxConsumer, consumer)

	// 4. /v1/models 本地直答拦截
	basePath := path
	if qIdx := strings.Index(basePath, "?"); qIdx != -1 {
		basePath = basePath[:qIdx]
	}
	if (basePath == "/v1/models" || basePath == "/models") && strings.EqualFold(method, "GET") {
		ctx.SetContext(ctxIsModelsEndpoint, true)
		modelsJSON := buildModelsResponse(config.CanonicalModels)
		_ = proxywasm.SendHttpResponse(200, [][2]string{
			{"Content-Type", "application/json"},
			{"x-request-id", cleanReqID},
			{"x-consumer", consumer},
		}, []byte(modelsJSON), -1)
		return types.ActionPause
	}

	// 5. 携带请求体的请求（如 /v1/chat/completions）：
	// 必须在 Header 阶段返回 ActionPause (对应 Envoy FilterHeadersStatus::StopIteration)，
	// 阻止 Envoy 在 decodeHeaders 完成后执行 `request_headers_ = nullptr`。
	// 只有保留 request_headers_ 句柄，后续 onHttpRequestBody 中才能合法修改
	// Authorization 与 x-higress-llm-* 请求头，避免触发 host "bad argument" 致命错误。
	// 在 onHttpRequestBody 完成请求头注入与路由重算后，调用 ResumeHttpRequest() 恢复流处理。
	if ctx.HasRequestBody() {
		return types.ActionPause
	}

	return types.ActionContinue
}

func onHttpRequestBody(ctx wrapper.HttpContext, config PluginConfig, body []byte) types.Action {
	if isModels, _ := ctx.GetContext(ctxIsModelsEndpoint).(bool); isModels {
		return types.ActionContinue
	}
	if len(body) == 0 {
		_ = proxywasm.ResumeHttpRequest()
		return types.ActionContinue
	}

	reqID, _ := ctx.GetContext(ctxRequestID).(string)
	consumer, _ := ctx.GetContext(ctxConsumer).(string)

	// 解析请求体中的 model
	modelResult := gjson.GetBytes(body, "model")
	if !modelResult.Exists() || strings.TrimSpace(modelResult.String()) == "" {
		errMsg := "Missing required 'model' parameter"
		sendRejectRecord(config, reqID, consumer, "", 400, errMsg)
		_ = proxywasm.SendHttpResponse(400, [][2]string{
			{"Content-Type", "application/json"},
			{"x-request-id", reqID},
			{"x-consumer", consumer},
		}, []byte(`{"error":{"message":"Missing required 'model' parameter","type":"invalid_request_error","param":"model","code":"parameter_missing"}}`), -1)
		return types.ActionPause
	}

	model := strings.TrimSpace(modelResult.String())

	// 5. 模型准入查验 (22 键严格白名单)
	resolvedModel, isAlias, allowed := validateAndResolveModel(model, config.CanonicalModels, config.Aliases)
	if !allowed {
		errMsg := fmt.Sprintf("The model '%s' does not exist or you do not have access to it.", model)
		sendRejectRecord(config, reqID, consumer, model, 400, errMsg)
		respBody := fmt.Sprintf(`{"error":{"message":"%s","type":"invalid_request_error","param":"model","code":"model_not_found"}}`, errMsg)
		_ = proxywasm.SendHttpResponse(400, [][2]string{
			{"Content-Type", "application/json"},
			{"x-request-id", reqID},
			{"x-consumer", consumer},
		}, []byte(respBody), -1)
		return types.ActionPause
	}

	// 6. 别名改写与原始名跟踪 (方案 A 模式)
	if isAlias {
		newBody, err := rewriteBodyModel(body, resolvedModel)
		if err == nil {
			_ = proxywasm.ReplaceHttpRequestBody(newBody)
		}
	}

	// 注入原始模型名头与上游路由提供商头 (供 Envoy Ingress 路由及财务审计追踪)
	provider := defaultModelProviders[resolvedModel]
	if provider == "" {
		provider = "a6api"
	}
	// ⚠️ 必须用 Remove + Add: Envoy 的 replaceHeaderMapValue 在 key 不存在时返回
	// NotFound 且不会创建 header。客户端请求里天然没有 x-higress-llm-* 这三个头,
	// 若用 Replace 则注入全部静默失败, provider header 缺失导致 Envoy 永远命中
	// 默认 catch-all 路由 (openai-proxy-for-antigravity), A6 模型会被错误中转。
	if err := setRequestHeader("x-original-model", model); err != nil {
		proxywasm.LogErrorf("[ai-gatekeeper] inject x-original-model failed: %v", err)
	}
	if err := setRequestHeader("x-higress-llm-model", resolvedModel); err != nil {
		proxywasm.LogErrorf("[ai-gatekeeper] inject x-higress-llm-model failed: %v", err)
	}
	if err := setRequestHeader("x-higress-llm-provider", provider); err != nil {
		proxywasm.LogErrorf("[ai-gatekeeper] inject x-higress-llm-provider failed: %v", err)
	}
	// 回读校验: 确认 header 真的落表, 作为路由是否生效的唯一可信证据
	if got, err := proxywasm.GetHttpRequestHeader("x-higress-llm-provider"); err != nil || got != provider {
		proxywasm.LogErrorf("[ai-gatekeeper] provider header verify FAILED, want=%s got=%q err=%v", provider, got, err)
	} else {
		proxywasm.LogWarnf("[ai-gatekeeper] route provider=%s injected & verified", provider)
	}
	// 清空路由缓存以触发 Envoy 重新选路 (Higress 默认开启 body 阶段后 reroute)
	if _, err := proxywasm.CallForeignFunction("clear_route_cache", nil); err != nil {
		proxywasm.LogWarnf("[ai-gatekeeper] clear_route_cache returned: %v", err)
	}
	// 上游鉴权: 该 provider 若配置了真实 Key, 用它替换客户端的 Virtual Key。
	// 这样 A6 / 元衡 等商业渠道可以被网关**直连**, 既不需要额外中转,
	// 也不需要依赖 Higress header-control 注解 (该插件在 higress-core
	// 无 Console 部署里默认未启用, 注解会被静默忽略)。
	if rawKey := strings.TrimSpace(config.UpstreamKeys[provider]); rawKey != "" {
		if strings.HasPrefix(rawKey, "__") {
			errMsg := fmt.Sprintf("provider '%s' 的上游凭证尚未注入, 请等待 Secret 同步 Job 完成", provider)
			proxywasm.LogErrorf("[ai-gatekeeper] %s", errMsg)
			sendRejectRecord(config, reqID, consumer, model, 503, errMsg)
			_ = proxywasm.SendHttpResponse(503, [][2]string{
				{"Content-Type", "application/json"},
				{"x-request-id", reqID},
				{"x-consumer", consumer},
			}, []byte(`{"error":{"message":"`+errMsg+`","type":"server_error","code":"upstream_credential_missing"}}`), -1)
			return types.ActionPause
		}
		if err := setRequestHeader("authorization", "Bearer "+rawKey); err != nil {
			proxywasm.LogErrorf("[ai-gatekeeper] inject upstream authorization for %s failed: %v", provider, err)
			sendRejectRecord(config, reqID, consumer, model, 500, "upstream credential injection failed")
			_ = proxywasm.SendHttpResponse(500, [][2]string{
				{"Content-Type", "application/json"},
				{"x-request-id", reqID},
				{"x-consumer", consumer},
			}, []byte(`{"error":{"message":"upstream credential injection failed","type":"server_error","code":"upstream_credential_inject_failed"}}`), -1)
			return types.ActionPause
		}
	}

	ctx.SetContext(ctxOriginalModel, model)
	ctx.SetContext(ctxModelRequested, resolvedModel)

	// 恢复在 onHttpRequestHeaders 中暂停的 HTTP 请求解码流
	_ = proxywasm.ResumeHttpRequest()
	return types.ActionContinue
}
