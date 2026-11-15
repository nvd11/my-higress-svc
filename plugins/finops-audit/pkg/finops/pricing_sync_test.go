package finops

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/nvd11/my-higress-svc/plugins/finops-audit/internal/chargeconfig"
)

// repoConfigPath 从 pkg/finops 上溯到仓库根目录的 config.yaml
const repoConfigPath = "../../../../config.yaml"

// TestPricingGeneratedFromConfigYAML 守卫 "config.yaml 是唯一标准":
// 生成表必须与 config.yaml 逐字段一致, 任何一边漂移都会让 CI 红。
func TestPricingGeneratedFromConfigYAML(t *testing.T) {
	path, err := filepath.Abs(repoConfigPath)
	if err != nil {
		t.Fatalf("解析 config.yaml 路径失败: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("找不到 %s: %v (测试需在完整仓库内运行)", path, err)
	}

	models, err := chargeconfig.ParseFile(path)
	if err != nil {
		t.Fatalf("解析 config.yaml 失败: %v", err)
	}

	expectedKeys := 0
	for _, m := range models {
		expectedKeys += 1 + len(m.Aliases)
		want := ModelPricing{
			PromptPer1M:     m.Pricing.PromptPer1M,
			CompletionPer1M: m.Pricing.CompletionPer1M,
			CacheReadPer1M:  m.Pricing.CacheReadPer1M,
			Provider:        m.Provider,
			ProviderKey:     providerKeyAlias(m.APIKey),
			Canonical:       m.Name,
			UpstreamModel:   upstreamModel(m),
		}

		for _, key := range append([]string{m.Name}, m.Aliases...) {
			got, ok := modelPricing[key]
			if !ok {
				t.Errorf("价目表缺少 %q (config.yaml 已声明) —— 请运行 go run internal/pricinggen/main.go", key)
				continue
			}
			if got != want {
				t.Errorf("%q 价目与 config.yaml 不一致:\n got  %+v\n want %+v", key, got, want)
			}
		}
	}

	if len(modelPricing) != expectedKeys {
		t.Errorf("价目表键数 %d 与 config.yaml 派生数 %d 不一致 (存在多余或缺失键)", len(modelPricing), expectedKeys)
	}

	if declared := DeclaredModels(); len(declared) != len(models) {
		t.Fatalf("declaredModels %d 个, config.yaml %d 个", len(declared), len(models))
	} else {
		for i := range models {
			if declared[i] != models[i].Name {
				t.Errorf("declaredModels[%d] = %q; want %q", i, declared[i], models[i].Name)
			}
		}
	}
}

// TestUndeclaredModelsAreNotPriced 反向守卫: 价目表里不能出现 config.yaml 未声明的模型
func TestUndeclaredModelsAreNotPriced(t *testing.T) {
	for _, model := range []string{"gemini-3.8-backup", "gpt-5.6-luna-yuanheng", "yui", "rin"} {
		if _, ok := GetModelPricing(model); ok {
			t.Errorf("%q 曾出现在旧硬编码矩阵里, 但 config.yaml 未声明, 不应有价目", model)
		}
	}
}

// TestPricingInvariants 守卫 config.yaml 的三段价目不会写反:
// 缓存读不得贵于输入 (否则缓存命中反而更亏), 输出不得便宜于输入。
func TestPricingInvariants(t *testing.T) {
	for _, name := range DeclaredModels() {
		p, ok := GetModelPricing(name)
		if !ok {
			t.Fatalf("%q 未命中价目表", name)
		}
		if p.CacheReadPer1M > p.PromptPer1M {
			t.Errorf("%s: 缓存读价 %v 高于输入价 %v (疑似写反)", name, p.CacheReadPer1M, p.PromptPer1M)
		}
		if p.CompletionPer1M < p.PromptPer1M {
			t.Errorf("%s: 输出价 %v 低于输入价 %v (疑似写反)", name, p.CompletionPer1M, p.PromptPer1M)
		}
	}
}

// TestDeepSeekV41Flash1017Pricing 守卫 2026-11-15 由主人提供的上游人民币价目的
// 折算正确性。上游价目 (CNY / 1M tokens):
//
//	输入 ¥0.048038 · 输出 ¥0.192151 · 缓存读 ¥0.000961
//
// 网关统一按 fx=7.23 折算为美元入表。这里独立于生成器重算一遍, 防止
// 手抄错误 (少一位小数/漏除汇率/把输出价写成输入价)。
func TestDeepSeekV41Flash1017Pricing(t *testing.T) {
	const fx = 7.23
	cases := []struct {
		name string
		cny  float64
		got  func(ModelPricing) float64
	}{
		{"prompt", 0.048038, func(p ModelPricing) float64 { return p.PromptPer1M }},
		{"completion", 0.192151, func(p ModelPricing) float64 { return p.CompletionPer1M }},
		{"cache_read", 0.000961, func(p ModelPricing) float64 { return p.CacheReadPer1M }},
	}

	p, ok := GetModelPricing("deepseek-v4.1-flash-1017")
	if !ok {
		t.Fatal("deepseek-v4.1-flash-1017 未命中价目表")
	}
	if p.Canonical != "deepseek-v4.1-flash-1017" {
		t.Errorf("Canonical = %q", p.Canonical)
	}
	if p.UpstreamModel != "deepseek-v4.1-flash-1017" {
		t.Errorf("UpstreamModel = %q", p.UpstreamModel)
	}
	if p.Provider != "a6api" {
		t.Errorf("Provider = %q", p.Provider)
	}

	for _, c := range cases {
		want := c.cny / fx
		if got := c.got(p); math.Abs(got-want) > 1e-6 {
			t.Errorf("%s 价 = %.9f, 期望 ¥%.6f / %.2f = %.9f", c.name, got, c.cny, fx, want)
		}
	}

	// 成本口径: 1M 输入 + 1M 输出 (无缓存命中) 应为 0.006644 + 0.026577 USD
	usd, cny, declared := CalculateCost("deepseek-v4.1-flash-1017", 1_000_000, 1_000_000, 0, fx)
	if !declared {
		t.Fatal("CalculateCost 报未纳管")
	}
	if want := 0.033221; math.Abs(usd-want) > 1e-6 {
		t.Errorf("cost_usd = %.9f, 期望 %.6f", usd, want)
	}
	if want := 0.033221 * fx; math.Abs(cny-want) > 1e-6 {
		t.Errorf("cost_cny = %.9f, 期望 %.6f", cny, want)
	}
}

func providerKeyAlias(apiKey string) string {
	const prefix = "os.environ/"
	if len(apiKey) > len(prefix) && apiKey[:len(prefix)] == prefix {
		return apiKey[len(prefix):]
	}
	if apiKey == "" {
		return "unknown"
	}
	return apiKey
}

func upstreamModel(m chargeconfig.Model) string {
	if m.UpstreamModel != "" {
		return m.UpstreamModel
	}
	return m.Name
}
