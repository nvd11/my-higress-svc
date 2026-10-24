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

// 默认 10 个标准主名 (防卫配置缺失时的兜底)
var defaultCanonicalModels = []string{
	"gemini-4-argon",
	"gemini-4-argon-search",
	"gemini-3.8-flash",
	"gemini-3.8-flash-search",
	"kimi-k3",
	"glm-5.3",
	"hy3",
	"hy4-preview",
	"gpt-5.6-luna",
	"deepseek-v4-pro-0813",
}

// 默认 12 个别名映射 (防卫配置缺失时的兜底)
var defaultAliases = map[string]string{
	"gemini-4":                "gemini-4-argon",
	"gemini-4-flash":          "gemini-4-argon",
	"gemini-4:search":         "gemini-4-argon-search",
	"gemini-4-search":         "gemini-4-argon-search",
	"gemini-3.8":              "gemini-3.8-flash",
	"gemini-2.5-flash":        "gemini-3.8-flash",
	"gemini-3.8-flash:search": "gemini-3.8-flash-search",
	"kimi":                    "kimi-k3",
	"glm":                     "glm-5.3",
	"hunyuan3":                "hy3",
	"luna":                    "gpt-5.6-luna",
	"gpt-5.6-luna-a6":         "gpt-5.6-luna",
}

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

	// 鉴权通过: 注入 x-consumer
	_ = proxywasm.ReplaceHttpRequestHeader("x-consumer", consumer)
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

	return types.ActionContinue
}

func onHttpRequestBody(ctx wrapper.HttpContext, config PluginConfig, body []byte) types.Action {
	if isModels, _ := ctx.GetContext(ctxIsModelsEndpoint).(bool); isModels {
		return types.ActionContinue
	}
	if len(body) == 0 {
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

	// 注入原始模型名头 (方便下游审计与追踪)
	_ = proxywasm.ReplaceHttpRequestHeader("x-original-model", model)
	ctx.SetContext(ctxOriginalModel, model)
	ctx.SetContext(ctxModelRequested, resolvedModel)

	return types.ActionContinue
}
