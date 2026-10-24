package logic

import (
	"encoding/json"
	"strings"
)

const (
	// MaxSSEParseBytes 超过该阈值的裸流不再做聚合解析，直接原样透出，防止单请求吃爆内存
	MaxSSEParseBytes = 64 << 20

	// ImagePreviewChars 图片 URL / data-uri 的展示预览长度上限
	ImagePreviewChars = 120
)

// NormalizePayload 报文读侧幂等整形。
//
// 背景：gemini-adapter 时代（commit 3165c0b）由适配器把请求体拆成
// {system_prompt, user_prompt, messages} 三个键；adapter 退役（commit 502c8cb）后，
// finops-audit wasm 插件只上报裸 OpenAI/Anthropic 请求体，Dashboard 前端
// PayloadDrawer 依赖的键名不复存在，导致 System Prompt 卡片消失、
// User Prompt 显示「（无最新单独用户输入）」。
//
// 因此这里在读取侧统一补一层整形：
//   - 幂等：adapter 时代已整形过的老数据原样返回，不做任何改写
//   - 兼容 OpenAI messages[] / Anthropic 顶层 system / Responses API input[]
//   - 裸 SSE 流式响应聚合出干净的 reply / reasoning_content / tool_calls
func NormalizePayload(promptObj, responseObj map[string]interface{}) (map[string]interface{}, map[string]interface{}) {
	return normalizePrompt(promptObj), normalizeResponse(responseObj)
}

// normalizePrompt 从请求体中派生 system_prompt / user_prompt，并保证 messages 存在
func normalizePrompt(promptObj map[string]interface{}) map[string]interface{} {
	if promptObj == nil {
		return nil
	}

	// 幂等快路径：已经是整形结构，直接返回
	if hasKey(promptObj, "system_prompt") && hasKey(promptObj, "user_prompt") {
		return promptObj
	}

	// 历史兜底：非法 JSON 被存成了 raw_content，这里再给一次解析机会
	if !hasKey(promptObj, "messages") && !hasKey(promptObj, "input") {
		if raw := asString(promptObj["raw_content"]); raw != "" {
			var parsed map[string]interface{}
			if err := json.Unmarshal([]byte(raw), &parsed); err == nil {
				if hasKey(parsed, "system_prompt") && hasKey(parsed, "user_prompt") {
					return parsed
				}
				promptObj = parsed
			}
		}
	}

	out := make(map[string]interface{}, len(promptObj)+2)
	for k, v := range promptObj {
		out[k] = v
	}

	// Anthropic 协议的 system 在顶层，不在 messages 里
	if !hasKey(out, "system_prompt") {
		if sys, ok := out["system"]; ok {
			if txt := extractBlockText(sys); txt != "" {
				out["system_prompt"] = txt
			}
		}
	}

	msgs := asMessageList(out["messages"])
	if msgs == nil {
		// Responses API 把消息放在 input[]
		msgs = asMessageList(out["input"])
		if msgs != nil && !hasKey(out, "messages") {
			out["messages"] = out["input"]
		}
	}
	if msgs == nil {
		return out
	}

	// 「本次最新提问」取最后一条 user 消息（多轮 & tool-call 循环下都正确）
	lastUserIdx := -1
	for i := len(msgs) - 1; i >= 0; i-- {
		if asString(msgs[i]["role"]) == "user" {
			lastUserIdx = i
			break
		}
	}

	var systemText, userText string
	for i, m := range msgs {
		role := asString(m["role"])
		switch {
		case role == "system" && systemText == "":
			systemText = extractBlockText(m["content"])
		case role == "user" && i == lastUserIdx:
			userText = extractBlockText(m["content"])
		}
	}

	if !hasKey(out, "system_prompt") && systemText != "" {
		out["system_prompt"] = systemText
	}
	if !hasKey(out, "user_prompt") && userText != "" {
		out["user_prompt"] = userText
	}
	return out
}

// normalizeResponse 把裸 SSE 流式响应聚合成结构化字段（幂等）
func normalizeResponse(responseObj map[string]interface{}) map[string]interface{} {
	if responseObj == nil {
		return nil
	}

	// 非流式响应: 上游直接返回完整 JSON (OpenAI chat.completion / Anthropic messages)
	if !hasNonEmpty(responseObj, "reply") && !hasNonEmpty(responseObj, "raw_content") {
		if enriched := normalizeCompletionObject(responseObj); enriched != nil {
			return enriched
		}
	}

	raw := asString(responseObj["reply"])
	if raw == "" {
		raw = asString(responseObj["raw_content"])
	}
	if raw == "" || len(raw) > MaxSSEParseBytes || !looksLikeSSE(raw) {
		return responseObj
	}

	agg := aggregateSSE(raw)
	if agg == nil {
		return responseObj
	}

	out := make(map[string]interface{}, len(responseObj)+4)
	for k, v := range responseObj {
		out[k] = v
	}

	// 原始流完整保留在 raw_stream，供「原始 JSON 报文」标签页回看，不丢证据
	out["raw_stream"] = raw
	out["reply"] = agg.reply
	if agg.reasoning != "" {
		out["reasoning_content"] = agg.reasoning
	}
	if len(agg.toolCalls) > 0 {
		out["tool_calls"] = agg.toolCalls
	}
	if agg.usage != nil && !hasKey(out, "usage") {
		out["usage"] = agg.usage
	}
	return out
}

// normalizeCompletionObject 把"已经结构化的非流式响应体"补齐成
// {reply, reasoning_content, tool_calls}，让非流式调用也能正常展示。
// 返回 nil 表示不是可识别的响应结构，交给调用方原样透出。
func normalizeCompletionObject(responseObj map[string]interface{}) map[string]interface{} {
	reply, reasoning, toolCalls := extractCompletionViews(responseObj)
	if reply == "" && reasoning == "" && len(toolCalls) == 0 {
		return nil
	}

	out := make(map[string]interface{}, len(responseObj)+3)
	for k, v := range responseObj {
		out[k] = v
	}
	out["reply"] = reply
	if reasoning != "" {
		out["reasoning_content"] = reasoning
	}
	if len(toolCalls) > 0 {
		out["tool_calls"] = toolCalls
	}
	return out
}

// extractCompletionViews 从 OpenAI chat.completion 或 Anthropic messages 响应体中
// 抽取正文、思维链与工具调用
func extractCompletionViews(responseObj map[string]interface{}) (string, string, []map[string]interface{}) {
	// --- OpenAI chat.completion ---
	if choices, ok := responseObj["choices"].([]interface{}); ok && len(choices) > 0 {
		if ch, ok := choices[0].(map[string]interface{}); ok {
			if msg, ok := ch["message"].(map[string]interface{}); ok {
				var calls []map[string]interface{}
				if list, ok := msg["tool_calls"].([]interface{}); ok {
					for _, item := range list {
						if m, ok := item.(map[string]interface{}); ok {
							calls = append(calls, m)
						}
					}
				}
				return asString(msg["content"]), asString(msg["reasoning_content"]), calls
			}
		}
	}

	// --- Anthropic messages: content 是 block 数组 ---
	if blocks, ok := responseObj["content"].([]interface{}); ok {
		var text strings.Builder
		var reasoning strings.Builder
		var calls []map[string]interface{}
		matched := false
		for _, item := range blocks {
			m, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			switch asString(m["type"]) {
			case "text":
				text.WriteString(asString(m["text"]))
				matched = true
			case "thinking":
				reasoning.WriteString(asString(m["thinking"]))
				matched = true
			case "tool_use":
				calls = append(calls, map[string]interface{}{
					"id":   asString(m["id"]),
					"type": "function",
					"function": map[string]interface{}{
						"name":      asString(m["name"]),
						"arguments": mustJSONString(m["input"]),
					},
				})
				matched = true
			}
		}
		if matched {
			return text.String(), reasoning.String(), calls
		}
	}
	return "", "", nil
}

func mustJSONString(v interface{}) string {
	if v == nil {
		return "{}"
	}
	if s, ok := v.(string); ok {
		return s
	}
	if b, err := json.Marshal(v); err == nil {
		return string(b)
	}
	return "{}"
}

func hasNonEmpty(m map[string]interface{}, key string) bool {
	return asString(m[key]) != ""
}

type sseAggregate struct {
	reply     string
	reasoning string
	toolCalls []map[string]interface{}
	usage     map[string]interface{}
}

// aggregateSSE 兼容 OpenAI chat.completion.chunk、Responses API 与 Anthropic messages 三种流
func aggregateSSE(raw string) *sseAggregate {
	agg := &sseAggregate{}

	var content, reasoning strings.Builder
	tcByIndex := make(map[int]map[string]interface{})
	var tcOrder []int
	partialJSON := make(map[int]*strings.Builder)

	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}

		var payload string
		switch {
		case strings.HasPrefix(line, "data:"):
			payload = strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if payload == "" || payload == "[DONE]" {
				continue
			}
		case strings.HasPrefix(line, "{"):
			// 少数网关会丢弃 data: 前缀，逐行直出 JSON
			payload = line
		default:
			continue
		}

		var frame map[string]interface{}
		if err := json.Unmarshal([]byte(payload), &frame); err != nil {
			continue
		}

		// --- OpenAI Chat Completions ---
		if choices, ok := frame["choices"].([]interface{}); ok && len(choices) > 0 {
			if ch, ok := choices[0].(map[string]interface{}); ok {
				if d, ok := ch["delta"].(map[string]interface{}); ok {
					content.WriteString(asString(d["content"]))
					reasoning.WriteString(asString(d["reasoning_content"]))
					mergeToolCalls(d["tool_calls"], tcByIndex, &tcOrder)
				}
				if m, ok := ch["message"].(map[string]interface{}); ok {
					content.WriteString(asString(m["content"]))
					reasoning.WriteString(asString(m["reasoning_content"]))
					mergeToolCalls(m["tool_calls"], tcByIndex, &tcOrder)
				}
			}
		}

		// --- OpenAI Responses API ---
		switch asString(frame["type"]) {
		case "response.output_text.delta":
			content.WriteString(asString(frame["delta"]))
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			reasoning.WriteString(asString(frame["delta"]))
		case "content_block_delta":
			// --- Anthropic messages ---
			if d, ok := frame["delta"].(map[string]interface{}); ok {
				switch asString(d["type"]) {
				case "text_delta":
					content.WriteString(asString(d["text"]))
				case "thinking_delta":
					reasoning.WriteString(asString(d["thinking"]))
				case "input_json_delta":
					idx := intField(frame["index"])
					if partialJSON[idx] == nil {
						partialJSON[idx] = &strings.Builder{}
					}
					partialJSON[idx].WriteString(asString(d["partial_json"]))
				}
			}
		case "content_block_start":
			if cb, ok := frame["content_block"].(map[string]interface{}); ok && asString(cb["type"]) == "tool_use" {
				idx := intField(frame["index"])
				tc := getOrCreateToolCall(idx, tcByIndex, &tcOrder)
				tc["id"] = asString(cb["id"])
				tc["type"] = "function"
				tc["function"] = map[string]interface{}{
					"name":      asString(cb["name"]),
					"arguments": "",
				}
			}
		}

		if u, ok := frame["usage"].(map[string]interface{}); ok {
			agg.usage = u
		}
	}

	// 回填 Anthropic 分片到达的 tool 参数
	for idx, sb := range partialJSON {
		if tc, ok := tcByIndex[idx]; ok {
			if fn, ok := tc["function"].(map[string]interface{}); ok {
				fn["arguments"] = sb.String()
			}
		}
	}

	agg.reply = content.String()
	agg.reasoning = reasoning.String()
	for _, idx := range tcOrder {
		agg.toolCalls = append(agg.toolCalls, tcByIndex[idx])
	}
	return agg
}

func mergeToolCalls(raw interface{}, tcByIndex map[int]map[string]interface{}, order *[]int) {
	list, ok := raw.([]interface{})
	if !ok {
		return
	}
	for _, item := range list {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		idx := intField(m["index"])
		if _, was := m["index"]; !was {
			idx = len(*order)
		}
		tc := getOrCreateToolCall(idx, tcByIndex, order)

		if id := asString(m["id"]); id != "" {
			tc["id"] = id
		}
		if t := asString(m["type"]); t != "" {
			tc["type"] = t
		}
		fn, ok := m["function"].(map[string]interface{})
		if !ok {
			continue
		}
		cur, ok := tc["function"].(map[string]interface{})
		if !ok {
			cur = map[string]interface{}{}
			tc["function"] = cur
		}
		if name := asString(fn["name"]); name != "" {
			cur["name"] = name
		}
		if args := asString(fn["arguments"]); args != "" {
			cur["arguments"] = asString(cur["arguments"]) + args
		}
	}
}

func getOrCreateToolCall(idx int, tcByIndex map[int]map[string]interface{}, order *[]int) map[string]interface{} {
	if tc, ok := tcByIndex[idx]; ok {
		return tc
	}
	tc := map[string]interface{}{}
	tcByIndex[idx] = tc
	*order = append(*order, idx)
	return tc
}

// extractBlockText 从 OpenAI / Anthropic / Responses 的 content 结构中抽取纯文本
func extractBlockText(content interface{}) string {
	switch v := content.(type) {
	case string:
		return v

	case []interface{}:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			if s, ok := item.(string); ok {
				if s != "" {
					parts = append(parts, s)
				}
				continue
			}
			m, ok := item.(map[string]interface{})
			if !ok {
				continue
			}
			switch asString(m["type"]) {
			case "image_url", "input_image":
				parts = append(parts, imagePlaceholder(m["image_url"]))
			case "image":
				parts = append(parts, anthropicImagePlaceholder(m["source"]))
			case "tool_use":
				parts = append(parts, "[tool_use] "+asString(m["name"]))
			case "tool_result":
				if t := extractBlockText(m["content"]); t != "" {
					parts = append(parts, "[tool_result] "+t)
				}
			default:
				if t := asString(m["text"]); t != "" {
					parts = append(parts, t)
					continue
				}
				if t := extractBlockText(m["content"]); t != "" {
					parts = append(parts, t)
				}
			}
		}
		return strings.Join(parts, "\n")

	case map[string]interface{}:
		if t := asString(v["text"]); t != "" {
			return t
		}
		if c, ok := v["content"]; ok {
			return extractBlockText(c)
		}
		if b, err := json.Marshal(v); err == nil {
			return string(b)
		}
	}
	return ""
}

func imagePlaceholder(img interface{}) string {
	url := ""
	switch v := img.(type) {
	case string:
		url = v
	case map[string]interface{}:
		url = asString(v["url"])
		if url == "" {
			return anthropicImagePlaceholder(v["source"])
		}
	}
	if url == "" {
		return "🖼️ [图片]"
	}
	if len(url) > ImagePreviewChars {
		url = url[:ImagePreviewChars] + "..."
	}
	return "🖼️ [图片] " + url
}

func anthropicImagePlaceholder(source interface{}) string {
	m, ok := source.(map[string]interface{})
	if !ok {
		return "🖼️ [图片]"
	}
	switch asString(m["type"]) {
	case "base64":
		return "🖼️ [图片] (base64 " + asString(m["media_type"]) + ")"
	case "url":
		url := asString(m["url"])
		if len(url) > ImagePreviewChars {
			url = url[:ImagePreviewChars] + "..."
		}
		return "🖼️ [图片] " + url
	case "file":
		return "🖼️ [图片] (file " + asString(m["file_id"]) + ")"
	}
	return "🖼️ [图片]"
}

func looksLikeSSE(raw string) bool {
	t := strings.TrimSpace(raw)
	if t == "" {
		return false
	}
	if strings.HasPrefix(t, "data:") {
		return true
	}
	return strings.Contains(t, "\ndata:") || strings.Contains(t, "\r\ndata:")
}

func asMessageList(v interface{}) []map[string]interface{} {
	switch list := v.(type) {
	case []interface{}:
		out := make([]map[string]interface{}, 0, len(list))
		for _, item := range list {
			if m, ok := item.(map[string]interface{}); ok {
				out = append(out, m)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case []map[string]interface{}:
		if len(list) == 0 {
			return nil
		}
		return list
	}
	return nil
}

func asString(v interface{}) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func intField(v interface{}) int {
	if f, ok := v.(float64); ok {
		return int(f)
	}
	if n, ok := v.(int); ok {
		return n
	}
	return 0
}

func hasKey(m map[string]interface{}, key string) bool {
	_, ok := m[key]
	return ok
}
