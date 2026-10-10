package main

import (
	"log"
	"os"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// ModelPricing 费率配置 (USD / 1M tokens)
type ModelPricing struct {
	PromptPer1M      float64 `yaml:"prompt_per_1m"`
	CompletionPer1M  float64 `yaml:"completion_per_1m"`
	CacheReadPer1M   float64 `yaml:"cache_read_per_1m"`
	TieredThreshold  int     `yaml:"tiered_threshold"`
	TieredMultiplier float64 `yaml:"tiered_multiplier"`
}

// ModelConfig 单个模型路由与配置
type ModelConfig struct {
	ModelName     string       `yaml:"model_name"`
	Aliases       []string     `yaml:"aliases"`
	Provider      string       `yaml:"provider"`
	Protocol      string       `yaml:"protocol"` // "gemini-native" | "openai"
	UpstreamURL   string       `yaml:"upstream_url"`
	UpstreamModel string       `yaml:"upstream_model"`
	APIKey        string       `yaml:"api_key"` // 支持 "os.environ/KEY_NAME" 或明文
	Fallback      string       `yaml:"fallback"`
	EnableSearch  bool         `yaml:"enable_search"`
	Pricing       ModelPricing `yaml:"pricing"`
}

// GlobalConfig 全局模型配置树 (对齐 LiteLLM config.yaml)
type GlobalConfig struct {
	ModelList []ModelConfig `yaml:"model_list"`
}

var (
	appConfig   *GlobalConfig
	configMutex sync.RWMutex
)

// ResolveKey 解析支持 os.environ/XXX 语法的 API Key
func (m *ModelConfig) ResolveKey() string {
	val := strings.TrimSpace(m.APIKey)
	if strings.HasPrefix(val, "os.environ/") {
		envName := strings.TrimPrefix(val, "os.environ/")
		return os.Getenv(envName)
	}
	return val
}

// LoadConfig 从指定路径或环境变量加载 config.yaml
func LoadConfig(path string) (*GlobalConfig, error) {
	if path == "" {
		path = os.Getenv("CONFIG_PATH")
	}
	if path == "" {
		// 默认探测路径
		candidates := []string{
			"config.yaml",
			"/etc/higress/config.yaml",
			"../../config.yaml",
		}
		for _, c := range candidates {
			if _, err := os.Stat(c); err == nil {
				path = c
				break
			}
		}
	}

	if path == "" {
		log.Printf("⚠️ No config.yaml found, using built-in fallback defaults")
		return getDefaultConfig(), nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("⚠️ Failed reading config from %s: %v, falling back to defaults", path, err)
		return getDefaultConfig(), nil
	}

	var cfg GlobalConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		log.Printf("❌ Failed parsing yaml from %s: %v", path, err)
		return nil, err
	}

	configMutex.Lock()
	appConfig = &cfg
	configMutex.Unlock()

	log.Printf("✅ Loaded %d models from configuration (%s)", len(cfg.ModelList), path)
	return &cfg, nil
}

// FindModelConfig 根据客户端请求的模型名精准或别名匹配模型配置
func FindModelConfig(reqModel string) *ModelConfig {
	configMutex.RLock()
	defer configMutex.RUnlock()

	if appConfig == nil || len(appConfig.ModelList) == 0 {
		return nil
	}

	normReq := strings.ToLower(strings.TrimSpace(reqModel))

	// 1. 精确匹配 model_name
	for i := range appConfig.ModelList {
		m := &appConfig.ModelList[i]
		if strings.ToLower(m.ModelName) == normReq {
			return m
		}
	}

	// 2. 精确匹配 aliases
	for i := range appConfig.ModelList {
		m := &appConfig.ModelList[i]
		for _, alias := range m.Aliases {
			if strings.ToLower(alias) == normReq {
				return m
			}
		}
	}

	// 3. 包含式模糊匹配 (如客户端传 kimi, glm, luna)
	for i := range appConfig.ModelList {
		m := &appConfig.ModelList[i]
		for _, alias := range m.Aliases {
			if strings.Contains(normReq, strings.ToLower(alias)) {
				return m
			}
		}
		if strings.Contains(normReq, strings.ToLower(m.ModelName)) {
			return m
		}
	}

	return nil
}

// CalculatePrice 核心计费动态核算
func (m *ModelConfig) CalculatePrice(promptTokens, cacheReadTokens, completionTokens int) (costUSD float64, promptPrice, cachePrice, compPrice float64) {
	pPrice := m.Pricing.PromptPer1M
	cPrice := m.Pricing.CompletionPer1M
	caPrice := m.Pricing.CacheReadPer1M

	// 阶梯倍率判定
	if m.Pricing.TieredThreshold > 0 && promptTokens > m.Pricing.TieredThreshold {
		mult := m.Pricing.TieredMultiplier
		if mult <= 0 {
			mult = 2.0
		}
		pPrice *= mult
		cPrice *= mult
		caPrice *= mult
	}

	nonCachedTokens := promptTokens - cacheReadTokens
	if nonCachedTokens < 0 {
		nonCachedTokens = 0
	}

	costUSD = (float64(nonCachedTokens)*pPrice + float64(cacheReadTokens)*caPrice + float64(completionTokens)*cPrice) / 1000000.0
	return costUSD, pPrice, caPrice, cPrice
}

func getDefaultConfig() *GlobalConfig {
	return &GlobalConfig{
		ModelList: []ModelConfig{
			{
				ModelName:   "gemini-3.8-flash",
				Aliases:     []string{"gemini-3.8", "gemini-2.5-flash"},
				Provider:    "google-gemini",
				Protocol:    "gemini-native",
				UpstreamURL: "https://generativelanguage.googleapis.com",
				APIKey:      "os.environ/OPENAI_API_KEY_FREE_3",
				Pricing: ModelPricing{
					PromptPer1M:      0.075,
					CompletionPer1M:  0.300,
					CacheReadPer1M:   0.01875,
					TieredThreshold:  128000,
					TieredMultiplier: 2.0,
				},
			},
			{
				ModelName:     "kimi-k3",
				Aliases:       []string{"kimi"},
				Provider:      "a6api",
				Protocol:      "openai",
				UpstreamURL:   "https://api.a6api.com/v1/chat/completions",
				UpstreamModel: "kimi-k3",
				APIKey:        "os.environ/A6_API_KEY",
				Fallback:      "gpt-5.6-luna",
				Pricing: ModelPricing{
					PromptPer1M:     0.150,
					CompletionPer1M: 0.600,
					CacheReadPer1M:  0.030,
				},
			},
			{
				ModelName:     "glm-5.3",
				Aliases:       []string{"glm"},
				Provider:      "a6api",
				Protocol:      "openai",
				UpstreamURL:   "https://api.a6api.com/v1/chat/completions",
				UpstreamModel: "glm-5.3",
				APIKey:        "os.environ/A6_API_KEY",
				Fallback:      "gpt-5.6-luna",
				Pricing: ModelPricing{
					PromptPer1M:     0.100,
					CompletionPer1M: 0.400,
					CacheReadPer1M:  0.020,
				},
			},
			{
				ModelName:     "gpt-5.6-luna",
				Aliases:       []string{"luna"},
				Provider:      "a6api",
				Protocol:      "openai",
				UpstreamURL:   "https://api.a6api.com/v1/chat/completions",
				UpstreamModel: "gpt-5.6-luna",
				APIKey:        "os.environ/A6_API_KEY",
				Pricing: ModelPricing{
					PromptPer1M:     0.250,
					CompletionPer1M: 1.000,
					CacheReadPer1M:  0.125,
				},
			},
		},
	}
}
