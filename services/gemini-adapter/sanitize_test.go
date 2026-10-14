package main

import (
	"encoding/json"
	"strings"
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

func TestSanitizeGoogleTurns_ModelFirstTurn(t *testing.T) {
	// 测试：如果输入的第一轮就是 model（历史截断），必须自动在最前方插入 user turn
	input := []GoogleContent{
		{
			Role:  "model",
			Parts: []GooglePart{{Text: "I am an assistant"}},
		},
		{
			Role:  "user",
			Parts: []GooglePart{{Text: "Hi"}},
		},
	}
	sanitized := sanitizeGoogleTurns(input)
	if len(sanitized) < 2 {
		t.Fatalf("expected at least 2 turns, got %d", len(sanitized))
	}
	if sanitized[0].Role != "user" {
		t.Fatalf("expected first turn to be user, got %s", sanitized[0].Role)
	}
}

func TestSanitizeGoogleTurns_MixedTextAndFunctionResponse(t *testing.T) {
	// 测试：同一个 user turn 里混杂了 Text 和 FunctionResponse（例如 OpenCode 伴随提示），且前置有 FunctionCall
	input := []GoogleContent{
		{
			Role:  "user",
			Parts: []GooglePart{{Text: "list files"}},
		},
		{
			Role: "model",
			Parts: []GooglePart{
				{
					FunctionCall: &GoogleFunctionCall{
						Name: "glob",
						Args: map[string]interface{}{"pattern": "*"},
					},
				},
			},
		},
		{
			Role: "user",
			Parts: []GooglePart{
				{Text: "here are the results:"},
				{
					FunctionResponse: &GoogleFunctionResponse{
						Name:     "glob",
						Response: map[string]interface{}{"files": []string{"a.go"}},
					},
				},
			},
		},
	}

	sanitized := sanitizeGoogleTurns(input)
	// 验证：Google 会要求 model(call) 紧随包含 functionResponse 的 turn
	for i := 0; i < len(sanitized); i++ {
		if sanitized[i].Role == "user" {
			for _, p := range sanitized[i].Parts {
				if p.FunctionResponse != nil {
					// 必须前一个是 model，且 model 里有匹配的 call
					if i == 0 || sanitized[i-1].Role != "model" {
						t.Fatalf("turn %d has functionResponse but previous turn is not model", i)
					}
				}
			}
		}
	}
}

func TestParsePseudoToolCall(t *testing.T) {
	// 验证：能够正确解析模型幻觉出来的伪工具调用字符串
	s1 := `[Assistant invoked tool bash with args: {"command":"echo hello"}]`
	fn, args, ok := parsePseudoToolCall(s1)
	if !ok || fn != "bash" || args != `{"command":"echo hello"}` {
		t.Fatalf("failed parsing s1: fn=%s, args=%s, ok=%v", fn, args, ok)
	}

	s2 := `Assistant invoked tool bash with args: {"command":"ls -la"}`
	fn, args, ok = parsePseudoToolCall(s2)
	if !ok || fn != "bash" || args != `{"command":"ls -la"}` {
		t.Fatalf("failed parsing s2: fn=%s, args=%s, ok=%v", fn, args, ok)
	}

	s3 := `I called tool grep with arguments: {"path":"/home/gateman/test","pattern":"test"}`
	fn, args, ok = parsePseudoToolCall(s3)
	if !ok || fn != "grep" || args != `{"path":"/home/gateman/test","pattern":"test"}` {
		t.Fatalf("failed parsing s3: fn=%s, args=%s, ok=%v", fn, args, ok)
	}

	sNested := `I executed edit with input: {"filePath":"services/gemini-adapter/main.go","newString":"if foo == \"bar\" {\n\tcontinue\n}","oldString":"if foo == \"bar\" {\n}"}`
	fn, args, ok = parsePseudoToolCall(sNested)
	if !ok || fn != "edit" || !strings.Contains(args, "newString") || !strings.Contains(args, "oldString") {
		t.Fatalf("failed parsing sNested: fn=%s, args=%s, ok=%v", fn, args, ok)
	}

	sNullPrefix := `nullI executed exec with input: {"command":"pwd"}`
	fn, args, ok = parsePseudoToolCall(sNullPrefix)
	if !ok || fn != "exec" || args != `{"command":"pwd"}` {
		t.Fatalf("failed parsing sNullPrefix: fn=%s, args=%s, ok=%v", fn, args, ok)
	}

	// 验证：包含非标 \' 转义的 Python/Shell 复杂命令能被正确净化解析 (Alice 故障场景)
	sSingleQuote := `nullI executed write with input: {"content":"subprocess.call(['ssh', \'upload\'])", "path":"test.py"}`
	fn, args, ok = parsePseudoToolCall(sSingleQuote)
	if !ok || fn != "write" || !strings.Contains(args, "subprocess.call") {
		t.Fatalf("failed parsing sSingleQuote: fn=%s, args=%s, ok=%v", fn, args, ok)
	}

	// 验证：包含 \x27 十六进制转义的复杂 Shell 命令 (Alice 故障场景)
	sHexEscape := `nullI executed exec with input: {"command":"curl -H 'Authorization: alist-xxx' --data-binary \x27@file.rmvb\x27"}`
	fn, args, ok = parsePseudoToolCall(sHexEscape)
	if !ok || fn != "exec" || !strings.Contains(args, "alist-xxx") {
		t.Fatalf("failed parsing sHexEscape: fn=%s, args=%s, ok=%v", fn, args, ok)
	}

	// 验证：包含前置自然语言回复的混合文本能精准切分
	sMixed := "收到 Boss! 马上为您处理！💋\n\nnullI executed exec with input: {\"command\":\"uptime\"}"
	convPrefix, fn, args, ok := extractConversationalAndPseudoToolCall(sMixed)
	if !ok || fn != "exec" || convPrefix != "收到 Boss! 马上为您处理！💋" || args != `{"command":"uptime"}` {
		t.Fatalf("failed parsing sMixed: convPrefix=%s, fn=%s, args=%s, ok=%v", convPrefix, fn, args, ok)
	}

	s4 := `Just normal conversational text from model.`
	_, _, ok = parsePseudoToolCall(s4)
	if ok {
		t.Fatalf("s4 should not match pseudo tool call")
	}

	// 验证：Alice 真实报错场景中的 Python 脚本（包含多种复杂转义与嵌套引号）
	sAliceS2 := `nullI executed ` + "`" + `write` + "`" + ` with input: {"content":"import subprocess\nTOKEN = \"alist-xxx\"\nupload_cmd = f'''curl -s -X PUT \"http://localhost:5244/api/fs/put\" \\\n -H \"Authorization: {TOKEN}\" \\\n --data-binary \"@{starfive_path}\"'''\nsubprocess.check_call(['ssh', 'gateman@10.0.1.227', \'{upload_cmd}\'])","path":"/home/gateman/.openclaw/workspace/scripts/sync_initial_d_season2.py"}`
	fn, args, ok = parsePseudoToolCall(sAliceS2)
	if !ok || fn != "write" {
		t.Fatalf("failed parsing sAliceS2: fn=%s, ok=%v", fn, ok)
	}
	var s2Args map[string]interface{}
	if err := json.Unmarshal([]byte(args), &s2Args); err != nil {
		t.Fatalf("sAliceS2 args invalid json: %v", err)
	}
	if s2Args["path"] != "/home/gateman/.openclaw/workspace/scripts/sync_initial_d_season2.py" {
		t.Fatalf("unexpected sAliceS2 path: %v", s2Args["path"])
	}
}

func TestMissingSigPairwiseDegrade(t *testing.T) {
	// 验证：当缺失 thought_signature 时，成对降级为文本后的时序通过 sanitizeGoogleTurns 且无孤儿 FunctionResponse
	input := []GoogleContent{
		{
			Role:  "user",
			Parts: []GooglePart{{Text: "check system"}},
		},
		{
			Role:  "model",
			Parts: []GooglePart{{Text: "I called tool default_api:bash with arguments: {\"command\":\"uptime\"}"}},
		},
		{
			Role:  "user",
			Parts: []GooglePart{{Text: "Tool result for default_api:bash:\n14:20:00 up 10 days"}},
		},
		{
			Role:  "user",
			Parts: []GooglePart{{Text: "what is the uptime?"}},
		},
	}

	sanitized := sanitizeGoogleTurns(input)
	if len(sanitized) != 3 {
		t.Fatalf("expected 3 turns after user merge, got %d", len(sanitized))
	}
	if sanitized[0].Role != "user" || sanitized[1].Role != "model" || sanitized[2].Role != "user" {
		t.Fatalf("expected user -> model -> user alternating turns, got: %v, %v, %v",
			sanitized[0].Role, sanitized[1].Role, sanitized[2].Role)
	}
	// 必须无任何孤儿 FunctionResponse
	for _, c := range sanitized {
		for _, p := range c.Parts {
			if p.FunctionResponse != nil {
				t.Fatalf("expected no FunctionResponse in degraded turn")
			}
		}
	}
}

func TestFindActiveAssistantIdx(t *testing.T) {
	// 场景 1：以 User 提问结尾（典型的用户提问场景，例如"检查一下我的支出表"）
	// 所有前序工具调用均为历史旧账，必须返回 -1，0 次 Redis 探测
	msgs1 := []OpenAIMessage{
		{Role: "user", Content: "step 1"},
		{Role: "assistant", ToolCalls: []OpenAIToolCall{{ID: "call_1", Function: OpenAIFunctionCallDesc{Name: "bash"}}}},
		{Role: "tool", ToolCallID: "call_1", Content: "ok"},
		{Role: "assistant", Content: "done 1"},
		{Role: "user", Content: "检查一下我的支出表"},
	}
	if idx := findActiveAssistantIdx(msgs1); idx != -1 {
		t.Fatalf("expected -1 for user-ending conversation, got %d", idx)
	}

	// 场景 2：以 Tool 响应结尾（正在执行的 ReAct 循环末端）
	// 只有倒数第二个包含待响应 tool_calls 的 assistant 消息才算活跃
	msgs2 := []OpenAIMessage{
		{Role: "user", Content: "step 1"},
		{Role: "assistant", ToolCalls: []OpenAIToolCall{{ID: "call_old", Function: OpenAIFunctionCallDesc{Name: "read"}}}},
		{Role: "tool", ToolCallID: "call_old", Content: "old content"},
		{Role: "user", Content: "step 2"},
		{Role: "assistant", ToolCalls: []OpenAIToolCall{{ID: "call_active", Function: OpenAIFunctionCallDesc{Name: "bash"}}}},
		{Role: "tool", ToolCallID: "call_active", Content: "new content"},
	}
	if idx := findActiveAssistantIdx(msgs2); idx != 4 {
		t.Fatalf("expected 4 for active tool response turn, got %d", idx)
	}

	// 场景 3：末尾 assistant 包含并发多工具调用且正在返回多个 tool response
	msgs3 := []OpenAIMessage{
		{Role: "assistant", ToolCalls: []OpenAIToolCall{
			{ID: "call_a", Function: OpenAIFunctionCallDesc{Name: "read"}},
			{ID: "call_b", Function: OpenAIFunctionCallDesc{Name: "grep"}},
		}},
		{Role: "tool", ToolCallID: "call_a", Content: "res_a"},
		{Role: "tool", ToolCallID: "call_b", Content: "res_b"},
	}
	if idx := findActiveAssistantIdx(msgs3); idx != 0 {
		t.Fatalf("expected 0 for parallel active tool turn, got %d", idx)
	}
}
