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

	log.Printf("🚀 Gemini Adapter Sidecar listening on port :%s (Audit Backend: %s)", port, func() string {
		u := os.Getenv("AUDIT_BACKEND_URL")
		if u == "" {
			return "http://higress-dashboard-backend.higress-system.svc.cluster.local:4000/api/v1/internal/audit-log"
		}
		return u
	}())
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

// sanitizeGoogleTurns 解决 Google Gemini 原生对话时序强校验限制:
// "Please ensure that function call turn comes immediately after a user turn or after a function response turn."
// 规则剖析:
// 1. 任何带有 functionCall 的 model turn，紧随其后的必须是包含对应 functionResponse 的 user turn。
// 2. 任何包含 functionResponse 的 user turn，前面必须紧邻着发出 functionCall 的 model turn。
// 3. 严格交替: 连续相同的 role (user 连 user, 或 model 连 model) 进行 Parts 合并，绝不产生非法同角色切块。
// 4. 当因为 Compaction 或截断导致出现孤立的 functionCall (缺失后续 response) 时，自动注入一个合法的空/成功 functionResponse。
// 5. 当出现孤立的 functionResponse (前文被截断缺失 call) 时，自动平滑丢弃该孤立 response 或转为纯文本，防止 Google 400 拒收。
func sanitizeGoogleTurns(contents []GoogleContent) []GoogleContent {
	if len(contents) == 0 {
		return contents
	}

	// 步骤一：合并相邻相同 Role 的 Content，确保 Role 严格交替
	var merged []GoogleContent
	for _, c := range contents {
		if len(c.Parts) == 0 {
			continue
		}
		if len(merged) > 0 && merged[len(merged)-1].Role == c.Role {
			merged[len(merged)-1].Parts = append(merged[len(merged)-1].Parts, c.Parts...)
		} else {
			merged = append(merged, c)
		}
	}

	// 步骤二：修复 FunctionCall 与 FunctionResponse 的成对时序与合法性
	var sanitized []GoogleContent
	for i := 0; i < len(merged); i++ {
		current := merged[i]

		// 检查是否有 FunctionResponse
		hasFuncResp := false
		for _, p := range current.Parts {
			if p.FunctionResponse != nil {
				hasFuncResp = true
				break
			}
		}

		if hasFuncResp {
			// Google 要求: FunctionResponse 必须紧接在包含 FunctionCall 的 model turn 之后
			// 检查前一个 turn 是否包含匹配的 FunctionCall
			prevHasFuncCall := false
			if len(sanitized) > 0 && sanitized[len(sanitized)-1].Role == "model" {
				for _, p := range sanitized[len(sanitized)-1].Parts {
					if p.FunctionCall != nil {
						prevHasFuncCall = true
						break
					}
				}
			}

			if !prevHasFuncCall {
				// 前置缺失 FunctionCall (典型 Compaction 裁剪破损场景)
				// 将无法对应的 FunctionResponse 转译为纯文本 user 说明，避免触发 Google 400 严格校验
				var newParts []GooglePart
				for _, p := range current.Parts {
					if p.FunctionResponse != nil {
						respBytes, _ := json.Marshal(p.FunctionResponse.Response)
						newParts = append(newParts, GooglePart{
							Text: fmt.Sprintf("[Previous tool execution result for %s]: %s", p.FunctionResponse.Name, string(respBytes)),
						})
					} else {
						newParts = append(newParts, p)
					}
				}
				current.Parts = newParts
			}
		}

		// 检查当前 turn 是否有 FunctionCall
		var funcCalls []*GoogleFunctionCall
		for _, p := range current.Parts {
			if p.FunctionCall != nil {
				funcCalls = append(funcCalls, p.FunctionCall)
			}
		}

		sanitized = append(sanitized, current)

		// 如果当前 model turn 包含了 FunctionCall，Google 强制规定紧接着必须是 user turn 并且包含 FunctionResponse
		if len(funcCalls) > 0 && current.Role == "model" {
			// 检查下一个 turn 是否是 user 并且提供了对应的 FunctionResponse
			nextHasFuncResp := false
			if i+1 < len(merged) && merged[i+1].Role == "user" {
				for _, p := range merged[i+1].Parts {
					if p.FunctionResponse != nil {
						nextHasFuncResp = true
						break
					}
				}
			}

			if !nextHasFuncResp {
				// 发生孤立 FunctionCall (Compaction 或请求末尾被截断，缺失 tool response)
				// 自动注入合成的 user function response，满足 Google 协议的闭环校验要求
				var syntheticParts []GooglePart
				for _, fc := range funcCalls {
					syntheticParts = append(syntheticParts, GooglePart{
						FunctionResponse: &GoogleFunctionResponse{
							Name: fc.Name,
							Response: map[string]interface{}{
								"status": "success",
								"output": "[Auto-generated placeholder response for truncated compaction turn]",
							},
						},
					})
				}
				sanitized = append(sanitized, GoogleContent{
					Role:  "user",
					Parts: syntheticParts,
				})
			}
		}
	}

	// 步骤三：再次确保没有连续相同的 role（比如注入合成 user 后紧跟下一个 user）
	var finalContents []GoogleContent
	for _, c := range sanitized {
		if len(c.Parts) == 0 {
			continue
		}
		if len(finalContents) > 0 && finalContents[len(finalContents)-1].Role == c.Role {
			finalContents[len(finalContents)-1].Parts = append(finalContents[len(finalContents)-1].Parts, c.Parts...)
		} else {
			finalContents = append(finalContents, c)
		}
	}

	return finalContents
}

// cleanGeminiSchema 递归净化 JSON Schema，剔除 Google Gemini 不支持的元字段 ($schema, exclusiveMinimum 等)
func cleanGeminiSchema(schema interface{}) interface{} {
	switch v := schema.(type) {
	case map[string]interface{}:
		cleaned := make(map[string]interface{})
		for k, val := range v {
			trimmedKey := strings.TrimSpace(k)
			if trimmedKey == "" {
				continue
			}
			// 过滤掉 Google 不识别的元关键字
			if trimmedKey == "$schema" || trimmedKey == "$id" || trimmedKey == "$defs" || trimmedKey == "definitions" || trimmedKey == "title" || trimmedKey == "additionalProperties" || trimmedKey == "default" {
				continue
			}
			// 将 exclusiveMinimum/exclusiveMaximum 转为 Google 支持的 minimum/maximum
			if trimmedKey == "exclusiveMinimum" {
				cleaned["minimum"] = val
				continue
			}
			if trimmedKey == "exclusiveMaximum" {
				cleaned["maximum"] = val
				continue
			}
			cleaned[trimmedKey] = cleanGeminiSchema(val)
		}
		return cleaned
	case []interface{}:
		cleanedList := make([]interface{}, len(v))
		for i, item := range v {
			cleanedList[i] = cleanGeminiSchema(item)
		}
		return cleanedList
	default:
		return v
	}
}

// reportAuditLogAsync 异步向 dashboard-backend 上报审计与计量日志
func reportAuditLogAsync(reqID, rawAuthHeader, modelReq, modelUsed string, promptTokens, completionTokens int, latencyMs int, statusCode int, promptText, respText string, errText *string) {
	backendURL := os.Getenv("AUDIT_BACKEND_URL")
	if backendURL == "" {
		backendURL = "http://higress-dashboard-backend.higress-system.svc.cluster.local:4000/api/v1/internal/audit-log"
	}

	log.Printf("🚀 Invoked reportAuditLogAsync: reqID=%s, authHeader=%s, promptTokens=%d, compTokens=%d", reqID, rawAuthHeader, promptTokens, completionTokens)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("❌ Panic in reportAuditLogAsync: %v", r)
			}
		}()

		keyAlias := "unknown-higress"
		auth := strings.TrimPrefix(rawAuthHeader, "Bearer ")
		auth = strings.TrimSpace(auth)
		switch {
		case strings.Contains(auth, "cindy") || strings.HasPrefix(auth, "sk-cmlWk"):
			keyAlias = "cindy-higress"
		case strings.Contains(auth, "yui"):
			keyAlias = "yui-higress"
		case strings.Contains(auth, "hebe"):
			keyAlias = "hebe-higress"
		case strings.Contains(auth, "jayden"):
			keyAlias = "jayden-higress"
		default:
			if len(auth) > 8 {
				keyAlias = auth[:8] + "-higress"
			}
		}

		totalTokens := promptTokens + completionTokens
		// Gemini 3.8 Flash 定价: Prompt $0.075 / 1M, Completion $0.30 / 1M
		costUSD := (float64(promptTokens)*0.075 + float64(completionTokens)*0.30) / 1000000.0
		fxRate := 7.2300
		costCNY := costUSD * fxRate

		payload := map[string]interface{}{
			"id":                 reqID,
			"request_id":         reqID,
			"api_key_alias":      keyAlias,
			"model_requested":    modelReq,
			"model_used":         modelUsed,
			"provider":           "higress-gemini",
			"provider_key_alias": "OPENAI_API_KEY_FREE_3",
			"prompt_tokens":      promptTokens,
			"completion_tokens":  completionTokens,
			"reasoning_tokens":   0,
			"total_tokens":       totalTokens,
			"cost_usd":           costUSD,
			"cost_cny":           costCNY,
			"fx_rate":            fxRate,
			"latency_ms":         latencyMs,
			"status_code":        statusCode,
			"error_msg":          errText,
			"created_at":         time.Now().UTC(),
			"prompt":             promptText,
			"response":           respText,
		}

		b, _ := json.Marshal(payload)
		client := &http.Client{Timeout: 10 * time.Second}
		httpReq, err := http.NewRequest("POST", backendURL, bytes.NewReader(b))
		if err != nil {
			log.Printf("❌ Failed building audit request: %v", err)
			return
		}
		httpReq.Header.Set("Content-Type", "application/json")
		resp, doErr := client.Do(httpReq)
		if doErr != nil {
			log.Printf("❌ Failed sending audit log to %s: %v", backendURL, doErr)
		} else {
			log.Printf("📊 Successfully reported audit log to %s (status=%d, reqID=%s, key=%s)", backendURL, resp.StatusCode, reqID, keyAlias)
			_ = resp.Body.Close()
		}
	}()
}

// -----------------------------------------------------------------------------
// handleOpenAIStyle: 接收标准 OpenAI 格式，转译调用 Google AI Studio
// -----------------------------------------------------------------------------

func handleOpenAIStyle(c *gin.Context, defaultAPIKey string) {
	startTime := time.Now()
	rawAuthHeader := c.GetHeader("Authorization")
	if rawAuthHeader == "" {
		rawAuthHeader = c.GetHeader("x-api-key")
	}
	if rawAuthHeader == "" {
		rawAuthHeader = c.GetHeader("api-key")
	}

	key := defaultAPIKey
	if rawAuthHeader != "" {
		token := strings.TrimPrefix(rawAuthHeader, "Bearer ")
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

	// 提取结构化的 user_prompt 与 system_prompt 供 Dashboard 完美透视展示
	var userPrompt, systemPrompt string
	for i := len(req.Messages) - 1; i >= 0; i-- {
		m := req.Messages[i]
		if m.Role == "user" && userPrompt == "" {
			switch cv := m.Content.(type) {
			case string:
				userPrompt = cv
			default:
				cb, _ := json.Marshal(cv)
				userPrompt = string(cb)
			}
		}
		if m.Role == "system" && systemPrompt == "" {
			switch cv := m.Content.(type) {
			case string:
				systemPrompt = cv
			default:
				cb, _ := json.Marshal(cv)
				systemPrompt = string(cb)
			}
		}
	}
	promptMap := map[string]interface{}{
		"user_prompt":   userPrompt,
		"system_prompt": systemPrompt,
		"messages":      req.Messages,
		"tools":         req.Tools,
	}
	promptJSONBytes, _ := json.Marshal(promptMap)
	structuredPrompt := string(promptJSONBytes)

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
				// 丢弃占位符与非法短签名，防止 Base64 崩溃与 Corrupted 报错
				if strings.Contains(sig, "placeholder") || len(sig) < 20 {
					sig = ""
				}

				// 🎯 处理 thoughtSignature 与 Google 3.8 校验:
				// Google 对历史 tool_call 的 thought_signature 有服务端加密验签 (HMAC),
				// 伪造/串用占位符会直接触发 "Corrupted thought signature."
				// 若由于历史压缩/客户端未透传导致没有该 callID 的真实精确签名，
				// 最佳做法：将该历史 tool_call 转译为纯文本 assistant 描述，
				// Google 100% 当作正常对话历史接收，完全免除验签，确保会话永远平稳！
				if sig == "" {
					argsStr := tc.Function.Arguments
					parts = append(parts, GooglePart{
						Text: fmt.Sprintf("[Assistant invoked tool %s with args: %s]", tc.Function.Name, argsStr),
					})
				} else {
					parts = append(parts, GooglePart{
						ThoughtSignature: sig,
						FunctionCall: &GoogleFunctionCall{
							Name: tc.Function.Name,
							Args: args,
							ID:   tc.ID,
						},
					})
				}
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

	// 🎯 核心消息流净化与修复 (Sanitize turns for Google Gemini API):
	// 彻底解决: "Please ensure that function call turn comes immediately after a user turn or after a function response turn."
	gReq.Contents = sanitizeGoogleTurns(gReq.Contents)

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
					Parameters:  cleanGeminiSchema(t.Function.Parameters),
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
	log.Printf("👉 Generated Google Payload Size: %d, Content: %s", len(gReqBytes), string(gReqBytes))

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
		var streamToolCalls []gin.H
		var sources []string
		seenSources := make(map[string]bool)
		fullResponseText := ""
		promptTokens := 0
		completionTokens := 0

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
					fullResponseText += part.Text
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
					}

					tcObj := gin.H{
						"id":   callID,
						"type": "function",
						"function": gin.H{
							"name":      part.FunctionCall.Name,
							"arguments": string(argsBytes),
						},
					}
					streamToolCalls = append(streamToolCalls, tcObj)

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
					promptTokens = gResp.UsageMetadata.PromptTokenCount
					completionTokens = gResp.UsageMetadata.CandidatesTokenCount
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

		// 异步上报审计日志至 dashboard-backend
		latency := int(time.Since(startTime).Milliseconds())
		respMap := map[string]interface{}{
			"reply":      fullResponseText,
			"tool_calls": streamToolCalls,
		}
		respJSONBytes, _ := json.Marshal(respMap)
		reportAuditLogAsync(chatCmplID, rawAuthHeader, model, realModel, promptTokens, completionTokens, latency, http.StatusOK, structuredPrompt, string(respJSONBytes), nil)
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

	// 异步上报审计日志至 dashboard-backend
	pTokens := 0
	cTokens := 0
	if gResp.UsageMetadata != nil {
		pTokens = gResp.UsageMetadata.PromptTokenCount
		cTokens = gResp.UsageMetadata.CandidatesTokenCount
	}
	latency := int(time.Since(startTime).Milliseconds())
	respMap := map[string]interface{}{
		"reply":      outContent,
		"tool_calls": outToolCalls,
	}
	respJSONBytes, _ := json.Marshal(respMap)
	reportAuditLogAsync(chatCmplID, rawAuthHeader, model, realModel, pTokens, cTokens, latency, http.StatusOK, structuredPrompt, string(respJSONBytes), nil)
}

// stripThoughtSignaturePlaceholder 递归清洗客户端注入的非法伪造签名 (如 thought_signature_placeholder)
func stripThoughtSignaturePlaceholder(v interface{}) interface{} {
	switch val := v.(type) {
	case map[string]interface{}:
		cleaned := make(map[string]interface{})
		for k, item := range val {
			if k == "thought_signature" || k == "thoughtSignature" {
				if s, ok := item.(string); ok && (strings.Contains(s, "placeholder") || len(s) < 20) {
					continue // 丢弃非法占位符
				}
			}
			cleaned[k] = stripThoughtSignaturePlaceholder(item)
		}
		return cleaned
	case []interface{}:
		cleanedList := make([]interface{}, len(val))
		for i, item := range val {
			cleanedList[i] = stripThoughtSignaturePlaceholder(item)
		}
		return cleanedList
	default:
		return v
	}
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
		// 递归剥离占位符
		cleanedBody := stripThoughtSignaturePlaceholder(bodyMap)
		rawBody, _ = json.Marshal(cleanedBody)
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
