package finops

import (
	"math"
	"strings"
)

// ModelPricing 单个模型的计费口径。
//
// 唯一来源是仓库根目录 config.yaml 的 model_list —— 本文件不含任何价格字面量,
// 价目表由 internal/pricinggen 在构建期生成到 pricing_gen.go。
// 单位: 美元 / 1M tokens。
type ModelPricing struct {
	PromptPer1M     float64
	CompletionPer1M float64
	CacheReadPer1M  float64
	// Provider config.yaml 声明的上游渠道标识 (a6api / google-gemini ...)
	Provider string
	// ProviderKey config.yaml 中 api_key 的环境变量别名 (A6_API_KEY ...)
	ProviderKey string
	// Canonical 别名归一后的模型主名
	Canonical string
	// UpstreamModel 实际转发给上游的模型名
	UpstreamModel string
}

// GetModelPricing 按 config.yaml 声明的模型主名或别名精确匹配。
//
// 未声明返回 false。调用方必须显式处理未纳管模型 —— 这里不再提供任何
// "猜一个模型的价目" 的兜底逻辑。
func GetModelPricing(model string) (ModelPricing, bool) {
	p, ok := modelPricing[model]
	return p, ok
}

// DeclaredModels 返回 config.yaml 中显式声明的模型主名 (按文件顺序)。
func DeclaredModels() []string {
	out := make([]string, len(declaredModels))
	copy(out, declaredModels)
	return out
}

// CalculateCost 按 config.yaml 的三段价目计算开销。
//
//	cost = 未缓存输入 × 输入价 + 缓存命中 × 缓存价 + 输出 × 输出价
//
// 说明:
//   - cacheReadTokens 是 promptTokens 的子集; 超出部分按 0 处理, 避免负输入;
//   - reasoning tokens 已包含在 completionTokens 内 (上游 usage 语义),
//     因此不单独计费, 否则重复收费;
//   - 模型未在 config.yaml 声明时返回 declared=false 且开销为 0, 由调用方决定
//     如何标记, 绝不套用其它模型的价格。
func CalculateCost(model string, promptTokens, completionTokens, cacheReadTokens int, fxRate float64) (costUSD, costCNY float64, declared bool) {
	pricing, ok := modelPricing[model]
	if !ok {
		return 0, 0, false
	}

	if cacheReadTokens < 0 {
		cacheReadTokens = 0
	}
	if cacheReadTokens > promptTokens {
		cacheReadTokens = promptTokens
	}
	billablePrompt := promptTokens - cacheReadTokens

	costUSD = float64(billablePrompt)/1_000_000*pricing.PromptPer1M +
		float64(cacheReadTokens)/1_000_000*pricing.CacheReadPer1M +
		float64(completionTokens)/1_000_000*pricing.CompletionPer1M
	costUSD = math.Round(costUSD*1_000_000) / 1_000_000
	costCNY = math.Round(costUSD*fxRate*1_000_000) / 1_000_000
	return costUSD, costCNY, true
}

// ResolveVirtualKey 校验 Token 并解析出对应的租户标识 (api_key_alias)
func ResolveVirtualKey(authHeader string, virtualKeys map[string]string) (alias string, authorized bool) {
	if len(virtualKeys) == 0 {
		return "default", true
	}

	token := strings.TrimPrefix(authHeader, "Bearer ")
	token = strings.TrimSpace(token)
	if token == "" {
		return "", false
	}

	if a, ok := virtualKeys[token]; ok {
		return a, true
	}
	return "", false
}
