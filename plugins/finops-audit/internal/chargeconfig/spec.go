// Package chargeconfig 解析仓库根目录 config.yaml 的 model_list 段。
//
// config.yaml 是模型拓扑与计费的唯一标准 (single source of truth)。
// Wasm 插件运行在 wasip1 沙箱内无法读取文件, 因此由 internal/pricinggen
// 在构建期把它编译成 pkg/finops/pricing_gen.go。
//
// 本包刻意只实现 config.yaml 使用的 YAML 子集, 保持零第三方依赖,
// 并对所有未知结构立即报错 —— 宁可构建失败, 也不静默吞掉新加的字段。
package chargeconfig

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Pricing 计费单价, 单位: 美元 / 1M tokens
type Pricing struct {
	PromptPer1M     float64
	CompletionPer1M float64
	CacheReadPer1M  float64
}

// Model 单个模型的声明式定义
type Model struct {
	Name          string
	Aliases       []string
	Provider      string
	Protocol      string
	UpstreamURL   string
	UpstreamModel string
	APIKey        string // 原始字面量, 例如 os.environ/A6_API_KEY
	Pricing       Pricing

	// 记录 config.yaml 中实际声明了哪些价位。
	// 显式写 0 (未核价) 合法; 整个字段缺失则视为配置错误。
	PromptSet, CompletionSet, CacheReadSet bool
}

// List config.yaml 中按文件顺序声明的模型清单
type List []Model

const (
	sectionNone    = ""
	sectionAliases = "aliases"
	sectionPricing = "pricing"
)

// 允许出现的模型级标量字段。新增字段必须同步登记, 否则解析报错。
var allowedScalarFields = map[string]bool{
	"model_name":     true,
	"provider":       true,
	"protocol":       true,
	"upstream_url":   true,
	"upstream_model": true,
	"enable_search":  true,
	"api_key":        true,
	"fallback":       true,
}

// ParseFile 读取并解析指定路径的 config.yaml
func ParseFile(path string) (List, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取 %s 失败: %w", path, err)
	}
	list, err := Parse(string(raw))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return list, nil
}

// Parse 解析 config.yaml 文本
func Parse(src string) (List, error) {
	var out List
	var cur *Model
	section := sectionNone
	seenModelList := false

	for i, raw := range strings.Split(src, "\n") {
		lineNo := i + 1
		line := strings.TrimRight(raw, " \t\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		indent := len(line) - len(strings.TrimLeft(line, " "))

		switch indent {
		case 0:
			if trimmed != "model_list:" {
				return nil, fmt.Errorf("第 %d 行: 不支持的顶层键 %q (只允许 model_list)", lineNo, trimmed)
			}
			seenModelList = true

		case 2:
			if !strings.HasPrefix(trimmed, "- ") {
				return nil, fmt.Errorf("第 %d 行: model_list 下应为 \"- model_name: ...\" 列表项, 实际 %q", lineNo, trimmed)
			}
			if cur != nil {
				out = append(out, *cur)
			}
			cur = &Model{}
			section = sectionNone
			key, val, err := splitKV(strings.TrimPrefix(trimmed, "- "), lineNo)
			if err != nil {
				return nil, err
			}
			if err := assignScalar(cur, key, val, lineNo); err != nil {
				return nil, err
			}

		case 4:
			if cur == nil {
				return nil, fmt.Errorf("第 %d 行: 模型字段出现在列表项之外", lineNo)
			}
			key, val, err := splitKV(trimmed, lineNo)
			if err != nil {
				return nil, err
			}
			if key == sectionAliases || key == sectionPricing {
				if val != "" {
					return nil, fmt.Errorf("第 %d 行: %s 必须是块结构, 不能直接赋值", lineNo, key)
				}
				section = key
				continue
			}
			section = sectionNone
			if err := assignScalar(cur, key, val, lineNo); err != nil {
				return nil, err
			}

		case 6:
			if cur == nil {
				return nil, fmt.Errorf("第 %d 行: 嵌套字段出现在列表项之外", lineNo)
			}
			switch section {
			case sectionAliases:
				if !strings.HasPrefix(trimmed, "- ") {
					return nil, fmt.Errorf("第 %d 行: aliases 下应为 \"- 别名\" 列表项, 实际 %q", lineNo, trimmed)
				}
				cur.Aliases = append(cur.Aliases, unquote(strings.TrimPrefix(trimmed, "- ")))
			case sectionPricing:
				key, val, err := splitKV(trimmed, lineNo)
				if err != nil {
					return nil, err
				}
				price, err := strconv.ParseFloat(val, 64)
				if err != nil {
					return nil, fmt.Errorf("第 %d 行: pricing.%s 不是合法浮点数 %q", lineNo, key, val)
				}
				switch key {
				case "prompt_per_1m":
					cur.Pricing.PromptPer1M = price
					cur.PromptSet = true
				case "completion_per_1m":
					cur.Pricing.CompletionPer1M = price
					cur.CompletionSet = true
				case "cache_read_per_1m":
					cur.Pricing.CacheReadPer1M = price
					cur.CacheReadSet = true
				default:
					return nil, fmt.Errorf("第 %d 行: 不支持的定价字段 pricing.%s", lineNo, key)
				}
			default:
				return nil, fmt.Errorf("第 %d 行: 缩进层级非法 (块 %q 未声明)", lineNo, section)
			}

		default:
			return nil, fmt.Errorf("第 %d 行: 不支持的缩进 %d 空格: %q", lineNo, indent, trimmed)
		}
	}

	if !seenModelList {
		return nil, fmt.Errorf("未找到 model_list 段")
	}
	if cur != nil {
		out = append(out, *cur)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("model_list 为空")
	}
	if err := out.validate(); err != nil {
		return nil, err
	}
	return out, nil
}

// validate 结构校验 + 重名冲突检测
func (l List) validate() error {
	seen := make(map[string]string)
	for i := range l {
		m := &l[i]
		if m.Name == "" {
			return fmt.Errorf("第 %d 个模型缺少 model_name", i+1)
		}
		// 三段价位必须全部显式声明 (写 0 可以, 漏写不行)
		if !m.PromptSet || !m.CompletionSet || !m.CacheReadSet {
			return fmt.Errorf("模型 %s: pricing 必须同时声明 prompt_per_1m / completion_per_1m / cache_read_per_1m", m.Name)
		}
		for _, key := range append([]string{m.Name}, m.Aliases...) {
			if prev, dup := seen[key]; dup {
				return fmt.Errorf("模型名/别名 %q 重复出现 (%s 与 %s)", key, prev, m.Name)
			}
			seen[key] = m.Name
		}
	}
	return nil
}

func splitKV(body string, lineNo int) (string, string, error) {
	idx := strings.Index(body, ":")
	if idx <= 0 {
		return "", "", fmt.Errorf("第 %d 行: 期望 \"key: value\" 结构, 实际 %q", lineNo, body)
	}
	return strings.TrimSpace(body[:idx]), strings.TrimSpace(body[idx+1:]), nil
}

func assignScalar(m *Model, key, val string, lineNo int) error {
	if !allowedScalarFields[key] {
		return fmt.Errorf("第 %d 行: 不支持的模型字段 %q (若是新增字段, 请同步登记到 chargeconfig)", lineNo, key)
	}
	v := unquote(val)
	switch key {
	case "model_name":
		m.Name = v
	case "provider":
		m.Provider = v
	case "protocol":
		m.Protocol = v
	case "upstream_url":
		m.UpstreamURL = v
	case "upstream_model":
		m.UpstreamModel = v
	case "api_key":
		m.APIKey = v
	}
	return nil
}

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			if unq, err := strconv.Unquote(s); err == nil {
				return unq
			}
			return s[1 : len(s)-1]
		}
	}
	return s
}
