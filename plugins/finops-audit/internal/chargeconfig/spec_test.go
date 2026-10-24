package chargeconfig

import (
	"strings"
	"testing"
)

const sample = `
# 注释行
model_list:
  - model_name: "alpha"
    aliases:
      - "a1"
      - "a2:search"
    provider: "a6api"
    protocol: "openai"
    upstream_url: "https://example.com/v1/chat/completions"
    upstream_model: "alpha-up"
    enable_search: true
    api_key: "os.environ/A6_API_KEY"
    fallback: "beta"
    pricing:
      prompt_per_1m: 0.5
      completion_per_1m: 1.5
      cache_read_per_1m: 0.05

  - model_name: "beta"
    provider: "google-gemini"
    api_key: "os.environ/G_KEY"
    pricing:
      prompt_per_1m: 0
      completion_per_1m: 0
      cache_read_per_1m: 0
`

func TestParseSample(t *testing.T) {
	list, err := Parse(sample)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("模型数 = %d; want 2", len(list))
	}

	a := list[0]
	if a.Name != "alpha" || a.Provider != "a6api" || a.UpstreamModel != "alpha-up" || a.APIKey != "os.environ/A6_API_KEY" {
		t.Errorf("alpha 字段解析异常: %+v", a)
	}
	if len(a.Aliases) != 2 || a.Aliases[0] != "a1" || a.Aliases[1] != "a2:search" {
		t.Errorf("aliases 解析异常: %v", a.Aliases)
	}
	if a.Pricing.PromptPer1M != 0.5 || a.Pricing.CompletionPer1M != 1.5 || a.Pricing.CacheReadPer1M != 0.05 {
		t.Errorf("pricing 解析异常: %+v", a.Pricing)
	}

	// 显式写 0 合法 (未核价模型), 不能被当成"缺失"
	if b := list[1]; !b.PromptSet || b.Pricing.PromptPer1M != 0 {
		t.Errorf("beta 的显式 0 价应被接受: %+v", b)
	}
}

func TestParseRejectsUnknownField(t *testing.T) {
	src := strings.Replace(sample, "protocol:", "proto_magic:", 1)
	if _, err := Parse(src); err == nil {
		t.Fatal("未知字段应报错, 而不是静默忽略")
	}
}

func TestParseRejectsMissingPriceField(t *testing.T) {
	src := strings.Replace(sample, "      cache_read_per_1m: 0.05\n", "", 1)
	if _, err := Parse(src); err == nil {
		t.Fatal("缺少 cache_read_per_1m 应报错")
	}
}

func TestParseRejectsDuplicateNames(t *testing.T) {
	src := strings.Replace(sample, `- model_name: "beta"`, `- model_name: "a1"`, 1)
	if _, err := Parse(src); err == nil {
		t.Fatal("别名与其它模型主名冲突应报错")
	}
}

func TestParseRejectsEmptyList(t *testing.T) {
	if _, err := Parse("model_list:\n"); err == nil {
		t.Fatal("空 model_list 应报错")
	}
}

func TestParseRejectsUnknownTopLevel(t *testing.T) {
	if _, err := Parse("general_settings:\n  master_key: x\n"); err == nil {
		t.Fatal("未知顶层键应报错")
	}
}
