package main

import "time"

// PluginConfig 插件全局配置 (从 WasmPlugin CRD defaultConfig 解析)
type PluginConfig struct {
	// Dashboard-API 内部写入端点 (用于 G7 阻断审计留痕)
	DashboardAPIURL string `json:"dashboard_api_url"`
	// Virtual Key 到 api_key_alias 的映射字典 (微秒级鉴权与身份识别)
	VirtualKeys map[string]string `json:"virtual_keys"`
	// 允许放行的标准主名列表 (默认 10 个主名)
	CanonicalModels []string `json:"canonical_models"`
	// 允许放行的别名及向上游真名的映射字典 (默认 12 个别名)
	Aliases map[string]string `json:"aliases"`
}

// GatekeeperRejectRecord 对应 401/400 阻断留痕送入 Dashboard-API 的数据结构
type GatekeeperRejectRecord struct {
	RequestID        string    `json:"request_id"`
	APIKeyAlias      string    `json:"api_key_alias"`
	ModelRequested   string    `json:"model_requested"`
	ModelUsed        string    `json:"model_used"`
	Provider         string    `json:"provider"`
	ProviderKeyAlias string    `json:"provider_key_alias"`
	StatusCode       int       `json:"status_code"`
	ErrorMsg         *string   `json:"error_msg"`
	CreatedAt        time.Time `json:"created_at"`
	Prompt           string    `json:"prompt,omitempty"`
	Response         string    `json:"response,omitempty"`
}
