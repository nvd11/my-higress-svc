package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

// OpenAIResponsesRequest represents the payload sent by OpenAI Codex CLI (wire_api = "responses")
type OpenAIResponsesRequest struct {
	Model        string        `json:"model"`
	Instructions string        `json:"instructions,omitempty"`
	Input        interface{}   `json:"input"`
	Tools        []interface{} `json:"tools,omitempty"`
	Stream       bool          `json:"stream,omitempty"`
	Temperature  *float64      `json:"temperature,omitempty"`
	MaxTokens    *int          `json:"max_output_tokens,omitempty"`
}

// convertResponsesToChatRequest converts a /v1/responses request into a standard OpenAIChatRequest
func convertResponsesToChatRequest(rReq OpenAIResponsesRequest) OpenAIChatRequest {
	chatReq := OpenAIChatRequest{
		Model:       rReq.Model,
		Stream:      rReq.Stream,
		Temperature: rReq.Temperature,
		MaxTokens:   rReq.MaxTokens,
	}
	if chatReq.Model == "" {
		chatReq.Model = "gemini-4-argon"
	}

	if strings.TrimSpace(rReq.Instructions) != "" {
		chatReq.Messages = append(chatReq.Messages, OpenAIMessage{
			Role:    "system",
			Content: rReq.Instructions,
		})
	}

	switch inp := rReq.Input.(type) {
	case string:
		if inp != "" {
			chatReq.Messages = append(chatReq.Messages, OpenAIMessage{
				Role:    "user",
				Content: inp,
			})
		}
	case []interface{}:
		for _, rawItem := range inp {
			itemMap, ok := rawItem.(map[string]interface{})
			if !ok {
				continue
			}
			itemType, _ := itemMap["type"].(string)
			role, _ := itemMap["role"].(string)

			switch itemType {
			case "function_call":
				callID, _ := itemMap["call_id"].(string)
				if callID == "" {
					callID, _ = itemMap["id"].(string)
				}
				name, _ := itemMap["name"].(string)
				args, _ := itemMap["arguments"].(string)
				chatReq.Messages = append(chatReq.Messages, OpenAIMessage{
					Role: "assistant",
					ToolCalls: []OpenAIToolCall{
						{
							ID:   callID,
							Type: "function",
							Function: OpenAIFunctionCallDesc{
								Name:      name,
								Arguments: args,
							},
						},
					},
				})
			case "function_call_output":
				callID, _ := itemMap["call_id"].(string)
				outputStr := ""
				switch outVal := itemMap["output"].(type) {
				case string:
					outputStr = outVal
				default:
					b, _ := json.Marshal(outVal)
					outputStr = string(b)
				}
				chatReq.Messages = append(chatReq.Messages, OpenAIMessage{
					Role:       "tool",
					ToolCallID: callID,
					Content:    outputStr,
				})
			default:
				if role == "" {
					role = "user"
				}
				if role == "developer" {
					role = "system"
				}
				var textBuilder strings.Builder
				switch contentVal := itemMap["content"].(type) {
				case string:
					textBuilder.WriteString(contentVal)
				case []interface{}:
					for _, partRaw := range contentVal {
						if partMap, ok := partRaw.(map[string]interface{}); ok {
							if txt, ok := partMap["text"].(string); ok {
								textBuilder.WriteString(txt)
							}
						}
					}
				}
				if textBuilder.Len() > 0 {
					if role == "system" && len(chatReq.Messages) > 0 && chatReq.Messages[0].Role == "system" {
						prev, _ := chatReq.Messages[0].Content.(string)
						chatReq.Messages[0].Content = prev + "\n\n" + textBuilder.String()
					} else {
						chatReq.Messages = append(chatReq.Messages, OpenAIMessage{
							Role:    role,
							Content: textBuilder.String(),
						})
					}
				}
			}
		}
	}

	// Convert Responses API tools to OpenAITool
	for _, rawTool := range rReq.Tools {
		tMap, ok := rawTool.(map[string]interface{})
		if !ok {
			continue
		}
		tType, _ := tMap["type"].(string)
		if tType != "function" {
			continue
		}
		// Responses API puts name, description, parameters at top level of tool object
		name, _ := tMap["name"].(string)
		desc, _ := tMap["description"].(string)
		params := tMap["parameters"]
		if fnSub, ok := tMap["function"].(map[string]interface{}); ok {
			if name == "" {
				name, _ = fnSub["name"].(string)
			}
			if desc == "" {
				desc, _ = fnSub["description"].(string)
			}
			if params == nil {
				params = fnSub["parameters"]
			}
		}
		if name != "" {
			chatReq.Tools = append(chatReq.Tools, OpenAITool{
				Type: "function",
				Function: OpenAIFunctionDef{
					Name:        name,
					Description: desc,
					Parameters:  params,
				},
			})
		}
	}

	return chatReq
}

func emitResponsesSSE(c *gin.Context, flusher http.Flusher, eventType string, payload gin.H) {
	payload["type"] = eventType
	b, _ := json.Marshal(payload)
	_, _ = c.Writer.Write([]byte(fmt.Sprintf("event: %s\ndata: %s\n\n", eventType, string(b))))
	if flusher != nil {
		flusher.Flush()
	}
}

// handleOpenAIResponses handles POST /v1/responses for OpenAI Codex CLI
func handleOpenAIResponses(c *gin.Context, defaultAPIKey, a6ApiKey string) {
	startTime := time.Now()
	rawAuthHeader := c.GetHeader("Authorization")
	if rawAuthHeader == "" {
		rawAuthHeader = c.GetHeader("x-api-key")
	}
	if rawAuthHeader == "" {
		rawAuthHeader = c.GetHeader("api-key")
	}
	if strings.Contains(rawAuthHeader, "20261019-888888") {
		c.JSON(http.StatusUnauthorized, gin.H{"error": gin.H{"message": "This API Key has been revoked due to exposure. Please use the new key.", "type": "invalid_request_error", "code": "invalid_api_key"}})
		return
	}

	var rReq OpenAIResponsesRequest
	if err := c.ShouldBindJSON(&rReq); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"message": fmt.Sprintf("invalid json: %v", err)}})
		return
	}

	chatReq := convertResponsesToChatRequest(rReq)

	// Build internal request to our own localhost /v1/chat/completions to reuse 100% of Gemini/A6 routing, fallback, and audit logging
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	chatReqBytes, _ := json.Marshal(chatReq)
	internalURL := fmt.Sprintf("http://127.0.0.1:%s/v1/chat/completions", port)
	httpReq, err := http.NewRequestWithContext(c.Request.Context(), "POST", internalURL, bytes.NewReader(chatReqBytes))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"message": err.Error()}})
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if rawAuthHeader != "" {
		httpReq.Header.Set("Authorization", rawAuthHeader)
	}

	client := &http.Client{Timeout: 600 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{"message": err.Error()}})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		errBody, _ := io.ReadAll(resp.Body)
		c.Data(resp.StatusCode, "application/json", errBody)
		return
	}

	respID := "resp_" + strings.ReplaceAll(uuid.New().String(), "-", "")
	msgItemID := "msg_" + strings.ReplaceAll(uuid.New().String(), "-", "")
	createdAt := startTime.Unix()

	if !chatReq.Stream {
		bodyBytes, _ := io.ReadAll(resp.Body)
		var chatResp map[string]interface{}
		_ = json.Unmarshal(bodyBytes, &chatResp)

		replyText := ""
		var outputItems []gin.H
		if choices, ok := chatResp["choices"].([]interface{}); ok && len(choices) > 0 {
			if ch, ok := choices[0].(map[string]interface{}); ok {
				if msg, ok := ch["message"].(map[string]interface{}); ok {
					if cnt, ok := msg["content"].(string); ok && cnt != "" {
						replyText = cnt
						outputItems = append(outputItems, gin.H{
							"id":     msgItemID,
							"type":   "message",
							"status": "completed",
							"role":   "assistant",
							"content": []gin.H{
								{
									"type":        "output_text",
									"text":        replyText,
									"annotations": []gin.H{},
								},
							},
						})
					}
					if tcs, ok := msg["tool_calls"].([]interface{}); ok {
						for _, tcRaw := range tcs {
							if tcMap, ok := tcRaw.(map[string]interface{}); ok {
								callID, _ := tcMap["id"].(string)
								fnMap, _ := tcMap["function"].(map[string]interface{})
								name, _ := fnMap["name"].(string)
								args, _ := fnMap["arguments"].(string)
								outputItems = append(outputItems, gin.H{
									"id":        "fc_" + callID,
									"type":      "function_call",
									"status":    "completed",
									"call_id":   callID,
									"name":      name,
									"arguments": args,
								})
							}
						}
					}
				}
			}
		}
		if len(outputItems) == 0 {
			outputItems = append(outputItems, gin.H{
				"id":     msgItemID,
				"type":   "message",
				"status": "completed",
				"role":   "assistant",
				"content": []gin.H{
					{"type": "output_text", "text": "", "annotations": []gin.H{}},
				},
			})
		}

		c.JSON(http.StatusOK, gin.H{
			"id":          respID,
			"object":      "response",
			"created_at":  createdAt,
			"status":      "completed",
			"model":       chatReq.Model,
			"output":      outputItems,
			"output_text": replyText,
			"usage": gin.H{
				"input_tokens":  0,
				"output_tokens": 0,
				"total_tokens":  0,
			},
		})
		return
	}

	// Streaming SSE conversion: Chat Completions SSE -> OpenAI Responses SSE
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	flusher, _ := c.Writer.(http.Flusher)

	baseRespObj := gin.H{
		"id":         respID,
		"object":     "response",
		"created_at": createdAt,
		"status":     "in_progress",
		"model":      chatReq.Model,
		"output":     []gin.H{},
	}
	emitResponsesSSE(c, flusher, "response.created", gin.H{"response": baseRespObj})
	emitResponsesSSE(c, flusher, "response.in_progress", gin.H{"response": baseRespObj})

	msgItemStarted := false
	var fullText strings.Builder
	var finalOutputItems []gin.H
	outputIdx := 0
	msgOutputIdx := 0

	scanner := bufio.NewScanner(resp.Body)
	buf := make([]byte, 64*1024)
	scanner.Buffer(buf, 4*1024*1024)

	inputTokens := 0
	outputTokens := 0

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		dataStr := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		if dataStr == "" || dataStr == "[DONE]" {
			continue
		}

		var chunk map[string]interface{}
		if err := json.Unmarshal([]byte(dataStr), &chunk); err != nil {
			continue
		}

		if u, ok := chunk["usage"].(map[string]interface{}); ok {
			if pt, ok := u["prompt_tokens"].(float64); ok && pt > 0 {
				inputTokens = int(pt)
			}
			if ct, ok := u["completion_tokens"].(float64); ok && ct > 0 {
				outputTokens = int(ct)
			}
		}

		choices, ok := chunk["choices"].([]interface{})
		if !ok || len(choices) == 0 {
			continue
		}
		ch, ok := choices[0].(map[string]interface{})
		if !ok {
			continue
		}
		delta, ok := ch["delta"].(map[string]interface{})
		if !ok {
			continue
		}

		if txt, ok := delta["content"].(string); ok && txt != "" {
			if !msgItemStarted {
				msgItemStarted = true
				msgOutputIdx = outputIdx
				outputIdx++
				emitResponsesSSE(c, flusher, "response.output_item.added", gin.H{
					"output_index": msgOutputIdx,
					"item": gin.H{
						"id":      msgItemID,
						"type":    "message",
						"status":  "in_progress",
						"role":    "assistant",
						"content": []gin.H{},
					},
				})
				emitResponsesSSE(c, flusher, "response.content_part.added", gin.H{
					"item_id":       msgItemID,
					"output_index":  msgOutputIdx,
					"content_index": 0,
					"part": gin.H{
						"type":        "output_text",
						"text":        "",
						"annotations": []gin.H{},
					},
				})
			}
			fullText.WriteString(txt)
			emitResponsesSSE(c, flusher, "response.output_text.delta", gin.H{
				"item_id":       msgItemID,
				"output_index":  msgOutputIdx,
				"content_index": 0,
				"delta":         txt,
			})
		}

		if tcs, ok := delta["tool_calls"].([]interface{}); ok {
			for _, tcRaw := range tcs {
				tcMap, ok := tcRaw.(map[string]interface{})
				if !ok {
					continue
				}
				callID, _ := tcMap["id"].(string)
				if callID == "" {
					callID = "call_" + uuid.New().String()[:8]
				}
				fnMap, _ := tcMap["function"].(map[string]interface{})
				name, _ := fnMap["name"].(string)
				args, _ := fnMap["arguments"].(string)
				fcItemID := "fc_" + callID
				curIdx := outputIdx
				outputIdx++

				emitResponsesSSE(c, flusher, "response.output_item.added", gin.H{
					"output_index": curIdx,
					"item": gin.H{
						"id":        fcItemID,
						"type":      "function_call",
						"status":    "in_progress",
						"call_id":   callID,
						"name":      name,
						"arguments": "",
					},
				})
				if args != "" {
					emitResponsesSSE(c, flusher, "response.function_call_arguments.delta", gin.H{
						"item_id":      fcItemID,
						"output_index": curIdx,
						"delta":        args,
					})
				}
				emitResponsesSSE(c, flusher, "response.function_call_arguments.done", gin.H{
					"item_id":      fcItemID,
					"output_index": curIdx,
					"arguments":    args,
				})
				doneItem := gin.H{
					"id":        fcItemID,
					"type":      "function_call",
					"status":    "completed",
					"call_id":   callID,
					"name":      name,
					"arguments": args,
				}
				emitResponsesSSE(c, flusher, "response.output_item.done", gin.H{
					"output_index": curIdx,
					"item":         doneItem,
				})
				finalOutputItems = append(finalOutputItems, doneItem)
			}
		}
	}

	if msgItemStarted {
		finalText := fullText.String()
		emitResponsesSSE(c, flusher, "response.output_text.done", gin.H{
			"item_id":       msgItemID,
			"output_index":  msgOutputIdx,
			"content_index": 0,
			"text":          finalText,
		})
		donePart := gin.H{
			"type":        "output_text",
			"text":        finalText,
			"annotations": []gin.H{},
		}
		emitResponsesSSE(c, flusher, "response.content_part.done", gin.H{
			"item_id":       msgItemID,
			"output_index":  msgOutputIdx,
			"content_index": 0,
			"part":          donePart,
		})
		doneMsgItem := gin.H{
			"id":      msgItemID,
			"type":    "message",
			"status":  "completed",
			"role":    "assistant",
			"content": []gin.H{donePart},
		}
		emitResponsesSSE(c, flusher, "response.output_item.done", gin.H{
			"output_index": msgOutputIdx,
			"item":         doneMsgItem,
		})
		finalOutputItems = append([]gin.H{doneMsgItem}, finalOutputItems...)
	}

	completedResp := gin.H{
		"id":         respID,
		"object":     "response",
		"created_at": createdAt,
		"status":     "completed",
		"model":      chatReq.Model,
		"output":     finalOutputItems,
		"usage": gin.H{
			"input_tokens":         inputTokens,
			"input_tokens_details": gin.H{"cached_tokens": 0},
			"output_tokens":        outputTokens,
			"output_tokens_details": gin.H{"reasoning_tokens": 0},
			"total_tokens":         inputTokens + outputTokens,
		},
	}
	emitResponsesSSE(c, flusher, "response.completed", gin.H{"response": completedResp})
	log.Printf("✅ Completed /v1/responses stream for model=%s (items=%d)", chatReq.Model, len(finalOutputItems))
}
