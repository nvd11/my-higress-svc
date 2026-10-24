package finops

import (
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
