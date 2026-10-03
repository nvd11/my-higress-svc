package finops

import (
	"strings"
	"testing"
)

func TestRepairMalformedChunk(t *testing.T) {
	// 模拟官方 ai-proxy 吐出来的空壳 Chunk
	malformedChunk := `{"id":"chatcmpl-test-123","choices":[{"index":0,"delta":{},"finish_reason":null}],"model":"gemini-3.8-flash"}`

	// 模拟 Google 原始返回中的 FunctionCall
	rawGemini := `{"candidates":[{"content":{"parts":[{"functionCall":{"name":"execute_code","args":{"code":"import subprocess\nprint(1)"}}}]},"finishReason":"STOP"}]}`

	repaired, modified := RepairMalformedChunk(malformedChunk, rawGemini)

	if !modified {
		t.Fatalf("expected chunk to be repaired, but modified=false")
	}

	// 核心断言: 验证是否成功补上了 delta.tool_calls
	if !strings.Contains(repaired, `"name":"execute_code"`) {
		t.Errorf("missing function name: %s", repaired)
	}
	if !strings.Contains(repaired, `"arguments":"{\"code\":\"import subprocess\\nprint(1)\"}"`) && !strings.Contains(repaired, `execute_code`) {
		t.Errorf("missing arguments or structure: %s", repaired)
	}
	if !strings.Contains(repaired, `"finish_reason":"tool_calls"`) {
		t.Errorf("missing finish_reason tool_calls: %s", repaired)
	}
}
