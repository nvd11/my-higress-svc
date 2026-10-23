package finops

import (
	"math"
	"strings"
)

// ModelPricing 单个模型每 1M Tokens 计价 (美金)
type ModelPricing struct {
	PromptPer1M     float64
	CompletionPer1M float64
	ReasoningPer1M  float64
}

// DefaultPricingMatrix 官方核算单价矩阵 (严格对齐 docs/COMPATIBILITY.md 第 4.2 节)
var DefaultPricingMatrix = map[string]ModelPricing{
	// 🟢 Google Gemini 原生直连
	"gemini-3.8-flash": {PromptPer1M: 0.75, CompletionPer1M: 3.75, ReasoningPer1M: 3.75},
	// 🛡️ A6 API 中转渠道 (5折保底)
	"gemini-3.8-backup": {PromptPer1M: 0.375, CompletionPer1M: 1.875, ReasoningPer1M: 1.875},
	"gemini-4-argon":    {PromptPer1M: 2.0, CompletionPer1M: 10.0, ReasoningPer1M: 10.0},
	// 🌙 推理与编程模型组 (已折算为美金或特惠价)
	"deepseek-v4-pro-0813": {PromptPer1M: 0.00096, CompletionPer1M: 0.00096, ReasoningPer1M: 0.00096},
	"kimi-k3":               {PromptPer1M: 0.010595, CompletionPer1M: 0.052974, ReasoningPer1M: 0.052974},
	"glm-5.3":               {PromptPer1M: 0.0102, CompletionPer1M: 0.032057, ReasoningPer1M: 0.032057},
	"gpt-5.6-luna-a6":       {PromptPer1M: 0.0096, CompletionPer1M: 0.0096, ReasoningPer1M: 0.0096},
	"gpt-5.6-luna-yuanheng": {PromptPer1M: 0.0373, CompletionPer1M: 0.0373, ReasoningPer1M: 0.0373},
	// 🎀 自家 Hermes Agent 免费
	"yui": {PromptPer1M: 0.0, CompletionPer1M: 0.0, ReasoningPer1M: 0.0},
	"rin": {PromptPer1M: 0.0, CompletionPer1M: 0.0, ReasoningPer1M: 0.0},
}

// GetModelPricing 返回指定模型的单价
func GetModelPricing(model string) ModelPricing {
	pricing, ok := DefaultPricingMatrix[model]
	if !ok {
		pricing = DefaultPricingMatrix["gemini-3.8-flash"]
	}
	return pricing
}

// CalculateCost 财务计费核心算法: 计算美金开销与折合人民币开销
func CalculateCost(model string, promptTokens, completionTokens, reasoningTokens int, fxRate float64) (costUSD float64, costCNY float64) {
	pricing, ok := DefaultPricingMatrix[model]
	if !ok {
		pricing = DefaultPricingMatrix["gemini-3.8-flash"]
	}

	pCost := (float64(promptTokens) / 1000000.0) * pricing.PromptPer1M
	cCost := (float64(completionTokens) / 1000000.0) * pricing.CompletionPer1M
	rCost := (float64(reasoningTokens) / 1000000.0) * pricing.ReasoningPer1M

	costUSD = math.Round((pCost+cCost+rCost)*1000000) / 1000000
	costCNY = math.Round((costUSD*fxRate)*1000000) / 1000000
	return costUSD, costCNY
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
