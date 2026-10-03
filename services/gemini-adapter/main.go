package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/generative-ai-go/genai"
	"github.com/google/uuid"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
)

type OpenAIMessage struct {
	Role    string      `json:"role"`
	Content interface{} `json:"content"`
}

type OpenAIFunction struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Parameters  map[string]interface{} `json:"parameters,omitempty"`
}

type OpenAITool struct {
	Type     string         `json:"type"`
	Function OpenAIFunction `json:"function"`
}

type ChatCompletionRequest struct {
	Model       string          `json:"model"`
	Messages    []OpenAIMessage `json:"messages"`
	Tools       []OpenAITool    `json:"tools,omitempty"`
	Stream      bool            `json:"stream,omitempty"`
	Temperature *float32        `json:"temperature,omitempty"`
	MaxTokens   *int            `json:"max_tokens,omitempty"`
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	apiKey := os.Getenv("GEMINI_API_KEY")

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())

	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok", "service": "gemini-adapter-sidecar"})
	})

	handleModels := func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"object": "list",
			"data": []gin.H{
				{"id": "gemini-3.8-flash", "object": "model", "owned_by": "google"},
				{"id": "gemini-3.8-backup", "object": "model", "owned_by": "google"},
			},
		})
	}
	r.GET("/models", handleModels)
	r.GET("/v1/models", handleModels)

	handleChat := func(c *gin.Context) {
		handleChatCompletion(c, apiKey)
	}
	r.POST("/chat/completions", handleChat)
	r.POST("/v1/chat/completions", handleChat)

	log.Printf("🚀 Gemini Official SDK Adapter listening on port :%s", port)
	if err := r.Run(":" + port); err != nil {
		log.Fatalf("failed starting adapter: %v", err)
	}
}

func handleChatCompletion(c *gin.Context, defaultAPIKey string) {
	var req ChatCompletionRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	key := defaultAPIKey
	authHeader := c.GetHeader("Authorization")
	if strings.HasPrefix(authHeader, "Bearer ") {
		token := strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer "))
		// 如果客户端传的是真实的 Google AI Studio 密钥 (以 AIza 开头)，优先使用客户端传递的
		if strings.HasPrefix(token, "AIza") {
			key = token
		}
	}

	if key == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "No valid Gemini API key configured"})
		return
	}

	ctx := c.Request.Context()
	client, err := genai.NewClient(ctx, option.WithAPIKey(key))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Failed to initialize Google SDK client: %v", err)})
		return
	}
	defer client.Close()

	// 统一绑定 gemini-3.8-flash 模型
	modelName := "gemini-3.8-flash"
	model := client.GenerativeModel(modelName)

	// 1. 彻底解除 Google 安全审查 (对齐 BLOCK_NONE)
	model.SafetySettings = []*genai.SafetySetting{
		{Category: genai.HarmCategoryHarassment, Threshold: genai.HarmBlockNone},
		{Category: genai.HarmCategoryHateSpeech, Threshold: genai.HarmBlockNone},
		{Category: genai.HarmCategorySexuallyExplicit, Threshold: genai.HarmBlockNone},
		{Category: genai.HarmCategoryDangerousContent, Threshold: genai.HarmBlockNone},
	}

	// 2. 映射 OpenAI 消息至 Google SDK 格式
	var cs []*genai.Content
	for _, m := range req.Messages {
		role := "user"
		if m.Role == "assistant" {
			role = "model"
		} else if m.Role == "system" {
			// Google SDK 原生支持独立系统指令
			model.SystemInstruction = &genai.Content{
				Parts: []genai.Part{genai.Text(extractTextContent(m.Content))},
			}
			continue
		}

		cs = append(cs, &genai.Content{
			Role:  role,
			Parts: []genai.Part{genai.Text(extractTextContent(m.Content))},
		})
	}

	// 3. 核心突破: 使用 Google 官方 SDK 完美注册工具集 (Tool Declarations)
	if len(req.Tools) > 0 {
		var funcDecls []*genai.FunctionDeclaration
		for _, t := range req.Tools {
			decl := &genai.FunctionDeclaration{
				Name:        t.Function.Name,
				Description: t.Function.Description,
			}
			// 转换 JSON Schema 参数
			if len(t.Function.Parameters) > 0 {
				paramBytes, _ := json.Marshal(t.Function.Parameters)
				var schema genai.Schema
				if err := json.Unmarshal(paramBytes, &schema); err == nil {
					decl.Parameters = &schema
				}
			}
			funcDecls = append(funcDecls, decl)
		}
		model.Tools = []*genai.Tool{{FunctionDeclarations: funcDecls}}
	}

	// 4. 流式 SSE 响应输出
	if req.Stream {
		c.Writer.Header().Set("Content-Type", "text/event-stream")
		c.Writer.Header().Set("Cache-Control", "no-cache")
		c.Writer.Header().Set("Connection", "keep-alive")

		iter := model.GenerateContentStream(ctx, contentsToParts(cs)...)
		reqID := "chatcmpl-" + uuid.NewString()

		c.Stream(func(w io.Writer) bool {
			resp, err := iter.Next()
			if err == iterator.Done {
				// 输出最终完成块与 [DONE]
				doneChunk := map[string]interface{}{
					"id":      reqID,
					"object":  "chat.completion.chunk",
					"created": time.Now().Unix(),
					"model":   modelName,
					"choices": []map[string]interface{}{
						{
							"index":         0,
							"delta":         map[string]interface{}{},
							"finish_reason": "stop",
						},
					},
				}
				b, _ := json.Marshal(doneChunk)
				_, _ = fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", string(b))
				return false
			}
			if err != nil {
				log.Printf("Google Stream Error: %v", err)
				return false
			}

			for _, candidate := range resp.Candidates {
				if candidate.Content == nil {
					continue
				}

				for _, part := range candidate.Content.Parts {
					delta := map[string]interface{}{}
					finishReason := interface{}(nil)

					// 🎯 核心解决: 完整转译流式 FunctionCall ➔ OpenAI delta.tool_calls
					if fnCall, ok := part.(genai.FunctionCall); ok {
						argsBytes, _ := json.Marshal(fnCall.Args)
						delta["tool_calls"] = []map[string]interface{}{
							{
								"index": 0,
								"id":    fmt.Sprintf("call_%s", uuid.NewString()[:8]),
								"type":  "function",
								"function": map[string]string{
									"name":      fnCall.Name,
									"arguments": string(argsBytes),
								},
							},
						}
						finishReason = "tool_calls"
					} else if textPart, ok := part.(genai.Text); ok {
						delta["content"] = string(textPart)
					}

					chunk := map[string]interface{}{
						"id":      reqID,
						"object":  "chat.completion.chunk",
						"created": time.Now().Unix(),
						"model":   modelName,
						"choices": []map[string]interface{}{
							{
								"index":         candidate.Index,
								"delta":         delta,
								"finish_reason": finishReason,
							},
						},
					}
					b, _ := json.Marshal(chunk)
					_, _ = fmt.Fprintf(w, "data: %s\n\n", string(b))
				}
			}
			return true
		})
		return
	}

	// 5. 非流式响应输出
	resp, err := model.GenerateContent(ctx, contentsToParts(cs)...)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("Google GenerateContent Error: %v", err)})
		return
	}

	var choices []map[string]interface{}
	reqID := "chatcmpl-" + uuid.NewString()

	for idx, candidate := range resp.Candidates {
		msg := map[string]interface{}{"role": "assistant"}
		finishReason := "stop"

		if candidate.Content != nil {
			var textParts []string
			var toolCalls []map[string]interface{}

			for _, part := range candidate.Content.Parts {
				if fnCall, ok := part.(genai.FunctionCall); ok {
					argsBytes, _ := json.Marshal(fnCall.Args)
					toolCalls = append(toolCalls, map[string]interface{}{
						"id":   fmt.Sprintf("call_%s", uuid.NewString()[:8]),
						"type": "function",
						"function": map[string]string{
							"name":      fnCall.Name,
							"arguments": string(argsBytes),
						},
					})
					finishReason = "tool_calls"
				} else if textPart, ok := part.(genai.Text); ok {
					textParts = append(textParts, string(textPart))
				}
			}

			if len(textParts) > 0 {
				msg["content"] = strings.Join(textParts, "")
			}
			if len(toolCalls) > 0 {
				msg["tool_calls"] = toolCalls
			}
		}

		choices = append(choices, map[string]interface{}{
			"index":         idx,
			"message":       msg,
			"finish_reason": finishReason,
		})
	}

	c.JSON(http.StatusOK, gin.H{
		"id":      reqID,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   modelName,
		"choices": choices,
		"usage": gin.H{
			"prompt_tokens":     resp.UsageMetadata.PromptTokenCount,
			"completion_tokens": resp.UsageMetadata.CandidatesTokenCount,
			"total_tokens":      resp.UsageMetadata.TotalTokenCount,
		},
	})
}

func extractTextContent(content interface{}) string {
	if str, ok := content.(string); ok {
		return str
	}
	b, _ := json.Marshal(content)
	return string(b)
}

func contentsToParts(contents []*genai.Content) []genai.Part {
	var parts []genai.Part
	for _, c := range contents {
		parts = append(parts, c.Parts...)
	}
	return parts
}
