package finops

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ==============================================================================
// 🔧 Tool Calls 流式修复补丁 (regression fix)
// ------------------------------------------------------------------------------
// 背景 / 病根:
//   Higress 官方 ai-proxy 在流式转发时, 会把上游返回的工具调用"阉割"成空壳 chunk,
//   形如: {"choices":[{"index":0,"delta":{},"finish_reason":null}]}
//   —— delta 里既没有 content, 也没有 tool_calls。下游 agent(如 opencode) 因此永远
//   收不到工具调用, 表现为"模型说了半句话就停住"。
//
// 历史:
//   该修复最初在 6f1d961 被接入 **非流式** 钩子 onHttpResponseBody; 但在 4b16d96
//   "modernize finops-audit with streaming support" 大重构中, 钩子被换成
//   onHttpStreamingResponseBody, 修复调用被遗漏 -> 回归 (regression)。
//   由于单测只覆盖工具函数本身、没有任何测试锁定"钩子是否真的调用了它", 回归长期隐身。
//
// 本补丁:
//   1. 重建为 **流式逐 chunk** 修复 (RepairSSEChunk);
//   2. 同时覆盖 OpenAI (`tool_calls` / legacy `function_call`) 与 Gemini (`functionCall`);
//   3. 支持从旁路源(rawContext, 即此前累积的原始报文)回溯被拆到其它 chunk 的调用信息。
// ==============================================================================

// toolCallCandidate 描述一处可能的工具调用来源。
type toolCallCandidate struct {
	path    string // gjson 路径: 指向 function 对象 {name, arguments|args}
	argsKey string // 参数字段名: OpenAI 用 "arguments", Gemini 用 "args"
}

// recoverToolCallPaths 按优先级排列的候选路径。
var recoverToolCallPaths = []toolCallCandidate{
	// OpenAI 现代流式格式 (理论上不会被剥, 但万一残留也一并兜底)
	{"choices.0.delta.function_call", "arguments"},
	// OpenAI 非流式格式被误塞进流式 delta/message 的情况
	{"choices.0.message.tool_calls.0.function", "arguments"},
	{"choices.0.message.function_call", "arguments"},
	// Gemini 原始 functionCall 的几种落点
	{"choices.0.delta.functionCall", "args"},
	{"choices.0.message.functionCall", "args"},
	{"candidates.0.content.parts.0.functionCall", "args"},
	{"functionCall", "args"},
}

// RepairMalformedChunk 修复单个 SSE `data:` 事件 (OpenAI chat.completion.chunk)。
//
// rawChunkJSON 为事件 JSON 本体; rawSnippet 为可选的旁路原始片段(例如响应早前
// 累积的原始报文, 或 Gemini 原始报文), 用于捞回被拆散/未转换的 functionCall。
//
// 返回修复后的 JSON 以及是否发生了修改。任何无法解析、非 chunk 结构、或已含
// 实质内容/工具调用的事件都会原样返回 (modified=false), 保证幂等与安全。
func RepairMalformedChunk(rawChunkJSON, rawSnippet string) (string, bool) {
	if strings.TrimSpace(rawChunkJSON) == "" || !gjson.Valid(rawChunkJSON) {
		return rawChunkJSON, false
	}

	parsed := gjson.Parse(rawChunkJSON)
	if !parsed.Get("choices.0").Exists() {
		return rawChunkJSON, false
	}
	// 必须是流式 delta 结构; 非流式 / usage-only chunk 直接跳过
	delta := parsed.Get("choices.0.delta")
	if !delta.Exists() {
		return rawChunkJSON, false
	}

	// 已经携带实质内容 (content / reasoning / role) 或已有真实工具调用 -> 无需修复
	if hasMeaningfulDelta(delta) {
		return rawChunkJSON, false
	}

	name, args, ok := recoverToolCall(parsed, rawSnippet)
	if !ok || strings.TrimSpace(name) == "" {
		return rawChunkJSON, false
	}

	// 以 chunk id + 函数名作盐生成稳定的 call id
	callID := GenerateCallID(parsed.Get("id").String() + "|" + name)
	toolCall := map[string]interface{}{
		"id":    callID,
		"type":  "function",
		"index": 0,
		"function": map[string]string{
			"name":      name,
			"arguments": args,
		},
	}
	toolCallBytes, err := json.Marshal([]interface{}{toolCall})
	if err != nil {
		return rawChunkJSON, false
	}

	repaired, err := sjson.SetRaw(rawChunkJSON, "choices.0.delta.tool_calls", string(toolCallBytes))
	if err != nil {
		return rawChunkJSON, false
	}
	repaired, err = sjson.Set(repaired, "choices.0.finish_reason", "tool_calls")
	if err != nil {
		return rawChunkJSON, false
	}
	return repaired, true
}

// hasMeaningfulDelta 判断 delta 是否已经携带实质内容或真实工具调用。
// 注意: legacy 的 `function_call` 不算"实质内容", 需要被转换, 故此处返回 false。
func hasMeaningfulDelta(delta gjson.Result) bool {
	if strings.TrimSpace(delta.Get("content").String()) != "" {
		return true
	}
	if strings.TrimSpace(delta.Get("reasoning_content").String()) != "" {
		return true
	}
	if strings.TrimSpace(delta.Get("reasoning").String()) != "" {
		return true
	}
	if strings.TrimSpace(delta.Get("role").String()) != "" {
		return true
	}
	if tc := delta.Get("tool_calls"); tc.Exists() && tc.IsArray() && len(tc.Array()) > 0 {
		return true
	}
	return false
}

// recoverToolCall 依次在事件本体与旁路片段中寻找可恢复的工具调用。
func recoverToolCall(parsed gjson.Result, rawSnippet string) (name, args string, ok bool) {
	if n, a, found := extractToolCall(parsed); found {
		return n, a, true
	}
	if rawSnippet != "" && strings.Contains(rawSnippet, "functionCall") {
		if n, a, found := extractToolCallFromText(rawSnippet); found {
			return n, a, true
		}
	}
	return "", "", false
}

// extractToolCallFromText 从"任意文本"里捞 functionCall, 兼容两种情况:
//  1. 整段就是一个合法 JSON (如 Gemini 原始报文);
//  2. 一段 SSE 文本 (含多个 "data: {...}" 行, 如此前累积的原始响应)。
func extractToolCallFromText(text string) (name, args string, ok bool) {
	if gjson.Valid(text) {
		if n, a, found := extractToolCall(gjson.Parse(text)); found {
			return n, a, true
		}
	}
	for _, line := range strings.Split(text, "\n") {
		payload := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "data:"))
		if payload == "" || payload == "[DONE]" || !gjson.Valid(payload) {
			continue
		}
		if n, a, found := extractToolCall(gjson.Parse(payload)); found {
			return n, a, true
		}
	}
	return "", "", false
}

// extractToolCall 在给定的 JSON 树里按候选路径提取 name 与 arguments。
func extractToolCall(root gjson.Result) (name, args string, ok bool) {
	for _, c := range recoverToolCallPaths {
		fn := root.Get(c.path)
		if !fn.Exists() || !fn.IsObject() {
			continue
		}
		n := strings.TrimSpace(fn.Get("name").String())
		if n == "" {
			continue
		}
		return n, normalizeArgs(fn.Get(c.argsKey)), true
	}
	return "", "", false
}

// normalizeArgs 统一参数形态:
//
//	OpenAItool_calls.arguments 是 JSON 字符串 -> 原样返回;
//	Gemini functionCall.args 是对象 -> 返回其 Raw JSON 文本;
//	缺失 -> "{}"。
func normalizeArgs(r gjson.Result) string {
	if !r.Exists() {
		return "{}"
	}
	if r.Type == gjson.String {
		s := strings.TrimSpace(r.String())
		if s == "" {
			return "{}"
		}
		return s
	}
	if r.IsObject() || r.IsArray() {
		return r.Raw
	}
	s := strings.TrimSpace(r.String())
	if s == "" {
		return "{}"
	}
	return s
}

// RepairSSEChunk 对一段原始 SSE 字节流做逐事件修复。
//
// chunk 可包含 0..N 个 `data:` 事件; rawContext 为此前累积的原始报文(可为空),
// 用于回溯被拆到其它 chunk 的工具调用信息。
//
// 非 data: 行、空行、注释与 "[DONE]" 均原样透传; 未被修改时直接返回原 slice,
// 不做多余拷贝。返回值即应发给下游的字节流。
func RepairSSEChunk(chunk []byte, rawContext string) []byte {
	if len(chunk) == 0 || !bytes.Contains(chunk, []byte("data:")) {
		return chunk
	}

	lines := strings.Split(string(chunk), "\n")
	modified := false

	for i, line := range lines {
		body := strings.TrimSuffix(line, "\r")
		if !strings.HasPrefix(body, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(body, "data:"))
		if payload == "" || payload == "[DONE]" {
			continue
		}
		if repaired, ok := RepairMalformedChunk(payload, rawContext); ok {
			cr := ""
			if strings.HasSuffix(line, "\r") {
				cr = "\r"
			}
			lines[i] = "data: " + repaired + cr
			modified = true
		}
	}

	if !modified {
		return chunk
	}
	return []byte(strings.Join(lines, "\n"))
}
