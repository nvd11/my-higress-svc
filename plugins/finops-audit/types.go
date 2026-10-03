package main

import (
	"time"

	"github.com/nvd11/my-higress-svc/plugins/finops-audit/pkg/finops"
)

// PluginConfig 插件全局配置 (从 WasmPlugin CRD defaultConfig 解析)
type PluginConfig struct {
	// VictoriaLogs HTTP 端点 (例如 http://10.0.1.227:9428/insert/jsonline)
	VictoriaLogsURL string `json:"victoria_logs_url"`
	// Dashboard-API 内部写入端点 (例如 http://my-higress-dashboard-backend.higress-system.svc:4000/api/v1/internal/audit-log)
	DashboardAPIURL string `json:"dashboard_api_url"`
	// 结算兜底汇率 (默认 7.2300)
	DefaultFxRate float64 `json:"default_fx_rate"`
	// Virtual Key 到 api_key_alias 的映射字典 (用于微秒级鉴权与身份识别)
	VirtualKeys map[string]string `json:"virtual_keys"`
}

// AuditLogRecord 对应 MySQL llm_request_logs 表结构的完整实体 (100% 兼容旧系统)
type AuditLogRecord struct {
	ID               string    `json:"id"`
	RequestID        string    `json:"request_id"`
	APIKeyAlias      string    `json:"api_key_alias"`
	ModelRequested   string    `json:"model_requested"`
	ModelUsed        string    `json:"model_used"`
	Provider         string    `json:"provider"`
	ProviderKeyAlias string    `json:"provider_key_alias"`
	PromptTokens     int       `json:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens"`
	ReasoningTokens  int       `json:"reasoning_tokens"`
	TotalTokens      int       `json:"total_tokens"`
	CostUSD          float64   `json:"cost_usd"`
	CostCNY          float64   `json:"cost_cny"`
	FxRate           float64   `json:"fx_rate"`
	LatencyMS        int       `json:"latency_ms"`
	StatusCode       int       `json:"status_code"`
	ErrorMsg         string    `json:"error_msg"`
	CreatedAt        time.Time `json:"created_at"`
	// 携带原始报文供给 Dashboard-API 写入 Redis 与 VictoriaLogs
	Prompt   string `json:"prompt,omitempty"`
	Response string `json:"response,omitempty"`
}

// CalculateCost 封装调用核心纯算费逻辑
func CalculateCost(model string, promptTokens, completionTokens, reasoningTokens int, fxRate float64) (costUSD float64, costCNY float64) {
	return finops.CalculateCost(model, promptTokens, completionTokens, reasoningTokens, fxRate)
}
