package finops

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"math"
	"regexp"
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
	// 🌙 推理与编程模型组 (已折算为美金或特惠价)
	"kimi-k3":               {PromptPer1M: 0.010595, CompletionPer1M: 0.052974, ReasoningPer1M: 0.052974},
	"glm-5.3":               {PromptPer1M: 0.0102, CompletionPer1M: 0.032057, ReasoningPer1M: 0.032057},
	"gpt-5.6-luna-a6":       {PromptPer1M: 0.0096, CompletionPer1M: 0.0096, ReasoningPer1M: 0.0096},
	"gpt-5.6-luna-yuanheng": {PromptPer1M: 0.0373, CompletionPer1M: 0.0373, ReasoningPer1M: 0.0373},
	// 🎀 自家 Hermes Agent 免费
	"yui": {PromptPer1M: 0.0, CompletionPer1M: 0.0, ReasoningPer1M: 0.0},
	"rin": {PromptPer1M: 0.0, CompletionPer1M: 0.0, ReasoningPer1M: 0.0},
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

var base64ImageRegex = regexp.MustCompile(`data:image/[a-zA-Z0-9\.\+-]+;base64,([A-Za-z0-9+/=]{64,})`)

// CollapseBase64Images 正则折叠报文中的 Base64 图片
func CollapseBase64Images(raw string) string {
	if len(raw) < 100 {
		return raw
	}
	return base64ImageRegex.ReplaceAllStringFunc(raw, func(m string) string {
		return fmt.Sprintf("[base64_image_collapsed: len=%d]", len(m))
	})
}

// CompressGzip 在内存中进行 Gzip 快速压缩
func CompressGzip(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	if err != nil {
		return nil, err
	}
	if _, err := zw.Write(data); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
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
