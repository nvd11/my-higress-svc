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
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

var thoughtSigCache sync.Map // map[string]string: callID / toolName -> thoughtSignature
var callIDToName sync.Map    // map[string]string: callID -> functionName

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	apiKey := os.Getenv("GEMINI_API_KEY")

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(gin.Logger())

	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "service": "gemini-adapter-sidecar"})
	})

	r.GET("/models", handleModels)
	r.GET("/v1/models", handleModels)
	r.GET("/v1beta/models", handleModels)

	// 统一处理推理请求
	r.POST("/*action", func(c *gin.Context) {
		path := c.Request.URL.Path
		log.Printf("📥 Ingesting POST request: %s", path)
		if strings.Contains(path, "chat/completions") {
			handleOpenAIStyle(c, apiKey)
			return
		}
		handleGeminiNativeProxy(c, apiKey)
	})

	r.NoRoute(func(c *gin.Context) {
		log.Printf("⚠️ Incoming Unmatched Path: %s, Method: %s", c.Request.URL.Path, c.Request.Method)
		if strings.Contains(c.Request.URL.Path, "generateContent") || c.Request.Method == "POST" {
			handleGeminiNativeProxy(c, apiKey)
			return
		}
		c.JSON(http.StatusNotFound, gin.H{"error": "route not found", "path": c.Request.URL.Path})
	})

	log.Printf("🚀 Gemini Adapter Sidecar listening on port :%s", port)
	if err := r.Run(":" + port); err != nil {
		log.Fatalf("failed starting adapter: %v", err)
	}
}

func handleModels(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"object": "list",
		"data": []gin.H{
			{"id": "gemini-3.8-flash", "object": "model", "owned_by": "google"},
			{"id": "gemini-3.8-flash-search", "object": "model", "owned_by": "google"},
			{"id": "gemini-3.8-backup", "object": "model", "owned_by": "google"},
		},
	})
}

// -----------------------------------------------------------------------------
// OpenAI 格式与 Gemini 原生格式转换结构体定义
// -----------------------------------------------------------------------------

type OpenAIChatRequest struct {
	Model       string          `json:"model"`
	Messages    []OpenAIMessage `json:"messages"`
	Tools       []OpenAITool    `json:"tools,omitempty"`
	Stream      bool            `json:"stream,omitempty"`
	Temperature *float64        `json:"temperature,omitempty"`
	MaxTokens   *int            `json:"max_tokens,omitempty"`
}

type OpenAIMessage struct {
	Role       string            `json:"role"`
	Content    interface{}       `json:"content"`
	Name       string            `json:"name,omitempty"`
	ToolCalls  []OpenAIToolCall  `json:"tool_calls,omitempty"`
	ToolCallID string            `json:"tool_call_id,omitempty"`
}

type OpenAITool struct {
	Type     string             `json:"type"`
	Function OpenAIFunctionDef  `json:"function"`
}

type OpenAIFunctionDef struct {
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	Parameters  interface{} `json:"parameters,omitempty"`
}

type OpenAIToolCall struct {
	Index    int                    `json:"index,omitempty"`
	ID       string                 `json:"id"`
	Type     string                 `json:"type"`
	Function OpenAIFunctionCallDesc `json:"function"`
}

type OpenAIFunctionCallDesc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// Google 结构体
type GoogleGenerateContentRequest struct {
	Contents          []GoogleContent    `json:"contents"`
	SystemInstruction *GoogleContent     `json:"system_instruction,omitempty"`
	Tools             []GoogleTool       `json:"tools,omitempty"`
	ToolConfig        *GoogleToolConfig  `json:"tool_config,omitempty"`
	SafetySettings    []GoogleSafety     `json:"safetySettings,omitempty"`
}

type GoogleToolConfig struct {
	IncludeServerSideToolInvocations bool `json:"include_server_side_tool_invocations"`
}

type GoogleContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []GooglePart `json:"parts"`
}

type GooglePart struct {
	Text             string                  `json:"text,omitempty"`
	ThoughtSignature string                  `json:"thoughtSignature,omitempty"`
	FunctionCall     *GoogleFunctionCall     `json:"functionCall,omitempty"`
	FunctionResponse *GoogleFunctionResponse `json:"functionResponse,omitempty"`
}

type GoogleFunctionCall struct {
	Name string                 `json:"name"`
	Args map[string]interface{} `json:"args,omitempty"`
	ID   string                 `json:"id,omitempty"`
}

type GoogleFunctionResponse struct {
	Name     string                 `json:"name"`
	Response map[string]interface{} `json:"response"`
}

type GoogleTool struct {
	FunctionDeclarations []GoogleFuncDecl `json:"function_declarations,omitempty"`
	GoogleSearch         *struct{}        `json:"googleSearch,omitempty"`
}

type GoogleFuncDecl struct {
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	Parameters  interface{} `json:"parameters,omitempty"`
}

type GoogleSafety struct {
	Category  string `json:"category"`
	Threshold string `json:"threshold"`
}

type GoogleGenerateContentResponse struct {
	Candidates     []GoogleCandidate `json:"candidates,omitempty"`
	PromptFeedback *GoogleFeedback   `json:"promptFeedback,omitempty"`
	UsageMetadata  *GoogleUsage      `json:"usageMetadata,omitempty"`
}

type GoogleCandidate struct {
	Content           GoogleContent      `json:"content"`
	FinishReason      string             `json:"finishReason,omitempty"`
	Index             int                `json:"index"`
	GroundingMetadata *GroundingMetadata `json:"groundingMetadata,omitempty"`
}

type GroundingMetadata struct {
	WebSearchQueries []string         `json:"webSearchQueries,omitempty"`
	GroundingChunks  []GroundingChunk `json:"groundingChunks,omitempty"`
}

type GroundingChunk struct {
	Web *GroundingWeb `json:"web,omitempty"`
}

type GroundingWeb struct {
	URI   string `json:"uri,omitempty"`
	Title string `json:"title,omitempty"`
}

type GoogleFeedback struct {
	BlockReason string `json:"blockReason,omitempty"`
}

type GoogleUsage struct {
	PromptTokenCount     int `json:"promptTokenCount"`
	CandidatesTokenCount int `json:"candidatesTokenCount"`
	TotalTokenCount      int `json:"totalTokenCount"`
}

// -----------------------------------------------------------------------------
// handleOpenAIStyle: 接收标准 OpenAI 格式，转译调用 Google AI Studio
// -----------------------------------------------------------------------------

func handleOpenAIStyle(c *gin.Context, defaultAPIKey string) {
	key := defaultAPIKey
	if auth := c.GetHeader("Authorization"); auth != "" {
		token := strings.TrimPrefix(auth, "Bearer ")
		token = strings.TrimSpace(token)
		if strings.HasPrefix(token, "AIza") {
			key = token
		}
	}

	var req OpenAIChatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid json: %v", err)})
		return
	}

	// 构造 Google 请求
	gReq := GoogleGenerateContentRequest{
		SafetySettings: []GoogleSafety{
			{Category: "HARM_CATEGORY_HARASSMENT", Threshold: "BLOCK_NONE"},
			{Category: "HARM_CATEGORY_HATE_SPEECH", Threshold: "BLOCK_NONE"},
			{Category: "HARM_CATEGORY_SEXUALLY_EXPLICIT", Threshold: "BLOCK_NONE"},
			{Category: "HARM_CATEGORY_DANGEROUS_CONTENT", Threshold: "BLOCK_NONE"},
			{Category: "HARM_CATEGORY_CIVIC_INTEGRITY", Threshold: "BLOCK_NONE"},
		},
	}

	// 预先建立本轮请求内所有 tool_calls 的 ID -> Name 快速反查表
	localToolMap := make(map[string]string)
	for _, m := range req.Messages {
		if m.Role == "assistant" {
			for _, tc := range m.ToolCalls {
				if tc.ID != "" && tc.Function.Name != "" {
					localToolMap[tc.ID] = tc.Function.Name
					callIDToName.Store(tc.ID, tc.Function.Name)
				}
			}
		}
	}

	for _, msg := range req.Messages {
		contentStr := ""
		switch v := msg.Content.(type) {
		case string:
			contentStr = v
		default:
			b, _ := json.Marshal(v)
			contentStr = string(b)
		}

		if msg.Role == "system" {
			gReq.SystemInstruction = &GoogleContent{
				Parts: []GooglePart{{Text: contentStr}},
			}
			continue
		}

		if msg.Role == "user" {
			gReq.Contents = append(gReq.Contents, GoogleContent{
				Role:  "user",
				Parts: []GooglePart{{Text: contentStr}},
			})
			continue
		}

		if msg.Role == "assistant" {
			parts := []GooglePart{}
			if contentStr != "" {
				parts = append(parts, GooglePart{Text: contentStr})
			}
			for _, tc := range msg.ToolCalls {
				var args map[string]interface{}
				_ = json.Unmarshal([]byte(tc.Function.Arguments), &args)

				sig := ""
				if tc.ID != "" {
					if v, ok := thoughtSigCache.Load(tc.ID); ok {
						sig = v.(string)
					}
				}
				if sig == "" && tc.Function.Name != "" {
					if v, ok := thoughtSigCache.Load(tc.Function.Name); ok {
						sig = v.(string)
					}
				}

				parts = append(parts, GooglePart{
					ThoughtSignature: sig,
					FunctionCall: &GoogleFunctionCall{
						Name: tc.Function.Name,
						Args: args,
						ID:   tc.ID,
					},
				})
			}
			if len(parts) > 0 {
				gReq.Contents = append(gReq.Contents, GoogleContent{
					Role:  "model",
					Parts: parts,
				})
			}
			continue
		}

		if msg.Role == "tool" {
			var respMap map[string]interface{}
			if err := json.Unmarshal([]byte(contentStr), &respMap); err != nil {
				respMap = map[string]interface{}{"content": contentStr}
			}

			// 严格确保 FunctionResponse.Name 非空，防止 Google 400
			toolName := msg.Name
			if toolName == "" && msg.ToolCallID != "" {
				if n, ok := localToolMap[msg.ToolCallID]; ok {
					toolName = n
				} else if v, ok := callIDToName.Load(msg.ToolCallID); ok {
					toolName = v.(string)
				}
			}
			if toolName == "" && len(req.Tools) > 0 {
				toolName = req.Tools[0].Function.Name
			}
			if toolName == "" {
				toolName = "default_api"
			}

			gReq.Contents = append(gReq.Contents, GoogleContent{
				Role: "user",
				Parts: []GooglePart{
					{
						FunctionResponse: &GoogleFunctionResponse{
							Name:     toolName,
							Response: respMap,
						},
					},
				},
			})
			continue
		}
	}

	// 判断是否启用 Google 原生 Search Grounding (联网搜索增强)
	model := req.Model
	if model == "" {
		model = "gemini-3.8-flash"
	}
	enableSearch := strings.HasSuffix(model, "-search") || strings.HasSuffix(model, ":search")
	realModel := model
	if enableSearch {
		realModel = strings.TrimSuffix(strings.TrimSuffix(model, "-search"), ":search")
	}
	if realModel == "" {
		realModel = "gemini-3.8-flash"
	}

	// 转换 Tools
	if len(req.Tools) > 0 {
		var decls []GoogleFuncDecl
		for _, t := range req.Tools {
			if t.Type == "function" {
				decls = append(decls, GoogleFuncDecl{
					Name:        t.Function.Name,
					Description: t.Function.Description,
					Parameters:  t.Function.Parameters,
				})
			}
		}
		if len(decls) > 0 {
			gReq.Tools = append(gReq.Tools, GoogleTool{FunctionDeclarations: decls})
		}
	}

	if enableSearch {
		gReq.Tools = append(gReq.Tools, GoogleTool{GoogleSearch: &struct{}{}})
		if len(req.Tools) > 0 {
			gReq.ToolConfig = &GoogleToolConfig{IncludeServerSideToolInvocations: true}
		}
	}

	gReqBytes, _ := json.Marshal(gReq)

	action := "generateContent"
	extraQuery := ""
	if req.Stream {
		action = "streamGenerateContent"
		extraQuery = "&alt=sse"
	}

	upstreamURL := fmt.Sprintf("https://generativelanguage.googleapis.com/v1alpha/models/%s:%s?key=%s%s", realModel, action, key, extraQuery)
	log.Printf("🚀 Dispatching OpenAI to Google: URL=%s (stream=%v, search=%v, client_tools=%d)", upstreamURL, req.Stream, enableSearch, len(req.Tools))

	httpReq, err := http.NewRequestWithContext(c.Request.Context(), "POST", upstreamURL, bytes.NewReader(gReqBytes))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", "litellm/1.97.0")

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("Google API request failed: %v", err)})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		errBody, _ := io.ReadAll(resp.Body)
		log.Printf("❌ Google API Error (%d): %s", resp.StatusCode, string(errBody))
		c.Data(resp.StatusCode, resp.Header.Get("Content-Type"), errBody)
		return
	}

	chatCmplID := "chatcmpl-" + uuid.New().String()
	created := time.Now().Unix()

	if req.Stream {
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		flusher, _ := c.Writer.(http.Flusher)

		scanner := bufio.NewScanner(resp.Body)
		buf := make([]byte, 64*1024)
		scanner.Buffer(buf, 1024*1024)

		hasToolCalls := false
		var sources []string
		seenSources := make(map[string]bool)

		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}

			jsonStr := strings.TrimPrefix(line, "data: ")
			var gResp GoogleGenerateContentResponse
			if err := json.Unmarshal([]byte(jsonStr), &gResp); err != nil {
				continue
			}

			// 检查安全拦截
			if gResp.PromptFeedback != nil && gResp.PromptFeedback.BlockReason != "" {
				errMsg := fmt.Sprintf("[Google AI Studio safety filter blocked request: %s]", gResp.PromptFeedback.BlockReason)
				chunk := gin.H{
					"id":      chatCmplID,
					"object":  "chat.completion.chunk",
					"created": created,
					"model":   model,
					"choices": []gin.H{
						{
							"index":         0,
							"delta":         gin.H{"content": errMsg},
							"finish_reason": "stop",
						},
					},
				}
				chunkBytes, _ := json.Marshal(chunk)
				_, _ = c.Writer.Write([]byte(fmt.Sprintf("data: %s\n\n", string(chunkBytes))))
				if flusher != nil {
					flusher.Flush()
				}
				break
			}

			if len(gResp.Candidates) == 0 {
				continue
			}

			cand := gResp.Candidates[0]
			if cand.GroundingMetadata != nil {
				for _, gc := range cand.GroundingMetadata.GroundingChunks {
					if gc.Web != nil && gc.Web.URI != "" {
						title := gc.Web.Title
						if title == "" {
							title = gc.Web.URI
						}
						key := gc.Web.URI
						if !seenSources[key] {
							seenSources[key] = true
							sources = append(sources, fmt.Sprintf("- [%s](%s)", title, gc.Web.URI))
						}
					}
				}
			}
			for _, part := range cand.Content.Parts {
				if part.Text != "" {
					chunk := gin.H{
						"id":      chatCmplID,
						"object":  "chat.completion.chunk",
						"created": created,
						"model":   model,
						"choices": []gin.H{
							{
								"index":         0,
								"delta":         gin.H{"content": part.Text},
								"finish_reason": nil,
							},
						},
					}
					chunkBytes, _ := json.Marshal(chunk)
					_, _ = c.Writer.Write([]byte(fmt.Sprintf("data: %s\n\n", string(chunkBytes))))
					if flusher != nil {
						flusher.Flush()
					}
				}

				if part.FunctionCall != nil {
					hasToolCalls = true
					argsBytes, _ := json.Marshal(part.FunctionCall.Args)
					callID := part.FunctionCall.ID
					if callID == "" {
						callID = "call_" + uuid.New().String()[:8]
					}

					callIDToName.Store(callID, part.FunctionCall.Name)

					if part.ThoughtSignature != "" {
						thoughtSigCache.Store(callID, part.ThoughtSignature)
						thoughtSigCache.Store(part.FunctionCall.Name, part.ThoughtSignature)
					}

					chunk := gin.H{
						"id":      chatCmplID,
						"object":  "chat.completion.chunk",
						"created": created,
						"model":   model,
						"choices": []gin.H{
							{
								"index": 0,
								"delta": gin.H{
									"tool_calls": []gin.H{
										{
											"index": 0,
											"id":    callID,
											"type":  "function",
											"function": gin.H{
												"name":      part.FunctionCall.Name,
												"arguments": string(argsBytes),
											},
										},
									},
								},
								"finish_reason": nil,
							},
						},
					}
					chunkBytes, _ := json.Marshal(chunk)
					_, _ = c.Writer.Write([]byte(fmt.Sprintf("data: %s\n\n", string(chunkBytes))))
					if flusher != nil {
						flusher.Flush()
					}
				}
			}

			if cand.FinishReason != "" {
				// 若存在联网搜索引用的信源，且不是纯工具调用，将信源优雅追加在末尾
				if len(sources) > 0 && !hasToolCalls {
					sourceBlock := "\n\n🌐 **网络参考信源**:\n" + strings.Join(sources, "\n")
					sourceChunk := gin.H{
						"id":      chatCmplID,
						"object":  "chat.completion.chunk",
						"created": created,
						"model":   model,
						"choices": []gin.H{
							{
								"index":         0,
								"delta":         gin.H{"content": sourceBlock},
								"finish_reason": nil,
							},
						},
					}
					chunkBytes, _ := json.Marshal(sourceChunk)
					_, _ = c.Writer.Write([]byte(fmt.Sprintf("data: %s\n\n", string(chunkBytes))))
					if flusher != nil {
						flusher.Flush()
					}
					sources = nil // 仅发一次
				}

				finishReason := "stop"
				if hasToolCalls {
					finishReason = "tool_calls"
				}

				usage := gin.H{}
				if gResp.UsageMetadata != nil {
					usage = gin.H{
						"prompt_tokens":     gResp.UsageMetadata.PromptTokenCount,
						"completion_tokens": gResp.UsageMetadata.CandidatesTokenCount,
						"total_tokens":      gResp.UsageMetadata.TotalTokenCount,
					}
				}

				finalChunk := gin.H{
					"id":      chatCmplID,
					"object":  "chat.completion.chunk",
					"created": created,
					"model":   model,
					"choices": []gin.H{
						{
							"index":         0,
							"delta":         gin.H{},
							"finish_reason": finishReason,
						},
					},
					"usage": usage,
				}
				chunkBytes, _ := json.Marshal(finalChunk)
				_, _ = c.Writer.Write([]byte(fmt.Sprintf("data: %s\n\n", string(chunkBytes))))
				if flusher != nil {
					flusher.Flush()
				}
			}
		}

		_, _ = c.Writer.Write([]byte("data: [DONE]\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
		return
	}

	// 非流式响应
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed reading Google response"})
		return
	}

	var gResp GoogleGenerateContentResponse
	if err := json.Unmarshal(respBody, &gResp); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed parsing Google response"})
		return
	}

	if len(gResp.Candidates) == 0 {
		c.JSON(http.StatusOK, gin.H{
			"id":      chatCmplID,
			"object":  "chat.completion",
			"created": created,
			"model":   model,
			"choices": []gin.H{
				{
					"index":         0,
					"message":       gin.H{"role": "assistant", "content": ""},
					"finish_reason": "stop",
				},
			},
		})
		return
	}

	cand := gResp.Candidates[0]
	var outContent string
	var outToolCalls []gin.H

	for _, part := range cand.Content.Parts {
		if part.Text != "" {
			outContent += part.Text
		}
		if part.FunctionCall != nil {
			argsBytes, _ := json.Marshal(part.FunctionCall.Args)
			callID := part.FunctionCall.ID
			if callID == "" {
				callID = "call_" + uuid.New().String()[:8]
			}
			callIDToName.Store(callID, part.FunctionCall.Name)
			if part.ThoughtSignature != "" {
				thoughtSigCache.Store(callID, part.ThoughtSignature)
				thoughtSigCache.Store(part.FunctionCall.Name, part.ThoughtSignature)
			}
			outToolCalls = append(outToolCalls, gin.H{
				"id":   callID,
				"type": "function",
				"function": gin.H{
					"name":      part.FunctionCall.Name,
					"arguments": string(argsBytes),
				},
			})
		}
	}

	finishReason := "stop"
	if len(outToolCalls) > 0 {
		finishReason = "tool_calls"
	}

	if cand.GroundingMetadata != nil && len(outToolCalls) == 0 {
		var sources []string
		seen := make(map[string]bool)
		for _, gc := range cand.GroundingMetadata.GroundingChunks {
			if gc.Web != nil && gc.Web.URI != "" {
				title := gc.Web.Title
				if title == "" {
					title = gc.Web.URI
				}
				if !seen[gc.Web.URI] {
					seen[gc.Web.URI] = true
					sources = append(sources, fmt.Sprintf("- [%s](%s)", title, gc.Web.URI))
				}
			}
		}
		if len(sources) > 0 {
			outContent += "\n\n🌐 **网络参考信源**:\n" + strings.Join(sources, "\n")
		}
	}

	msgObj := gin.H{"role": "assistant"}
	if outContent != "" {
		msgObj["content"] = outContent
	}
	if len(outToolCalls) > 0 {
		msgObj["tool_calls"] = outToolCalls
	}

	usage := gin.H{}
	if gResp.UsageMetadata != nil {
		usage = gin.H{
			"prompt_tokens":     gResp.UsageMetadata.PromptTokenCount,
			"completion_tokens": gResp.UsageMetadata.CandidatesTokenCount,
			"total_tokens":      gResp.UsageMetadata.TotalTokenCount,
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"id":      chatCmplID,
		"object":  "chat.completion",
		"created": created,
		"model":   model,
		"choices": []gin.H{
			{
				"index":         0,
				"message":       msgObj,
				"finish_reason": finishReason,
			},
		},
		"usage": usage,
	})
}

// -----------------------------------------------------------------------------
// handleGeminiNativeProxy: 纯透传 Google 原生请求 (兼容器)
// -----------------------------------------------------------------------------

func handleGeminiNativeProxy(c *gin.Context, defaultAPIKey string) {
	key := defaultAPIKey
	if token := c.GetHeader("x-goog-api-key"); token != "" && strings.HasPrefix(token, "AIza") {
		key = token
	}

	rawBody, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed reading request body"})
		return
	}

	var bodyMap map[string]interface{}
	if err := json.Unmarshal(rawBody, &bodyMap); err == nil {
		bodyMap["safetySettings"] = []map[string]string{
			{"category": "HARM_CATEGORY_HARASSMENT", "threshold": "BLOCK_NONE"},
			{"category": "HARM_CATEGORY_HATE_SPEECH", "threshold": "BLOCK_NONE"},
			{"category": "HARM_CATEGORY_SEXUALLY_EXPLICIT", "threshold": "BLOCK_NONE"},
			{"category": "HARM_CATEGORY_DANGEROUS_CONTENT", "threshold": "BLOCK_NONE"},
			{"category": "HARM_CATEGORY_CIVIC_INTEGRITY", "threshold": "BLOCK_NONE"},
		}
		rawBody, _ = json.Marshal(bodyMap)
	}

	upstreamPath := c.Request.URL.Path
	upstreamPath = strings.Replace(upstreamPath, "/v1beta/", "/v1alpha/", 1)
	upstreamPath = strings.Replace(upstreamPath, "/v1/", "/v1alpha/", 1)
	if !strings.HasPrefix(upstreamPath, "/v1alpha/") {
		upstreamPath = "/v1alpha" + upstreamPath
	}

	upstreamURL := fmt.Sprintf("https://generativelanguage.googleapis.com%s?key=%s", upstreamPath, key)
	if c.Request.URL.RawQuery != "" {
		upstreamURL += "&" + c.Request.URL.RawQuery
	}

	httpReq, err := http.NewRequestWithContext(c.Request.Context(), c.Request.Method, upstreamURL, bytes.NewReader(rawBody))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("User-Agent", "litellm/1.97.0")
	httpReq.Header.Set("x-goog-api-key", key)

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("failed calling Google AI Studio: %v", err)})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		errBody, _ := io.ReadAll(resp.Body)
		c.Data(resp.StatusCode, resp.Header.Get("Content-Type"), errBody)
		return
	}

	for k, vv := range resp.Header {
		for _, v := range vv {
			c.Writer.Header().Add(k, v)
		}
	}
	c.Writer.WriteHeader(resp.StatusCode)

	flusher, hasFlusher := c.Writer.(http.Flusher)
	buf := make([]byte, 4096)
	for {
		n, rErr := resp.Body.Read(buf)
		if n > 0 {
			chunkStr := string(buf[:n])
			if strings.Contains(chunkStr, "\"blockReason\"") && !strings.Contains(chunkStr, "\"candidates\"") {
				synthetic := "data: {\"candidates\": [{\"content\": {\"parts\": [{\"text\": \"[Google AI Studio upstream safety filter blocked this request due to blockReason (e.g. repeated prompt / PROHIBITED_CONTENT). Please start a fresh topic or clear thread history.]\"}],\"role\": \"model\"},\"finishReason\": \"STOP\",\"index\": 0}]}\n\n"
				_, _ = c.Writer.Write([]byte(synthetic))
			} else {
				_, _ = c.Writer.Write(buf[:n])
			}
			if hasFlusher {
				flusher.Flush()
			}
		}
		if rErr != nil {
			break
		}
	}
}
