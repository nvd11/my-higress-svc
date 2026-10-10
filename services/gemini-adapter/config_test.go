package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestConfigLoadAndModelLookup(t *testing.T) {
	tmpDir := t.TempDir()
	cfgFile := filepath.Join(tmpDir, "config.yaml")

	yamlContent := `
model_list:
  - model_name: "custom-gemini"
    aliases: ["gemini-pro-alias", "my-gemini"]
    provider: "google-gemini"
    protocol: "gemini-native"
    upstream_url: "https://generativelanguage.googleapis.com"
    api_key: "os.environ/TEST_ENV_KEY"
    pricing:
      prompt_per_1m: 0.10
      completion_per_1m: 0.40
      cache_read_per_1m: 0.025
      tiered_threshold: 100000
      tiered_multiplier: 2.0

  - model_name: "kimi-k3"
    aliases: ["kimi"]
    provider: "a6api"
    protocol: "openai"
    upstream_url: "https://api.a6api.com/v1/chat/completions"
    upstream_model: "kimi-k3"
    api_key: "os.environ/A6_KEY"
    fallback: "gpt-5.6-luna"
    pricing:
      prompt_per_1m: 0.15
      completion_per_1m: 0.60
      cache_read_per_1m: 0.03
`
	if err := os.WriteFile(cfgFile, []byte(yamlContent), 0644); err != nil {
		t.Fatalf("failed writing test config: %v", err)
	}

	cfg, err := LoadConfig(cfgFile)
	if err != nil {
		t.Fatalf("failed loading config: %v", err)
	}
	if len(cfg.ModelList) != 2 {
		t.Fatalf("expected 2 models, got %d", len(cfg.ModelList))
	}

	// 1. 测试精准匹配
	m1 := FindModelConfig("custom-gemini")
	if m1 == nil {
		t.Fatalf("failed finding model by exact name")
	}
	if m1.Provider != "google-gemini" {
		t.Errorf("expected provider google-gemini, got %s", m1.Provider)
	}

	// 2. 测试 Alias 别名匹配
	mAlias := FindModelConfig("gemini-pro-alias")
	if mAlias == nil || mAlias.ModelName != "custom-gemini" {
		t.Fatalf("failed finding model by alias")
	}

	// 3. 测试模糊包含匹配
	mFuzzy := FindModelConfig("my-kimi-fast")
	if mFuzzy == nil || mFuzzy.ModelName != "kimi-k3" {
		t.Fatalf("failed finding model by fuzzy matching kimi")
	}

	// 4. 测试 Key 环境变量解析
	os.Setenv("TEST_ENV_KEY", "secret-test-key")
	if m1.ResolveKey() != "secret-test-key" {
		t.Errorf("expected secret-test-key, got %s", m1.ResolveKey())
	}
}

func TestPricingCalculation(t *testing.T) {
	cfg := &ModelConfig{
		ModelName: "test-model",
		Pricing: ModelPricing{
			PromptPer1M:      0.075,
			CompletionPer1M:  0.300,
			CacheReadPer1M:   0.01875,
			TieredThreshold:  128000,
			TieredMultiplier: 2.0,
		},
	}

	// Case 1: 正常范围 (prompt 10k, cache 8k, completion 1k)
	cost1, pPrice, caPrice, cPrice := cfg.CalculatePrice(10000, 8000, 1000)
	if pPrice != 0.075 || caPrice != 0.01875 || cPrice != 0.300 {
		t.Errorf("standard price mismatch: %f, %f, %f", pPrice, caPrice, cPrice)
	}
	// nonCached = 2000 => 2000 * 0.075 = 150
	// cache = 8000 => 8000 * 0.01875 = 150
	// comp = 1000 => 1000 * 0.300 = 300
	// total = 600 / 1M = 0.0006
	expected1 := (2000.0*0.075 + 8000.0*0.01875 + 1000.0*0.300) / 1000000.0
	if cost1 != expected1 {
		t.Errorf("expected cost %f, got %f", expected1, cost1)
	}

	// Case 2: 超过 128k 触发翻倍
	cost2, pPrice2, caPrice2, cPrice2 := cfg.CalculatePrice(200000, 100000, 2000)
	if pPrice2 != 0.150 || caPrice2 != 0.0375 || cPrice2 != 0.600 {
		t.Errorf("tiered price mismatch: %f, %f, %f", pPrice2, caPrice2, cPrice2)
	}
	expected2 := (100000.0*0.150 + 100000.0*0.0375 + 2000.0*0.600) / 1000000.0
	if cost2 != expected2 {
		t.Errorf("expected tiered cost %f, got %f", expected2, cost2)
	}
}
