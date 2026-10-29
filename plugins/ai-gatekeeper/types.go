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
	// provider -> 上游真实 API Key (由 higress-provider-key-sync Job 从 Secret 回填)。
	// 网关据此按 provider 注入 Authorization, 使 A6 / 元衡 等渠道可被直连,
	// 无需依赖 Higress 的 header-control 注解插件 (该插件在 higress-core 无 Console
	// 部署中默认未启用, 注解会被静默忽略)。
	UpstreamKeys map[string]string `json:"upstream_keys"`
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
