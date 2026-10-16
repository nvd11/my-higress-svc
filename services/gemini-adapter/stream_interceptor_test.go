package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestExtractBalancedJSON 详尽验证嵌套花括号、转义引号、代码块等复杂 JSON 结构的平衡提取能力
func TestExtractBalancedJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
		ok       bool
	}{
		{
			name:     "simple flat json",
			input:    `{"command": "uptime"}`,
			expected: `{"command": "uptime"}`,
			ok:       true,
		},
		{
			name:     "prefix before json",
			input:    `prefix text {"command": "uptime"}`,
			expected: `{"command": "uptime"}`,
			ok:       true,
		},
		{
			name:     "suffix after json",
			input:    `{"command": "uptime"} extra trailing text`,
			expected: `{"command": "uptime"}`,
			ok:       true,
		},
		{
			name:     "nested object braces",
			input:    `{"outer": {"inner": {"key": "val"}}}`,
			expected: `{"outer": {"inner": {"key": "val"}}}`,
			ok:       true,
		},
		{
			name:     "code with braces and newlines inside string (edit tool payload)",
			input:    `{"filePath": "main.go", "newString": "if a == 1 {\n\treturn nil\n}", "oldString": "if a == 1 {}"}`,
			expected: `{"filePath": "main.go", "newString": "if a == 1 {\n\treturn nil\n}", "oldString": "if a == 1 {}"}`,
			ok:       true,
		},
		{
			name:     "escaped quotes inside string",
			input:    `{"command": "echo \"{hello}\""}`,
			expected: `{"command": "echo \"{hello}\""}`,
			ok:       true,
		},
		{
			name:     "escaped backslashes followed by quote",
			input:    `{"command": "path\\\"with\\\"braces{}"}`,
			expected: `{"command": "path\\\"with\\\"braces{}"}`,
			ok:       true,
		},
		{
			name:     "incomplete json (depth > 0)",
			input:    `{"command": "echo {1..10}`,
			expected: "",
			ok:       false,
		},
		{
			name:     "no braces at all",
			input:    `just some regular text without braces`,
			expected: "",
			ok:       false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := extractBalancedJSON(tc.input)
			if ok != tc.ok {
				t.Fatalf("expected ok=%v, got ok=%v (got: %s)", tc.ok, ok, got)
			}
			if ok && got != tc.expected {
				t.Fatalf("expected: %s, got: %s", tc.expected, got)
			}
			if ok {
				var dummy map[string]interface{}
				if err := json.Unmarshal([]byte(got), &dummy); err != nil {
					t.Fatalf("extracted json is not valid json: %v", err)
				}
			}
		})
	}
}

// TestParsePseudoToolCallMultilineBashWithLiteralNewlines 验证包含字面量换行符的多行 bash/curl 命令也能被完美解析
func TestParsePseudoToolCallMultilineBashWithLiteralNewlines(t *testing.T) {
	raw := "I executed `bash` with input: {\"command\":\"curl -s -X POST \\\"https://higress.jppwl.asia/v1/chat/completions\\\" \\\\\n  -H \\\"Authorization: Bearer sk-alice-higress-20261011-888888\\\" \\\\\n  -H \\\"Content-Type: application/json\\\" \\\\\n  -d '{\\n    \\\"model\\\": \\\"gemini-3.8-flash\\\",\\n    \\\"messages\\\": [{\\\"role\\\":\\\"user\\\",\\\"content\\\":\\\"hi\\\"}]\\n  }'\"}"

	name, args, ok := parsePseudoToolCall(raw)
	if !ok || name != "bash" {
		t.Fatalf("failed parsing multiline bash command: name=%s, ok=%v", name, ok)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal([]byte(args), &parsed); err != nil {
		t.Fatalf("args is not valid JSON: %v", err)
	}
	cmd, _ := parsed["command"].(string)
	if !strings.Contains(cmd, "curl -s -X POST") || !strings.Contains(cmd, "gemini-3.8-flash") {
		t.Fatalf("unexpected parsed command: %s", cmd)
	}
}

// TestStreamBufferFragmentedSimulation 模拟流式分片以极碎 Token 片段到达时，流式缓冲区的拦截与组装行为
func TestStreamBufferFragmentedSimulation(t *testing.T) {
	// 模拟流式依次吐出的 Token 片段
	fragments := []string{
		"I", " executed", " edit ", "with", " input:", " {",
		`"filePath":`, ` "services/gemini-adapter/main.go",`,
		` "newString":`, ` "if trimmedKey == \"default\" {\n\tcontinue\n}",`,
		` "oldString":`, ` "if trimmedKey == \"default\" {}"`,
		"}",
	}

	var pendingBuf strings.Builder
	var emittedToolName, emittedToolArgs string
	var leakedPlainText []string

	for _, frag := range fragments {
		if pendingBuf.Len() > 0 || isPossiblePseudoCallStart(frag) {
			pendingBuf.WriteString(frag)
			buffered := pendingBuf.String()
			if strings.Contains(buffered, "}") {
				if fnName, fnArgs, ok := parsePseudoToolCall(buffered); ok {
					emittedToolName = fnName
					emittedToolArgs = fnArgs
					pendingBuf.Reset()
					continue
				}
			}
			if !isPossiblePseudoCallStart(buffered) {
				leakedPlainText = append(leakedPlainText, pendingBuf.String())
				pendingBuf.Reset()
			}
			continue
		}
		leakedPlainText = append(leakedPlainText, frag)
	}

	// 最终断言：
	// 1. 绝对不能向客户端泄漏任何明文伪调用 Token！
	if len(leakedPlainText) > 0 {
		t.Fatalf("expected 0 leaked plain text fragments, but leaked: %v", leakedPlainText)
	}
	// 2. 必须成功捕获并升级为真正的 edit 工具调用！
	if emittedToolName != "edit" {
		t.Fatalf("expected emitted tool to be 'edit', got '%s'", emittedToolName)
	}
	// 3. 提取出的参数必须为合法 JSON 且包含完整字段！
	var parsedArgs map[string]interface{}
	if err := json.Unmarshal([]byte(emittedToolArgs), &parsedArgs); err != nil {
		t.Fatalf("emitted tool args is not valid JSON: %v, raw: %s", err, emittedToolArgs)
	}
	if parsedArgs["filePath"] != "services/gemini-adapter/main.go" {
		t.Fatalf("expected filePath 'services/gemini-adapter/main.go', got '%v'", parsedArgs["filePath"])
	}
	if !strings.Contains(parsedArgs["newString"].(string), "continue") {
		t.Fatalf("expected newString to contain 'continue', got '%v'", parsedArgs["newString"])
	}
}

// TestStreamBufferNormalTextNoLeakOrHanging 验证正常文字流式回复绝对不会被误拦截卡死
func TestStreamBufferNormalTextNoLeakOrHanging(t *testing.T) {
	normalFragments := []string{
		"I", " think", " the", " issue", " is", " resolved.", " Everything", " is", " working", " fine.",
	}

	var pendingBuf strings.Builder
	var emittedDeltas []string

	for _, frag := range normalFragments {
		if pendingBuf.Len() > 0 || isPossiblePseudoCallStart(frag) {
			pendingBuf.WriteString(frag)
			buffered := pendingBuf.String()
			if !isPossiblePseudoCallStart(buffered) {
				emittedDeltas = append(emittedDeltas, buffered)
				pendingBuf.Reset()
			}
			continue
		}
		emittedDeltas = append(emittedDeltas, frag)
	}
	if pendingBuf.Len() > 0 {
		emittedDeltas = append(emittedDeltas, pendingBuf.String())
	}

	// 最终组装的文本必须和原始文本 100% 一致，无任何遗漏或阻塞
	fullText := strings.Join(emittedDeltas, "")
	if fullText != "I think the issue is resolved. Everything is working fine." {
		t.Fatalf("normal text was mistakenly blocked: '%s'", fullText)
	}
}

// TestProcessStreamPseudoBuffer_ThinkingProcessTailSplit 验证 Thinking Process 与半截 I executed 同处一个分片时的精确剥离与拦截
func TestProcessStreamPseudoBuffer_ThinkingProcessTailSplit(t *testing.T) {
	chunks := []string{
		"Thinking Process:\n\nLet's run `CGO_ENABLED=0 go test -v .`, commit, push, watch the GitHub Actions build, and update the K3s deployment!\n\n\nI executed ",
		"`bash` with",
		" input: {\"command\":\"CGO_ENABLED=0 go test -v . && \\\\\\ngit -C /home/gateman/projects/github/my-higress-svc add services/gemini-adapter/main.go\",\"timeout\":30000,\"workdir\":\"/home/gateman/projects/github/my-higress-svc/services/gemini-adapter\"}\n",
	}

	var pendingBuf strings.Builder
	var outTexts []string
	var outCalls []ParsedPseudoCall

	for _, ch := range chunks {
		pendingBuf.WriteString(ch)
		texts, calls, rem := processStreamPseudoBuffer(pendingBuf.String(), true, false)
		pendingBuf.Reset()
		if rem != "" {
			pendingBuf.WriteString(rem)
		}
		outTexts = append(outTexts, texts...)
		outCalls = append(outCalls, calls...)
	}
	if pendingBuf.Len() > 0 {
		texts, calls, rem := processStreamPseudoBuffer(pendingBuf.String(), true, true)
		outTexts = append(outTexts, texts...)
		if rem != "" {
			outTexts = append(outTexts, rem)
		}
		outCalls = append(outCalls, calls...)
	}

	fullText := strings.Join(outTexts, "")
	if strings.Contains(fullText, "I executed") || strings.Contains(fullText, "workdir") {
		t.Fatalf("pseudo tool call leaked into plain text: %s", fullText)
	}
	if len(outCalls) != 1 || outCalls[0].Name != "bash" {
		t.Fatalf("expected 1 bash tool call, got: %+v", outCalls)
	}
}

// TestProcessStreamPseudoBuffer_MultipleParallelToolCallsAcrossChunks 验证单轮连续多个伪工具调用跨分片时不丢失后半段
func TestProcessStreamPseudoBuffer_MultipleParallelToolCallsAcrossChunks(t *testing.T) {
	chunks := []string{
		"Thinking Process:\nLet's check both in parallel!\n\nI executed `read` with input: {\"filePath\":\"/home/gateman/.config/opencode/users-it-assests/",
		"browser_automation.md\"}\nI executed `webfetch` with input: {\"format\":\"markdown\",\"url\":\"https://blog.csdn.net/",
		"nvd11/article/details/166495870\"}\n",
	}

	var pendingBuf strings.Builder
	var outTexts []string
	var outCalls []ParsedPseudoCall

	for _, ch := range chunks {
		pendingBuf.WriteString(ch)
		texts, calls, rem := processStreamPseudoBuffer(pendingBuf.String(), true, false)
		pendingBuf.Reset()
		if rem != "" {
			pendingBuf.WriteString(rem)
		}
		outTexts = append(outTexts, texts...)
		outCalls = append(outCalls, calls...)
	}
	if pendingBuf.Len() > 0 {
		texts, calls, rem := processStreamPseudoBuffer(pendingBuf.String(), true, true)
		outTexts = append(outTexts, texts...)
		if rem != "" {
			outTexts = append(outTexts, rem)
		}
		outCalls = append(outCalls, calls...)
	}

	fullText := strings.Join(outTexts, "")
	if strings.Contains(fullText, "browser_automation.md") || strings.Contains(fullText, "I executed") {
		t.Fatalf("expected 0 leaked tool call fragments in text, got: %s", fullText)
	}
	if len(outCalls) != 2 {
		t.Fatalf("expected 2 tool calls (read + webfetch), got %d: %+v", len(outCalls), outCalls)
	}
	if outCalls[0].Name != "read" || outCalls[1].Name != "webfetch" {
		t.Fatalf("unexpected tool names: %+v", outCalls)
	}
}
