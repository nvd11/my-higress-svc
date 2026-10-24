package logic

import (
	"encoding/json"
	"strings"
	"testing"
)

func mustJSON(t *testing.T, raw string) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("invalid test fixture: %v", err)
	}
	return m
}

// adapter 时代已整形过的老数据必须原样返回（幂等红线，最不能破的一条）
func TestNormalizePrompt_IdempotentOnAdapterData(t *testing.T) {
	fixture := mustJSON(t, `{
		"model": "gemini-4-argon",
		"system_prompt": "你是 Cindy",
		"user_prompt": "今天天气如何",
		"messages": [{"role":"user","content":"今天天气如何"}]
	}`)

	got, _ := NormalizePayload(fixture, nil)
	if got["system_prompt"] != "你是 Cindy" {
		t.Errorf("system_prompt 被改写: %v", got["system_prompt"])
	}
	if got["user_prompt"] != "今天天气如何" {
		t.Errorf("user_prompt 被改写: %v", got["user_prompt"])
	}
}

// 本次事故的主场：裸 OpenAI 请求体必须能派生回 system_prompt / user_prompt
func TestNormalizePrompt_OpenAIMessages(t *testing.T) {
	fixture := mustJSON(t, `{
		"model": "deepseek-v4-pro-0813",
		"stream": true,
		"messages": [
			{"role":"system","content":"You are a cold operator."},
			{"role":"user","content":"第一问"},
			{"role":"assistant","content":"第一答"},
			{"role":"user","content":"第二问（最新）"}
		]
	}`)

	got, _ := NormalizePayload(fixture, nil)
	if got["system_prompt"] != "You are a cold operator." {
		t.Errorf("system_prompt 派生失败: %v", got["system_prompt"])
	}
	if got["user_prompt"] != "第二问（最新）" {
		t.Errorf("user_prompt 应取最后一条 user 消息, got: %v", got["user_prompt"])
	}
	if _, ok := got["messages"]; !ok {
		t.Errorf("messages 必须保留")
	}
	// 不得污染入参
	if _, ok := fixture["system_prompt"]; ok {
		t.Errorf("不应原地修改入参 map")
	}
}

// Anthropic 协议：system 在顶层，messages 里没有 system 角色
func TestNormalizePrompt_AnthropicTopLevelSystem(t *testing.T) {
	fixture := mustJSON(t, `{
		"model": "claude-like",
		"system": "Anthropic 的系统提示",
		"messages": [{"role":"user","content":[{"type":"text","text":"看图说话"}]}]
	}`)

	got, _ := NormalizePayload(fixture, nil)
	if got["system_prompt"] != "Anthropic 的系统提示" {
		t.Errorf("顶层 system 未提取: %v", got["system_prompt"])
	}
	if got["user_prompt"] != "看图说话" {
		t.Errorf("多模态 text block 未提取: %v", got["user_prompt"])
	}
}

// 多模态：文本抽取 + 图片占位 + base64 不炸内存
func TestNormalizePrompt_MultimodalContent(t *testing.T) {
	fixture := mustJSON(t, `{
		"messages": [{"role":"user","content":[
			{"type":"text","text":"这张图里有什么"},
			{"type":"image_url","image_url":{"url":"data:image/png;base64,`+strings.Repeat("Q", 400)+`"}}
		]}]
	}`)

	got, _ := NormalizePayload(fixture, nil)
	up, _ := got["user_prompt"].(string)
	if !strings.Contains(up, "这张图里有什么") {
		t.Errorf("文本块丢失: %s", up)
	}
	if !strings.Contains(up, "🖼️ [图片]") {
		t.Errorf("图片占位缺失: %s", up)
	}
	if len(up) > 5000 {
		t.Errorf("图片 base64 未被折叠，长度 %d", len(up))
	}
}

// 历史兜底：非法 JSON 存成 raw_content 的脏数据也要能救
func TestNormalizePrompt_RawContentFallback(t *testing.T) {
	fixture := mustJSON(t, `{"raw_content":"{\"messages\":[{\"role\":\"system\",\"content\":\"S\"},{\"role\":\"user\",\"content\":\"U\"}]}"}`)

	got, _ := NormalizePayload(fixture, nil)
	if got["system_prompt"] != "S" || got["user_prompt"] != "U" {
		t.Errorf("raw_content 兜底失败: sys=%v user=%v", got["system_prompt"], got["user_prompt"])
	}
}

// 任何畸形输入都不允许 panic（读侧是纯展示路径，宁可退化也不能挂）
func TestNormalizePrompt_NeverPanics(t *testing.T) {
	cases := []map[string]interface{}{
		nil,
		{},
		{"messages": "not-a-list"},
		{"messages": []interface{}{"str", 123, nil}},
		{"messages": []interface{}{map[string]interface{}{"role": nil, "content": nil}}},
		{"raw_content": "这不是 JSON"},
		{"input": []interface{}{map[string]interface{}{"role": "user", "content": "Responses API"}}},
	}
	for i, c := range cases {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("case %d panicked: %v", i, r)
				}
			}()
			NormalizePayload(c, c)
		}()
	}
}

// Responses API 的 input[] 要能当 messages 用
func TestNormalizePrompt_ResponsesAPIInput(t *testing.T) {
	fixture := mustJSON(t, `{"input":[{"role":"user","content":"来自 input[] 的提问"}]}`)
	got, _ := NormalizePayload(fixture, nil)
	if got["user_prompt"] != "来自 input[] 的提问" {
		t.Errorf("input[] 未识别: %v", got["user_prompt"])
	}
	if _, ok := got["messages"]; !ok {
		t.Errorf("input[] 应被平移到 messages")
	}
}

// 事故的第二个症状：裸 SSE 必须聚合成干净文本 + 思维链 + 工具调用
func TestNormalizeResponse_SSEAggregation(t *testing.T) {
	raw := strings.Join([]string{
		`data: {"model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"role":"assistant","content":""}}]}`,
		``,
		`data: {"choices":[{"index":0,"delta":{"reasoning_content":"The user wants"}}]}`,
		``,
		`data: {"choices":[{"index":0,"delta":{"reasoning_content":" me to read"}}]}`,
		``,
		`data: {"choices":[{"index":0,"delta":{"content":"Trail "}}]}`,
		``,
		`data: {"choices":[{"index":0,"delta":{"content":"read."}}]}`,
		``,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"pa"}}]}}]}`,
		``,
		`data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"th\":\"a.go\"}"}}]}}]}`,
		``,
		`data: {"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`,
		``,
		`data: [DONE]`,
	}, "\n")

	fixture := map[string]interface{}{"reply": raw}
	_, got := NormalizePayload(nil, fixture)

	if got["reply"] != "Trail read." {
		t.Errorf("reply 聚合错误: %q", got["reply"])
	}
	if got["reasoning_content"] != "The user wants me to read" {
		t.Errorf("reasoning_content 聚合错误: %q", got["reasoning_content"])
	}
	if got["raw_stream"] != raw {
		t.Errorf("原始流必须完整保留")
	}

	tcs, ok := got["tool_calls"].([]map[string]interface{})
	if !ok || len(tcs) != 1 {
		t.Fatalf("tool_calls 聚合失败: %v", got["tool_calls"])
	}
	fn := tcs[0]["function"].(map[string]interface{})
	if fn["name"] != "read_file" || fn["arguments"] != `{"path":"a.go"}` {
		t.Errorf("tool_calls 参数拼接错误: %v", fn)
	}
	if tcs[0]["id"] != "call_1" {
		t.Errorf("tool id 丢失: %v", tcs[0]["id"])
	}
	if _, ok := got["usage"]; !ok {
		t.Errorf("usage 未保留")
	}
}

// Anthropic SSE：text_delta + thinking_delta + input_json_delta
func TestNormalizeResponse_AnthropicSSE(t *testing.T) {
	raw := strings.Join([]string{
		`event: content_block_start`,
		`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"bash"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"想一想"}}`,
		`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"cmd\":\"ls\"}"}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"搞定"}}`,
	}, "\n")

	_, got := NormalizePayload(nil, map[string]interface{}{"reply": raw})
	if got["reasoning_content"] != "想一想" {
		t.Errorf("thinking 聚合错误: %q", got["reasoning_content"])
	}
	if got["reply"] != "搞定" {
		t.Errorf("text 聚合错误: %q", got["reply"])
	}
	tcs, ok := got["tool_calls"].([]map[string]interface{})
	if !ok || len(tcs) != 1 {
		t.Fatalf("anthropic tool_use 聚合失败: %v", got["tool_calls"])
	}
	fn := tcs[0]["function"].(map[string]interface{})
	if fn["name"] != "bash" || fn["arguments"] != `{"cmd":"ls"}` {
		t.Errorf("anthropic tool 参数错误: %v", fn)
	}
}

// 已经干净的响应不得被二次改写
func TestNormalizeResponse_Idempotent(t *testing.T) {
	fixture := map[string]interface{}{
		"reply":             "已经很干净的回复",
		"reasoning_content": "已有思维链",
	}
	_, got := NormalizePayload(nil, fixture)
	if got["reply"] != "已经很干净的回复" {
		t.Errorf("干净回复被改写: %v", got["reply"])
	}
	if _, ok := got["raw_stream"]; ok {
		t.Errorf("非 SSE 不应写入 raw_stream")
	}
}

// 非流式请求: 上游返回完整 OpenAI JSON, 必须能抽出 reply / reasoning / tool_calls
func TestNormalizeResponse_NonStreamingOpenAI(t *testing.T) {
	fixture := map[string]interface{}{
		"id":     "chatcmpl-6adc7be41d517",
		"object": "chat.completion",
		"model":  "deepseek-v4-pro",
		"choices": []interface{}{
			map[string]interface{}{
				"index":         0,
				"finish_reason": "stop",
				"message": map[string]interface{}{
					"role":              "assistant",
					"content":           "通了",
					"reasoning_content": "用户只要两个字",
				},
			},
		},
		"usage": map[string]interface{}{"prompt_tokens": 18108.0, "total_tokens": 19135.0},
	}

	_, got := NormalizePayload(nil, fixture)
	if got["reply"] != "通了" {
		t.Errorf("非流式 reply 抽取失败: %v", got["reply"])
	}
	if got["reasoning_content"] != "用户只要两个字" {
		t.Errorf("非流式 reasoning 抽取失败: %v", got["reasoning_content"])
	}
	if _, ok := got["usage"]; !ok {
		t.Errorf("usage 必须保留")
	}
	if _, ok := fixture["reply"]; ok {
		t.Errorf("不应原地修改入参")
	}
}

// 非流式 OpenAI: tool_calls 也要抽出来
func TestNormalizeResponse_NonStreamingToolCalls(t *testing.T) {
	fixture := map[string]interface{}{
		"choices": []interface{}{
			map[string]interface{}{
				"message": map[string]interface{}{
					"role":    "assistant",
					"content": nil,
					"tool_calls": []interface{}{
						map[string]interface{}{
							"id":   "call_abc",
							"type": "function",
							"function": map[string]interface{}{
								"name":      "read_file",
								"arguments": `{"path":"a.go"}`,
							},
						},
					},
				},
			},
		},
	}

	_, got := NormalizePayload(nil, fixture)
	tcs, ok := got["tool_calls"].([]map[string]interface{})
	if !ok || len(tcs) != 1 {
		t.Fatalf("tool_calls 抽取失败: %v", got["tool_calls"])
	}
	if tcs[0]["id"] != "call_abc" {
		t.Errorf("tool id 丢失: %v", tcs[0]["id"])
	}
}

// 非流式 Anthropic: content block 数组
func TestNormalizeResponse_NonStreamingAnthropic(t *testing.T) {
	fixture := map[string]interface{}{
		"type": "message",
		"content": []interface{}{
			map[string]interface{}{"type": "thinking", "thinking": "想一下"},
			map[string]interface{}{"type": "text", "text": "答案在这里"},
			map[string]interface{}{"type": "tool_use", "id": "toolu_1", "name": "bash",
				"input": map[string]interface{}{"cmd": "ls"}},
		},
	}

	_, got := NormalizePayload(nil, fixture)
	if got["reply"] != "答案在这里" {
		t.Errorf("anthropic text 抽取失败: %v", got["reply"])
	}
	if got["reasoning_content"] != "想一下" {
		t.Errorf("anthropic thinking 抽取失败: %v", got["reasoning_content"])
	}
	tcs, ok := got["tool_calls"].([]map[string]interface{})
	if !ok || len(tcs) != 1 {
		t.Fatalf("anthropic tool_use 抽取失败: %v", got["tool_calls"])
	}
	fn := tcs[0]["function"].(map[string]interface{})
	if fn["name"] != "bash" || fn["arguments"] != `{"cmd":"ls"}` {
		t.Errorf("anthropic 工具参数错误: %v", fn)
	}
}

// 无法识别的响应体必须原样透出，不能凭空造 reply
func TestNormalizeResponse_UnknownShapeUntouched(t *testing.T) {
	fixture := map[string]interface{}{"provider": "a6api", "foo": "bar"}
	_, got := NormalizePayload(nil, fixture)
	if _, ok := got["reply"]; ok {
		t.Errorf("不应给未知结构强塞 reply")
	}
	if got["provider"] != "a6api" {
		t.Errorf("原字段丢失")
	}
}
