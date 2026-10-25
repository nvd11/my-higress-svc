package finops

import (
	"testing"
)

func TestCalculateCost(t *testing.T) {
	// 针对 Gemini 3.8 Flash: 1M 输入 (无缓存) + 1M 输出 = 0.75 + 3.75
	costUSD, costCNY, declared := CalculateCost("gemini-3.8-flash", 1000000, 1000000, 0, 7.23)
	expectedUSD := 4.5
	expectedCNY := 32.535

	if !declared {
		t.Fatalf("gemini-3.8-flash 应命中 config.yaml 价目")
	}
	if costUSD != expectedUSD {
		t.Errorf("costUSD = %f; want %f", costUSD, expectedUSD)
	}
	if costCNY != expectedCNY {
		t.Errorf("costCNY = %f; want %f", costCNY, expectedCNY)
	}

	// DeepSeek V4 Pro: 1M 输入 (无缓存) + 1M 输出 = 0.00096 + 0.00354
	dsUSD, dsCNY, declared := CalculateCost("deepseek-v4-pro-0813", 1000000, 1000000, 0, 7.23)
	expectedDsUSD := 0.0045
	expectedDsCNY := 0.032535
	if !declared {
		t.Fatalf("deepseek-v4-pro-0813 应命中 config.yaml 价目")
	}
	if dsUSD != expectedDsUSD {
		t.Errorf("deepseek costUSD = %f; want %f", dsUSD, expectedDsUSD)
	}
	if dsCNY != expectedDsCNY {
		t.Errorf("deepseek costCNY = %f; want %f", dsCNY, expectedDsCNY)
	}
}

// 缓存命中必须走 config.yaml 的 cache_read_per_1m, 而不是按输入全价算
func TestCalculateCostUsesCachePrice(t *testing.T) {
	// gemini-3.8-flash: 输入 0.75 / 输出 3.75 / 缓存读 0.075
	// 1M 输入其中 1M 全部命中缓存, 输出 0 → 应只收缓存价 0.075
	usd, _, declared := CalculateCost("gemini-3.8-flash", 1000000, 0, 1000000, 7.23)
	if !declared {
		t.Fatalf("gemini-3.8-flash 应命中 config.yaml 价目")
	}
	if usd != 0.075 {
		t.Fatalf("全缓存输入应收费 0.075, 实际 %f (缓存被按全价算了)", usd)
	}

	// 半缓存: 500k 全价 (0.375) + 500k 缓存价 (0.0375) = 0.4125
	usd, _, _ = CalculateCost("gemini-3.8-flash", 1000000, 0, 500000, 7.23)
	if usd != 0.4125 {
		t.Fatalf("半缓存输入应收费 0.4125, 实际 %f", usd)
	}
}

// 缓存 token 数异常时不得产生负输入
func TestCalculateCostClampsCacheOverflow(t *testing.T) {
	usd, _, declared := CalculateCost("gemini-3.8-flash", 1000, 0, 999999, 7.23)
	if !declared {
		t.Fatalf("gemini-3.8-flash 应命中 config.yaml 价目")
	}
	// 全部视为缓存: 1000 tokens × 0.075/1M = 0.000075
	if usd != 0.000075 {
		t.Fatalf("缓存溢出应被裁剪为 prompt 上限, 实际 %f", usd)
	}
}

// 未纳管模型: 明确返回 declared=false 且不收费, 禁止套用其它模型价格
func TestUndeclaredModelHasNoFallbackPrice(t *testing.T) {
	for _, model := range []string{"definitely-not-a-model", "claude-opus-5", "gemini-4-argon", "", "GPT-5.6-LUNA"} {
		if _, ok := GetModelPricing(model); ok {
			t.Errorf("%q 不应命中价目表", model)
		}
		if usd, cny, declared := CalculateCost(model, 1000000, 1000000, 0, 7.23); declared || usd != 0 || cny != 0 {
			t.Errorf("%q 未纳管却算出费用: usd=%f cny=%f declared=%v", model, usd, cny, declared)
		}
	}
}

// config.yaml 的 aliases 必须解析为同一价目, 并归一到主名
func TestAliasResolvesToCanonicalPricing(t *testing.T) {
	canonical, ok := GetModelPricing("gemini-3.8-flash")
	if !ok {
		t.Fatalf("gemini-3.8-flash 应命中")
	}
	for _, alias := range []string{"gemini-3.8", "gemini-2.5-flash"} {
		got, ok := GetModelPricing(alias)
		if !ok {
			t.Fatalf("别名 %q 未生效", alias)
		}
		if got != canonical {
			t.Errorf("别名 %q 价目与主名不一致: %+v vs %+v", alias, got, canonical)
		}
		if got.Canonical != "gemini-3.8-flash" {
			t.Errorf("别名 %q 未归一到主名, 得到 %q", alias, got.Canonical)
		}
	}

	// a6 渠道: gpt-5.6-luna 的主名与 -a6 别名同价 (历史事故点)
	main, ok := GetModelPricing("gpt-5.6-luna")
	if !ok {
		t.Fatalf("gpt-5.6-luna 应命中")
	}
	alias, ok := GetModelPricing("gpt-5.6-luna-a6")
	if !ok || alias.PromptPer1M != main.PromptPer1M {
		t.Fatalf("gpt-5.6-luna-a6 别名价目异常: %+v", alias)
	}
}


func TestResolveVirtualKey(t *testing.T) {
	vkeys := map[string]string{
		"sk-cindy-secret": "cindy",
		"sk-moon-secret":  "moon",
	}

	alias, ok := ResolveVirtualKey("Bearer sk-cindy-secret", vkeys)
	if !ok || alias != "cindy" {
		t.Errorf("want cindy, got %s (ok=%v)", alias, ok)
	}

	alias, ok = ResolveVirtualKey("Bearer sk-invalid", vkeys)
	if ok || alias != "" {
		t.Errorf("want empty/false, got %s (ok=%v)", alias, ok)
	}

	alias, ok = ResolveVirtualKey("", vkeys)
	if ok || alias != "" {
		t.Errorf("want empty/false for empty authHeader, got %s (ok=%v)", alias, ok)
	}

	alias, ok = ResolveVirtualKey("Bearer   ", vkeys)
	if ok || alias != "" {
		t.Errorf("want empty/false for whitespace token, got %s (ok=%v)", alias, ok)
	}
}
