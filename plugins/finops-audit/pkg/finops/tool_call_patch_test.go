package finops

import (
	"strings"
	"testing"
)

// 一个典型的、被 Higress ai-proxy 阉割后的"空壳 chunk"
const malformedShellChunk = `{"id":"chatcmpl-test-123","choices":[{"index":0,"delta":{},"finish_reason":null}],"model":"deepseek-v4.1-flash-1017"}`

// Gemini 原始返回中的 functionCall
const rawGeminiSnippet = `{"candidates":[{"content":{"parts":[{"functionCall":{"name":"execute_code","args":{"code":"import subprocess\nprint(1)"}}}]},"finishReason":"STOP"}]}`

// ---------------------------------------------------------------------------
// RepairMalformedChunk: 单事件修复
// ---------------------------------------------------------------------------

// 1) 原始回归场景: 空壳 chunk + 旁路 Gemini 报文
func TestRepairMalformedChunk_GeminiFunctionCallFromSnippet(t *testing.T) {
	repaired, modified := RepairMalformedChunk(malformedShellChunk, rawGeminiSnippet)
	if !modified {
		t.Fatalf("expected chunk to be repaired, but modified=false")
	}
	if !strings.Contains(repaired, `"name":"execute_code"`) {
		t.Errorf("missing function name: %s", repaired)
	}
	if !strings.Contains(repaired, `"arguments":"{\"code\":\"import subprocess\\nprint(1)\"}"`) {
		t.Errorf("missing/invalid arguments: %s", repaired)
	}
	if !strings.Contains(repaired, `"finish_reason":"tool_calls"`) {
		t.Errorf("missing finish_reason tool_calls: %s", repaired)
	}
}

// 2) functionCall 直接内嵌在同一个事件里
func TestRepairMalformedChunk_GeminiFunctionCallInline(t *testing.T) {
	chunk := `{"id":"c1","choices":[{"index":0,"delta":{},"finish_reason":null}],"candidates":[{"content":{"parts":[{"functionCall":{"name":"get_weather","args":{"city":"Beijing"}}}]}}]}`
	repaired, modified := RepairMalformedChunk(chunk, "")
	if !modified {
		t.Fatalf("expected inline functionCall to be repaired")
	}
	if !strings.Contains(repaired, `"name":"get_weather"`) || !strings.Contains(repaired, `"city":"Beijing"`) {
		t.Errorf("inline functionCall not recovered: %s", repaired)
	}
}

// 3) OpenAI legacy function_call (delta.function_call) 应被转换成 tool_calls
func TestRepairMalformedChunk_OpenAILegacyFunctionCall(t *testing.T) {
	chunk := `{"id":"c2","choices":[{"index":0,"delta":{"function_call":{"name":"lookup","arguments":"{\"q\":\"x\"}"}},"finish_reason":null}]}`
	repaired, modified := RepairMalformedChunk(chunk, "")
	if !modified {
		t.Fatalf("expected legacy function_call to be converted")
	}
	if !strings.Contains(repaired, `"tool_calls"`) || !strings.Contains(repaired, `"name":"lookup"`) {
		t.Errorf("legacy function_call not converted: %s", repaired)
	}
	if !strings.Contains(repaired, `"finish_reason":"tool_calls"`) {
		t.Errorf("finish_reason not set: %s", repaired)
	}
}

// 4) 非流式的 message.tool_calls 被误塞进流式事件
func TestRepairMalformedChunk_MisplacedMessageToolCalls(t *testing.T) {
	chunk := `{"id":"c3","choices":[{"index":0,"delta":{},"finish_reason":null,"message":{"tool_calls":[{"id":"x","type":"function","function":{"name":"do_it","arguments":"{}"}}]}}]}`
	repaired, modified := RepairMalformedChunk(chunk, "")
	if !modified {
		t.Fatalf("expected misplaced message.tool_calls to be relocated")
	}
	if !strings.Contains(repaired, `"name":"do_it"`) {
		t.Errorf("misplaced tool call not relocated: %s", repaired)
	}
}

// 5) 已带真实 tool_calls 的事件 -> 幂等不改
func TestRepairMalformedChunk_AlreadyHasToolCalls(t *testing.T) {
	chunk := `{"id":"c4","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"type":"function","function":{"name":"ok","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}`
	repaired, modified := RepairMalformedChunk(chunk, "")
	if modified || repaired != chunk {
		t.Fatalf("valid tool_calls chunk must be untouched, got modified=%v out=%s", modified, repaired)
	}
}

// 6) 正常 content 事件 -> 不改
func TestRepairMalformedChunk_ContentChunkUntouched(t *testing.T) {
	chunk := `{"id":"c5","choices":[{"index":0,"delta":{"content":"你好"},"finish_reason":null}]}`
	if _, modified := RepairMalformedChunk(chunk, ""); modified {
		t.Fatalf("content chunk must not be modified")
	}
}

// 7) role 起始事件 -> 不改
func TestRepairMalformedChunk_RoleChunkUntouched(t *testing.T) {
	chunk := `{"id":"c6","choices":[{"index":0,"delta":{"role":"assistant"},"finish_reason":null}]}`
	if _, modified := RepairMalformedChunk(chunk, ""); modified {
		t.Fatalf("role chunk must not be modified")
	}
}

// 8) 空壳但没有任何可恢复信息 -> 不改 (无权凭空造工具调用)
func TestRepairMalformedChunk_ShellWithoutRecovery(t *testing.T) {
	if _, modified := RepairMalformedChunk(malformedShellChunk, ""); modified {
		t.Fatalf("shell without recoverable info must not be modified")
	}
}

// 9) 非法 JSON -> 原样返回
func TestRepairMalformedChunk_InvalidJSON(t *testing.T) {
	in := `{not json`
	out, modified := RepairMalformedChunk(in, rawGeminiSnippet)
	if modified || out != in {
		t.Fatalf("invalid json must be returned untouched")
	}
}

// 10) usage-only 事件 (choices 为空) -> 不改
func TestRepairMalformedChunk_UsageOnlyChunk(t *testing.T) {
	chunk := `{"id":"c7","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`
	if _, modified := RepairMalformedChunk(chunk, rawGeminiSnippet); modified {
		t.Fatalf("usage-only chunk must not be modified")
	}
}

// 11) 非流式结构 (无 delta) -> 不改
func TestRepairMalformedChunk_NonStreamShape(t *testing.T) {
	chunk := `{"id":"c8","choices":[{"index":0,"message":{"content":"hi"},"finish_reason":"stop"}]}`
	if _, modified := RepairMalformedChunk(chunk, rawGeminiSnippet); modified {
		t.Fatalf("non-stream shape must not be modified")
	}
}

// ---------------------------------------------------------------------------
// RepairSSEChunk: 流式 chunk 修复
// ---------------------------------------------------------------------------

// 12) 混合事件: 只修空壳, 正常 content / [DONE] 原样
func TestRepairSSEChunk_MixedEvents(t *testing.T) {
	sse := "data: " + `{"id":"a","choices":[{"index":0,"delta":{"content":"你"},"finish_reason":null}]}` + "\n\n" +
		"data: " + malformedShellChunk + "\n\n" +
		"data: [DONE]\n\n"

	out := string(RepairSSEChunk([]byte(sse), rawGeminiSnippet))
	if !strings.Contains(out, `"content":"你"`) {
		t.Errorf("normal content lost: %s", out)
	}
	if !strings.Contains(out, `"name":"execute_code"`) {
		t.Errorf("shell not repaired: %s", out)
	}
	if !strings.Contains(out, "data: [DONE]") {
		t.Errorf("[DONE] lost: %s", out)
	}
	// 只应出现一次 finish_reason:tool_calls (即只修了空壳那一条)
	if strings.Count(out, `"finish_reason":"tool_calls"`) != 1 {
		t.Errorf("expected exactly one repaired event, got: %s", out)
	}
}

// 13) 无 data: 的 chunk -> 快速原样返回
func TestRepairSSEChunk_NoDataFastPath(t *testing.T) {
	in := []byte(": keep-alive comment\n\n")
	if got := RepairSSEChunk(in, ""); string(got) != string(in) {
		t.Fatalf("non-data chunk must pass through untouched")
	}
}

// 14) 无任何可修复事件 -> 返回原 slice (不做拷贝)
func TestRepairSSEChunk_NothingToRepair(t *testing.T) {
	in := []byte("data: {\"id\":\"a\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\n")
	got := RepairSSEChunk(in, "")
	if &got[0] != &in[0] {
		t.Fatalf("expected original slice to be returned when nothing to repair")
	}
}

// 15) CRLF 行尾保持
func TestRepairSSEChunk_PreservesCRLF(t *testing.T) {
	sse := "data: " + malformedShellChunk + "\r\n\r\n"
	out := string(RepairSSEChunk([]byte(sse), rawGeminiSnippet))
	if !strings.Contains(out, "\r\n\r\n") {
		t.Fatalf("CRLF not preserved: %q", out)
	}
}

// 16) 从旁路上下文 (rawContext) 回溯工具调用
func TestRepairSSEChunk_ContextRecovery(t *testing.T) {
	shellOnly := "data: " + malformedShellChunk + "\n\n"
	out := string(RepairSSEChunk([]byte(shellOnly), rawGeminiSnippet))
	if !strings.Contains(out, `"name":"execute_code"`) {
		t.Fatalf("failed to recover tool call from rawContext: %s", out)
	}
}
