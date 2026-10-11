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
