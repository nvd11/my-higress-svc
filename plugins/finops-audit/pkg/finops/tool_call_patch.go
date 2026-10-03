package finops

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// RepairMalformedChunk 检查并修复被 Higress 阉割掉 tool_calls 的残缺 SSE Chunk
func RepairMalformedChunk(rawChunkJSON string, rawGeminiSnippet string) (repairedJSON string, modified bool) {
	// 如果原始数据里根本没有包含 functionCall，无需修复
	if !strings.Contains(rawGeminiSnippet, "functionCall") && !strings.Contains(rawChunkJSON, "functionCall") {
		return rawChunkJSON, false
	}

	parsed := gjson.Parse(rawChunkJSON)
	// 检查 choices[0].delta 是否缺少 content 且缺少 tool_calls
	content := parsed.Get("choices.0.delta.content").String()
	toolCalls := parsed.Get("choices.0.delta.tool_calls")

	if content == "" && (!toolCalls.Exists() || len(toolCalls.Array()) == 0) {
		// 从源报文中提取 functionCall
		source := rawGeminiSnippet
		if !strings.Contains(source, "functionCall") {
			source = rawChunkJSON
		}

		gParsed := gjson.Parse(source)
		fnCall := gParsed.Get("candidates.0.content.parts.0.functionCall")
		if !fnCall.Exists() {
			fnCall = gParsed.Get("functionCall")
		}

		if fnCall.Exists() {
			fnName := fnCall.Get("name").String()
			fnArgs := fnCall.Get("args").Raw
			if fnArgs == "" {
				fnArgs = "{}"
			}

			callID := fmt.Sprintf("call_%s", uuid.NewString()[:8])
			toolCallObj := map[string]interface{}{
				"id":    callID,
				"type":  "function",
				"index": 0,
				"function": map[string]string{
					"name":      fnName,
					"arguments": fnArgs,
				},
			}

			toolCallBytes, _ := json.Marshal([]interface{}{toolCallObj})

			// 动态注入 delta.tool_calls 并将 finish_reason 设为 tool_calls
			res, _ := sjson.SetRaw(rawChunkJSON, "choices.0.delta.tool_calls", string(toolCallBytes))
			res, _ = sjson.Set(res, "choices.0.finish_reason", "tool_calls")
			return res, true
		}
	}

	return rawChunkJSON, false
}
