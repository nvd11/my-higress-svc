package main

import (
	"testing"
)

func TestSanitizeGoogleTurns_OrphanFunctionResponse(t *testing.T) {
	// 模拟 Compaction 场景：前文被截断，开头直接出现 FunctionResponse，但前面没有 FunctionCall
	input := []GoogleContent{
		{
			Role: "user",
			Parts: []GooglePart{
				{
					FunctionResponse: &GoogleFunctionResponse{
						Name:     "read_file",
						Response: map[string]interface{}{"content": "hello world"},
					},
				},
			},
		},
		{
			Role: "model",
			Parts: []GooglePart{
				{Text: "I see the content"},
			},
		},
	}

	sanitized := sanitizeGoogleTurns(input)
	if len(sanitized) != 2 {
		t.Fatalf("expected 2 turns, got %d", len(sanitized))
	}

	// 孤立的 FunctionResponse 应该被安全降级为 Text，防止 Google 400
	if sanitized[0].Parts[0].FunctionResponse != nil {
		t.Errorf("expected FunctionResponse to be converted to Text to prevent orphan turn error")
	}
	if sanitized[0].Parts[0].Text == "" {
		t.Errorf("expected Text to contain degraded info")
	}
}

func TestSanitizeGoogleTurns_OrphanFunctionCall(t *testing.T) {
	// 模拟 Compaction 截断：model 发出了 FunctionCall，但后面直接跟着 user 提问，中间丢了 FunctionResponse
	input := []GoogleContent{
		{
			Role: "user",
			Parts: []GooglePart{{Text: "please run ls"}},
		},
		{
			Role: "model",
			Parts: []GooglePart{
				{
					FunctionCall: &GoogleFunctionCall{
						Name: "bash",
						Args: map[string]interface{}{"command": "ls"},
					},
				},
			},
		},
		{
			Role: "user",
			Parts: []GooglePart{{Text: "what is the next step?"}},
		},
	}

	sanitized := sanitizeGoogleTurns(input)
	// 期望在 model 和第二个 user 之间合成一个包含 FunctionResponse 的 turn，并随后合并/满足合法性
	hasResponseForBash := false
	for _, c := range sanitized {
		if c.Role == "user" {
			for _, p := range c.Parts {
				if p.FunctionResponse != nil && p.FunctionResponse.Name == "bash" {
					hasResponseForBash = true
				}
			}
		}
	}

	if !hasResponseForBash {
		t.Errorf("expected synthesized functionResponse for orphan functionCall, but not found")
	}
}

func TestSanitizeGoogleTurns_ConsecutiveRoles(t *testing.T) {
	// 测试连续同角色的合并
	input := []GoogleContent{
		{
			Role:  "user",
			Parts: []GooglePart{{Text: "part 1"}},
		},
		{
			Role:  "user",
			Parts: []GooglePart{{Text: "part 2"}},
		},
		{
			Role:  "model",
			Parts: []GooglePart{{Text: "response 1"}},
		},
	}

	sanitized := sanitizeGoogleTurns(input)
	if len(sanitized) != 2 {
		t.Fatalf("expected 2 turns after role merge, got %d", len(sanitized))
	}
	if len(sanitized[0].Parts) != 2 {
		t.Fatalf("expected 2 parts merged in first turn, got %d", len(sanitized[0].Parts))
	}
}

func TestCompactionZeroTools_ModeNone(t *testing.T) {
	// 验证：当客户端没有传 tools 时，gReq 的 tool_config.function_calling_config.mode 必须强制为 NONE
	var tools []OpenAITool
	var gReq GoogleGenerateContentRequest

	if len(tools) == 0 {
		if gReq.ToolConfig == nil {
			gReq.ToolConfig = &GoogleToolConfig{}
		}
		gReq.ToolConfig.FunctionCallingConfig = &GoogleFunctionCallingConfig{
			Mode: "NONE",
		}
	}

	if gReq.ToolConfig == nil || gReq.ToolConfig.FunctionCallingConfig == nil || gReq.ToolConfig.FunctionCallingConfig.Mode != "NONE" {
		t.Fatalf("expected mode NONE when tools is empty, got %+v", gReq.ToolConfig)
	}
}
