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
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

var thoughtSigCache sync.Map // map[string]string: callID / toolName -> thoughtSignature
var callIDToName sync.Map    // map[string]string: callID -> functionName

var pseudoCallPrefixRegex = regexp.MustCompile("(?is)(?:\\[?Assistant\\s+|I\\s+)?(?:invoked|called|executed)\\s+(?:tool\\s+)?[`'\"]?([\\w\\:\\-]+)[`'\"]?\\s+with\\s+(?:args|arguments|input):\\s*")

// extractBalancedJSON 寻找从 text 开始或从 '{' 开始的有效最外层 JSON 对象，准确跳过字符串内包含的转义字符与嵌套大括号
func extractBalancedJSON(s string) (string, bool) {
	start := strings.IndexByte(s, '{')
	if start == -1 {
		return "", false
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(s); i++ {
		b := s[i]
		if escaped {
			escaped = false
			continue
		}
		if b == '\\' {
			if inString {
				escaped = true
			}
			continue
		}
		if b == '"' {
			inString = !inString
			continue
		}
		if !inString {
			if b == '{' {
				depth++
			} else if b == '}' {
				depth--
				if depth == 0 {
					return s[start : i+1], true
				}
			}
		}
	}
	return "", false
}

var pseudoCallStarters = []string{
	"i called",
	"i executed",
	"called tool",
	"assistant invoked",
	"[assistant invoked",
	"invoked tool",
	"i invoked",
}

func isPossiblePseudoCallStart(text string) bool {
	trimmed := strings.TrimSpace(text)
	lower := strings.ToLower(trimmed)
	if lower == "" {
		return false
	}
	for _, s := range pseudoCallStarters {
		if strings.HasPrefix(s, lower) || strings.HasPrefix(lower, s) {
			return true
		}
	}
	return false
}

// parsePseudoToolCall 解析可能被历史提示词污染的伪工具调用纯文本
func parsePseudoToolCall(text string) (string, string, bool) {
	loc := pseudoCallPrefixRegex.FindStringSubmatchIndex(text)
	if len(loc) >= 4 {
		fnName := text[loc[2]:loc[3]]
		remainder := text[loc[1]:]
		if jsonStr, ok := extractBalancedJSON(remainder); ok {
			var js map[string]interface{}
			if err := json.Unmarshal([]byte(jsonStr), &js); err == nil {
				return fnName, jsonStr, true
			}
		}
	}
	return "", "", false
}

func emitTextDelta(c *gin.Context, flusher http.Flusher, chatCmplID string, created int64, model string, text string) {
	chunk := gin.H{
		"id":      chatCmplID,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   model,
		"choices": []gin.H{
			{
				"index":         0,
				"delta":         gin.H{"content": text},
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

func emitToolCallDelta(c *gin.Context, flusher http.Flusher, chatCmplID string, created int64, model string, callID, fnName, fnArgs string) {
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
								"name":      fnName,
								"arguments": fnArgs,
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

func main() {
	// 加载模型与路由配置 (支持 config.yaml 或环境变量)
	if _, err := LoadConfig(""); err != nil {
		log.Printf("⚠️ Warning initializing config: %v", err)
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	apiKey := os.Getenv("GEMINI_API_KEY")
	a6ApiKey := os.Getenv("A6_API_KEY")

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
			handleOpenAIStyle(c, apiKey, a6ApiKey)
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
			{"id": "kimi-k3", "object": "model", "owned_by": "moonshot"},
			{"id": "glm-5.3", "object": "model", "owned_by": "zhipu"},
			{"id": "gpt-5.6-luna", "object": "model", "owned_by": "openai"},
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
	FunctionCallingConfig           *GoogleFunctionCallingConfig `json:"function_calling_config,omitempty"`
	IncludeServerSideToolInvocations bool                         `json:"include_server_side_tool_invocations,omitempty"`
}

type GoogleFunctionCallingConfig struct {
	Mode string `json:"mode,omitempty"`
}

type GoogleContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []GooglePart `json:"parts"`
}

type GooglePart struct {
	Text             string                  `json:"text,omitempty"`
	Thought          bool                    `json:"thought,omitempty"`
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
	PromptTokenCount        int `json:"promptTokenCount"`
	CandidatesTokenCount    int `json:"candidatesTokenCount"`
	TotalTokenCount         int `json:"totalTokenCount"`
	CachedContentTokenCount int `json:"cachedContentTokenCount,omitempty"`
	ThoughtsTokenCount      int `json:"thoughtsTokenCount,omitempty"`
}

// sanitizeGoogleTurns 解决 Google Gemini 原生对话时序强校验限制:
// "Please ensure that function call turn comes immediately after a user turn or after a function response turn."
// 规则剖析:
// 1. 任何带有 functionCall 的 model turn，其前置 turn 必须是纯 user turn 或者纯 functionResponse turn。
// 2. 任何带有 functionCall 的 model turn，紧随其后的必须是包含匹配 functionResponse 的 user turn。
// 3. 任何包含 functionResponse 的 user turn，前面必须紧邻着发出匹配 functionCall 的 model turn。
// 4. 不得在包含 functionResponse 的同一个 user turn 中混杂普通 Text，防止时序歧义。
// 5. 严格交替: 连续相同的 role (user 连 user, 或 model 连 model) 进行 Parts 合并，绝不产生非法同角色切块。
// 6. 严禁开局第一轮为 model turn（首轮必须是 user）。
// 7. 当因为截断/Compaction 导致 functionCall 无法配对时，优雅降级为纯文本，或者补齐占位 functionResponse，确保 100% 满足 Google 状态机。
func sanitizeGoogleTurns(contents []GoogleContent) []GoogleContent {
	if len(contents) == 0 {
		return contents
	}

	// 步骤一：初始合并相邻相同 Role 的 Content，剔除空 Parts
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

	// 步骤二：校验并降级非法/孤立的 FunctionResponse 与 FunctionCall
	// 建立有效呼叫匹配集合
	var sanitized []GoogleContent
	for i := 0; i < len(merged); i++ {
		current := merged[i]

		if current.Role == "user" {
			// 检查是否包含 FunctionResponse
			var funcResponses []*GoogleFunctionResponse
			var textParts []GooglePart
			for _, p := range current.Parts {
				if p.FunctionResponse != nil {
					funcResponses = append(funcResponses, p.FunctionResponse)
				} else {
					textParts = append(textParts, p)
				}
			}

			if len(funcResponses) > 0 {
				// 检查前一个 turn 是否是包含对应 FunctionCall 的 model turn
				prevCallNames := make(map[string]bool)
				if len(sanitized) > 0 && sanitized[len(sanitized)-1].Role == "model" {
					for _, p := range sanitized[len(sanitized)-1].Parts {
						if p.FunctionCall != nil {
							prevCallNames[p.FunctionCall.Name] = true
						}
					}
				}

				if len(prevCallNames) == 0 {
					// 前面根本没有 model functionCall，全部降级为 Text
					for _, fr := range funcResponses {
						respBytes, _ := json.Marshal(fr.Response)
						textParts = append(textParts, GooglePart{
							Text: fmt.Sprintf("Tool result for %s:\n%s", fr.Name, string(respBytes)),
						})
					}
					current.Parts = textParts
				} else {
					// 前面有 FunctionCall，拆分合法的 FunctionResponse 与无法匹配的孤立项
					var validRespParts []GooglePart
					for _, fr := range funcResponses {
						if prevCallNames[fr.Name] {
							validRespParts = append(validRespParts, GooglePart{
								FunctionResponse: fr,
							})
						} else {
							respBytes, _ := json.Marshal(fr.Response)
							textParts = append(textParts, GooglePart{
								Text: fmt.Sprintf("Tool result for %s:\n%s", fr.Name, string(respBytes)),
							})
						}
					}

					// Google 要求: 带有 functionResponse 的 user turn 不应和普通用户提问混杂在同一 turn
					// 若同时存在 textParts 与 validRespParts，分别生成两个 user turn（后续由合成逻辑处理交替）
					if len(textParts) > 0 {
						sanitized = append(sanitized, GoogleContent{
							Role:  "user",
							Parts: textParts,
						})
					}
					if len(validRespParts) > 0 {
						current.Parts = validRespParts
					} else {
						// 全部都是无效的 response，已加入 textParts，当前循环结束
						continue
					}
				}
			}
		}

		// 检查当前 turn 是否为 model 且包含 FunctionCall
		var funcCalls []*GoogleFunctionCall
		if current.Role == "model" {
			for _, p := range current.Parts {
				if p.FunctionCall != nil {
					funcCalls = append(funcCalls, p.FunctionCall)
				}
			}
		}

		sanitized = append(sanitized, current)

		// 如果当前 model turn 包含了 FunctionCall，Google 强制规定紧接着必须是 user turn 并且包含匹配的 FunctionResponse
		if len(funcCalls) > 0 && current.Role == "model" {
			nextHasMatchingResp := false
			if i+1 < len(merged) && merged[i+1].Role == "user" {
				// 检查下一轮是否包含任意一个匹配的 tool response
				for _, p := range merged[i+1].Parts {
					if p.FunctionResponse != nil {
						for _, fc := range funcCalls {
							if fc.Name == p.FunctionResponse.Name {
								nextHasMatchingResp = true
								break
							}
						}
					}
				}
			}

			if !nextHasMatchingResp {
				// 发生孤立 FunctionCall，自动注入匹配的合成 user function response
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

	// 步骤三：再次严格归一化交替关系
	// 1. 合并连续相同 Role（但若前一个 user 含有 functionResponse 且后一个包含 text，则为了保持 Google 规范，合并为纯 user 块）
	// 2. 确保第一轮必须是 user（如果第一轮是 model，合成前置空 user）
	var normalized []GoogleContent
	for _, c := range sanitized {
		if len(c.Parts) == 0 {
			continue
		}
		if len(normalized) == 0 && c.Role == "model" {
			normalized = append(normalized, GoogleContent{
				Role:  "user",
				Parts: []GooglePart{{Text: "Hello"}},
			})
		}
		if len(normalized) > 0 && normalized[len(normalized)-1].Role == c.Role {
			normalized[len(normalized)-1].Parts = append(normalized[len(normalized)-1].Parts, c.Parts...)
		} else {
			normalized = append(normalized, c)
		}
	}

	// 步骤四：最后一道防线 —— 扫描整体序列，确保没有任何孤立或乱序
	var finalContents []GoogleContent
	for idx, c := range normalized {
		if c.Role == "user" {
			hasFuncResp := false
			for _, p := range c.Parts {
				if p.FunctionResponse != nil {
					hasFuncResp = true
					break
				}
			}
			if hasFuncResp {
				// 严格校验前置 turn 是否存在 FunctionCall
				prevCallExists := false
				if idx > 0 && finalContents[len(finalContents)-1].Role == "model" {
					for _, p := range finalContents[len(finalContents)-1].Parts {
						if p.FunctionCall != nil {
							prevCallExists = true
							break
						}
					}
				}
				if !prevCallExists {
					// 兜底降级该 turn 内的所有 functionResponse 为纯 Text
					var safeParts []GooglePart
					for _, p := range c.Parts {
						if p.FunctionResponse != nil {
							rb, _ := json.Marshal(p.FunctionResponse.Response)
							safeParts = append(safeParts, GooglePart{
								Text: fmt.Sprintf("Tool result for %s:\n%s", p.FunctionResponse.Name, string(rb)),
							})
						} else {
							safeParts = append(safeParts, p)
						}
					}
					c.Parts = safeParts
				}
			}
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
			if trimmedKey == "$schema" || trimmedKey == "$id" || trimmedKey == "$defs" || trimmedKey == "definitions" || trimmedKey == "title" || trimmedKey == "additionalProperties" || trimmedKey == "patternProperties" || trimmedKey == "default" {
				continue
			}
			// 将 const: X 转为 enum: [X]，因为 Gemini 不支持 const 关键字
			if trimmedKey == "const" {
				cleaned["enum"] = []interface{}{cleanGeminiSchema(val)}
				continue
			}
			// Gemini 对 anyOf / allOf / oneOf 的支持限制极大，如果其中包含 const 转换或不支持类型，会导致报错。
			// 递归处理子项
			if trimmedKey == "anyOf" || trimmedKey == "any_of" {
				cleaned["anyOf"] = cleanGeminiSchema(val)
				continue
			}
			if trimmedKey == "oneOf" || trimmedKey == "one_of" {
				cleaned["oneOf"] = cleanGeminiSchema(val)
				continue
			}
			if trimmedKey == "allOf" || trimmedKey == "all_of" {
				cleaned["allOf"] = cleanGeminiSchema(val)
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
func reportAuditLogAsync(reqID, rawAuthHeader, modelReq, modelUsed string, promptTokens, completionTokens, reasoningTokens, cacheReadTokens int, latencyMs int, statusCode int, promptText, respText string, errText *string) {
	backendURL := os.Getenv("AUDIT_BACKEND_URL")
	if backendURL == "" {
		backendURL = "http://higress-dashboard-backend.higress-system.svc.cluster.local:4000/api/v1/internal/audit-log"
	}

	log.Printf("🚀 Invoked reportAuditLogAsync: reqID=%s, authHeader=%s, promptTokens=%d, compTokens=%d, reasoningTokens=%d, cacheTokens=%d", reqID, rawAuthHeader, promptTokens, completionTokens, reasoningTokens, cacheReadTokens)

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
		case strings.Contains(auth, "moon"):
			keyAlias = "moon-higress"
		case strings.Contains(auth, "stella"):
			keyAlias = "stella-higress"
		case strings.Contains(auth, "alice"):
			keyAlias = "alice-higress"
		case strings.Contains(auth, "claire"):
			keyAlias = "claire-higress"
		case strings.Contains(auth, "leona"):
			keyAlias = "leona-higress"
		case strings.Contains(auth, "aurora"):
			keyAlias = "aurora-higress"
		case strings.Contains(auth, "rin"):
			keyAlias = "rin-higress"
		case strings.Contains(auth, "celia"):
			keyAlias = "celia-higress"
		case strings.Contains(auth, "jayden"):
			keyAlias = "jayden-higress"
		default:
			if len(auth) > 8 {
				keyAlias = auth[:8] + "-higress"
			}
		}

		totalTokens := promptTokens + completionTokens
		provider := "higress-gemini"
		providerKeyAlias := "OPENAI_API_KEY_FREE_3"

		// 默认费率计算基准 (USD / 1M Tokens)
		promptPrice := 0.075
		compPrice := 0.30
		cachePrice := 0.01875

		// 🎯 动态模型配置与费率解析
		mCfg := FindModelConfig(modelReq)
		if mCfg == nil && modelUsed != "" {
			mCfg = FindModelConfig(modelUsed)
		}

		var costUSD float64
		if mCfg != nil {
			provider = mCfg.Provider
			if mCfg.APIKey != "" {
				providerKeyAlias = strings.TrimPrefix(mCfg.APIKey, "os.environ/")
			}
			costUSD, promptPrice, cachePrice, compPrice = mCfg.CalculatePrice(promptTokens, cacheReadTokens, completionTokens)
		} else {
			// 备用兜底逻辑: 阶梯计费 > 128k
			if promptTokens > 128000 {
				promptPrice = 0.150
				compPrice = 0.600
				cachePrice = 0.0375
			}
			nonCachedTokens := promptTokens - cacheReadTokens
			if nonCachedTokens < 0 {
				nonCachedTokens = 0
			}
			costUSD = (float64(nonCachedTokens)*promptPrice + float64(cacheReadTokens)*cachePrice + float64(completionTokens)*compPrice) / 1000000.0
		}
		fxRate := 7.2300
		costCNY := costUSD * fxRate

		payload := map[string]interface{}{
			"id":                         reqID,
			"request_id":                 reqID,
			"api_key_alias":              keyAlias,
			"model_requested":            modelReq,
			"model_used":                 modelUsed,
			"provider":                   provider,
			"provider_key_alias":         providerKeyAlias,
			"prompt_tokens":              promptTokens,
			"completion_tokens":          completionTokens,
			"reasoning_tokens":           reasoningTokens,
			"cache_read_tokens":          cacheReadTokens,
			"total_tokens":               totalTokens,
			"prompt_unit_price_usd":      promptPrice,
			"cache_unit_price_usd":       cachePrice,
			"completion_unit_price_usd":  compPrice,
			"cost_usd":                   costUSD,
			"cost_cny":                   costCNY,
			"fx_rate":                    fxRate,
			"latency_ms":                 latencyMs,
			"status_code":                statusCode,
			"error_msg":                  errText,
			"created_at":                 time.Now().UTC(),
			"prompt":                     promptText,
			"response":                   respText,
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

// handleOpenAIUpstreamProxy 根据 config.yaml 配置将 OpenAI 协议请求转发至上游并处理降级与审计
func handleOpenAIUpstreamProxy(c *gin.Context, req OpenAIChatRequest, mCfg *ModelConfig, apiKey, rawAuthHeader string, startTime time.Time, structuredPrompt string) {
	if apiKey == "" {
		log.Printf("❌ Critical: Upstream API key is empty for model %s", req.Model)
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("configuration error: API key is empty for %s", req.Model)})
		return
	}

	targetModel := req.Model
	if mCfg.UpstreamModel != "" {
		targetModel = mCfg.UpstreamModel
	}
	actualUsedModel := targetModel
	req.Model = targetModel

	reqBytes, err := json.Marshal(req)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "failed serializing request"})
		return
	}

	upstreamURL := mCfg.UpstreamURL
	if upstreamURL == "" {
		upstreamURL = "https://api.a6api.com/v1/chat/completions"
	}

	httpReq, err := http.NewRequestWithContext(c.Request.Context(), "POST", upstreamURL, bytes.NewReader(reqBytes))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("User-Agent", "higress-ai-gateway/2.0")

	client := &http.Client{Timeout: 180 * time.Second}
	resp, err := client.Do(httpReq)

	// 🛡️ 声明式熔断降级 (Fallback): 根据 config.yaml 的 fallback 字段
	fallbackModel := mCfg.Fallback
	if fallbackModel != "" && (err != nil || resp == nil || resp.StatusCode >= 500 || resp.StatusCode == 429) {
		failReason := "network error"
		if resp != nil {
			failReason = fmt.Sprintf("status %d", resp.StatusCode)
			_ = resp.Body.Close()
		}
		log.Printf("⚠️ Model %s failed (%s), automatically falling back to %s backup...", targetModel, failReason, fallbackModel)
		actualUsedModel = fallbackModel
		req.Model = fallbackModel

		fallbackURL := upstreamURL
		fallbackKey := apiKey
		fbCfg := FindModelConfig(fallbackModel)
		if fbCfg != nil {
			if fbCfg.UpstreamURL != "" {
				fallbackURL = fbCfg.UpstreamURL
			}
			if fbCfg.ResolveKey() != "" {
				fallbackKey = fbCfg.ResolveKey()
			}
		}

		if fallbackBytes, bErr := json.Marshal(req); bErr == nil {
			if fbReq, fbErr := http.NewRequestWithContext(c.Request.Context(), "POST", fallbackURL, bytes.NewReader(fallbackBytes)); fbErr == nil {
				fbReq.Header.Set("Content-Type", "application/json")
				fbReq.Header.Set("Authorization", "Bearer "+fallbackKey)
				fbReq.Header.Set("User-Agent", "higress-ai-gateway/2.0")
				if fbResp, doErr := client.Do(fbReq); doErr == nil {
					resp = fbResp
					err = nil
					log.Printf("✅ Fallback to %s succeeded with status %d", fallbackModel, resp.StatusCode)
				}
			}
		}
	}

	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("A6 API request failed: %v", err)})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		errBody, _ := io.ReadAll(resp.Body)
		c.Data(resp.StatusCode, resp.Header.Get("Content-Type"), errBody)
		return
	}

	chatCmplID := "chatcmpl-" + uuid.New().String()

	if req.Stream {
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		flusher, _ := c.Writer.(http.Flusher)

		scanner := bufio.NewScanner(resp.Body)
		buf := make([]byte, 64*1024)
		scanner.Buffer(buf, 1024*1024)

		fullResponseText := ""
		promptTokens := 0
		completionTokens := 0
		reasoningTokens := 0
		cacheReadTokens := 0
		var streamToolCalls []gin.H

		for scanner.Scan() {
			line := scanner.Text()
			if strings.TrimSpace(line) == "" {
				continue
			}
			_, _ = c.Writer.Write([]byte(line + "\n\n"))
			if flusher != nil {
				flusher.Flush()
			}

			if !strings.HasPrefix(line, "data: ") || strings.TrimSpace(line) == "data: [DONE]" {
				continue
			}

			jsonStr := strings.TrimPrefix(line, "data: ")
			var chunkMap map[string]interface{}
			if err := json.Unmarshal([]byte(jsonStr), &chunkMap); err == nil {
				if id, ok := chunkMap["id"].(string); ok && id != "" {
					chatCmplID = id
				}
				if usage, ok := chunkMap["usage"].(map[string]interface{}); ok {
					if pt, ok := usage["prompt_tokens"].(float64); ok {
						promptTokens = int(pt)
					}
					if ct, ok := usage["completion_tokens"].(float64); ok {
						completionTokens = int(ct)
					}
					if ctd, ok := usage["completion_tokens_details"].(map[string]interface{}); ok {
						if rt, ok := ctd["reasoning_tokens"].(float64); ok {
							reasoningTokens = int(rt)
						}
					}
					if ptd, ok := usage["prompt_tokens_details"].(map[string]interface{}); ok {
						if ct, ok := ptd["cached_tokens"].(float64); ok {
							cacheReadTokens = int(ct)
						}
					}
				}
				if choices, ok := chunkMap["choices"].([]interface{}); ok && len(choices) > 0 {
					if ch, ok := choices[0].(map[string]interface{}); ok {
						if delta, ok := ch["delta"].(map[string]interface{}); ok {
							if cnt, ok := delta["content"].(string); ok {
								fullResponseText += cnt
							}
							if tcs, ok := delta["tool_calls"].([]interface{}); ok {
								for _, tcItem := range tcs {
									if tcMap, ok := tcItem.(map[string]interface{}); ok {
										streamToolCalls = append(streamToolCalls, tcMap)
									}
								}
							}
						}
					}
				}
			}
		}

		latency := int(time.Since(startTime).Milliseconds())
		respMap := map[string]interface{}{
			"reply":      fullResponseText,
			"tool_calls": streamToolCalls,
		}
		respJSONBytes, _ := json.Marshal(respMap)
		reportAuditLogAsync(chatCmplID, rawAuthHeader, req.Model, actualUsedModel, promptTokens, completionTokens, reasoningTokens, cacheReadTokens, latency, http.StatusOK, structuredPrompt, string(respJSONBytes), nil)
		return
	}

	// 非流式响应
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed reading A6 response"})
		return
	}

	var resMap map[string]interface{}
	if err := json.Unmarshal(respBody, &resMap); err == nil {
		if id, ok := resMap["id"].(string); ok && id != "" {
			chatCmplID = id
		}
	}

	c.Data(resp.StatusCode, "application/json", respBody)

	pTokens := 0
	cTokens := 0
	rTokens := 0
	cacheTokens := 0
	replyText := ""
	var outToolCalls []gin.H

	if resMap != nil {
		if usage, ok := resMap["usage"].(map[string]interface{}); ok {
			if pt, ok := usage["prompt_tokens"].(float64); ok {
				pTokens = int(pt)
			}
			if ct, ok := usage["completion_tokens"].(float64); ok {
				cTokens = int(ct)
			}
			if ctd, ok := usage["completion_tokens_details"].(map[string]interface{}); ok {
				if rt, ok := ctd["reasoning_tokens"].(float64); ok {
					rTokens = int(rt)
				}
			}
			if ptd, ok := usage["prompt_tokens_details"].(map[string]interface{}); ok {
				if ct, ok := ptd["cached_tokens"].(float64); ok {
					cacheTokens = int(ct)
				}
			}
		}
		if choices, ok := resMap["choices"].([]interface{}); ok && len(choices) > 0 {
			if ch, ok := choices[0].(map[string]interface{}); ok {
				if msg, ok := ch["message"].(map[string]interface{}); ok {
					if cnt, ok := msg["content"].(string); ok {
						replyText = cnt
					}
					if tcs, ok := msg["tool_calls"].([]interface{}); ok {
						for _, tcItem := range tcs {
							if tcMap, ok := tcItem.(map[string]interface{}); ok {
								outToolCalls = append(outToolCalls, tcMap)
							}
						}
					}
				}
			}
		}
	}

	latency := int(time.Since(startTime).Milliseconds())
	respMap := map[string]interface{}{
		"reply":      replyText,
		"tool_calls": outToolCalls,
	}
	respJSONBytes, _ := json.Marshal(respMap)
	reportAuditLogAsync(chatCmplID, rawAuthHeader, req.Model, actualUsedModel, pTokens, cTokens, rTokens, cacheTokens, latency, http.StatusOK, structuredPrompt, string(respJSONBytes), nil)
}

// -----------------------------------------------------------------------------
// handleOpenAIStyle: 接收标准 OpenAI 格式，转译调用 Google AI Studio
// -----------------------------------------------------------------------------

func handleOpenAIStyle(c *gin.Context, defaultAPIKey, a6ApiKey string) {
	startTime := time.Now()
	rawAuthHeader := c.GetHeader("Authorization")
	if rawAuthHeader == "" {
		rawAuthHeader = c.GetHeader("x-api-key")
	}
	if rawAuthHeader == "" {
		rawAuthHeader = c.GetHeader("api-key")
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

	// 🎯 动态模型路由匹配 (基于 config.yaml 声明)
	mCfg := FindModelConfig(req.Model)
	if mCfg != nil && mCfg.Protocol == "openai" {
		upstreamKey := mCfg.ResolveKey()
		if upstreamKey == "" {
			upstreamKey = a6ApiKey
		}
		handleOpenAIUpstreamProxy(c, req, mCfg, upstreamKey, rawAuthHeader, startTime, structuredPrompt)
		return
	}

	key := defaultAPIKey
	if mCfg != nil && mCfg.ResolveKey() != "" {
		key = mCfg.ResolveKey()
	}
	if key == "" {
		key = os.Getenv("GEMINI_API_KEY")
	}
	if rawAuthHeader != "" {
		token := strings.TrimPrefix(rawAuthHeader, "Bearer ")
		token = strings.TrimSpace(token)
		if strings.HasPrefix(token, "AIza") {
			key = token
		}
	}
	if key == "" {
		log.Printf("❌ Critical: GEMINI_API_KEY is not configured or empty")
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "gemini-adapter configuration error: GEMINI_API_KEY is empty",
		})
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

	// 预先建立本轮请求内所有 tool_calls 的 ID -> Name 快速反查表与缺失签名调用集合
	localToolMap := make(map[string]string)
	missingSigCallIDs := make(map[string]bool)
	for _, m := range req.Messages {
		if m.Role == "assistant" {
			for _, tc := range m.ToolCalls {
				if tc.ID != "" && tc.Function.Name != "" {
					localToolMap[tc.ID] = tc.Function.Name
					callIDToName.Store(tc.ID, tc.Function.Name)

					sig := ""
					if v, ok := thoughtSigCache.Load(tc.ID); ok {
						sig = v.(string)
					}
					if strings.Contains(sig, "placeholder") || len(sig) < 20 {
						sig = ""
					}
					if sig == "" {
						missingSigCallIDs[tc.ID] = true
					}
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
				// 🎯 若历史文本中包含了旧版本残留的 pseudo tool call，将其转化为干净的自然语言描述
				if fnName, fnArgs, ok := parsePseudoToolCall(contentStr); ok {
					parts = append(parts, GooglePart{
						Text: fmt.Sprintf("I executed `%s` with input: %s", fnName, fnArgs),
					})
				} else {
					parts = append(parts, GooglePart{Text: contentStr})
				}
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

				if sig != "" {
					// 拥有真实 thoughtSignature，安全作为原生 FunctionCall 发送给 Google
					parts = append(parts, GooglePart{
						ThoughtSignature: sig,
						FunctionCall: &GoogleFunctionCall{
							Name: tc.Function.Name,
							Args: args,
							ID:   tc.ID,
						},
					})
				} else {
					// 🎯 核心解决: Google Gemini 3.8 校验
					// "Function call is missing a thought_signature in functionCall parts."
					// 当丢失精确 thoughtSignature 时（例如 Pod 重启），该次调用与匹配的工具响应必须同步以自然语言呈现，
					// 彻底终结 Google 对 thought_signature 的强制验签，同时保持历史时序的完美连贯！
					argsStr := tc.Function.Arguments
					parts = append(parts, GooglePart{
						Text: fmt.Sprintf("I executed `%s` with input: %s", tc.Function.Name, argsStr),
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

			// 🎯 若前置的 tool call 丢失了 thoughtSignature 并转为自然语言，此工具响应必须同步以自然语言呈现！
			if missingSigCallIDs[msg.ToolCallID] {
				gReq.Contents = append(gReq.Contents, GoogleContent{
					Role: "user",
					Parts: []GooglePart{
						{
							Text: fmt.Sprintf("Tool output for `%s`:\n%s", toolName, contentStr),
						},
					},
				})
				continue
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
	if mCfg != nil && mCfg.UpstreamModel != "" {
		realModel = mCfg.UpstreamModel
	} else if enableSearch {
		realModel = strings.TrimSuffix(strings.TrimSuffix(model, "-search"), ":search")
	}
	if mCfg != nil && mCfg.EnableSearch {
		enableSearch = true
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
			if gReq.ToolConfig == nil {
				gReq.ToolConfig = &GoogleToolConfig{}
			}
			gReq.ToolConfig.IncludeServerSideToolInvocations = true
		}
	}

	// 防御机制：当客户端未提供 tools（如 OpenCode 在 session compaction / 摘要模式下，严禁任何 tool-call）
	// 主动向 Google 声明 function_calling_config.mode = "NONE"，杜绝模型根据历史上下文幻觉发起工具调用
	if len(req.Tools) == 0 {
		if gReq.ToolConfig == nil {
			gReq.ToolConfig = &GoogleToolConfig{}
		}
		gReq.ToolConfig.FunctionCallingConfig = &GoogleFunctionCallingConfig{
			Mode: "NONE",
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
		reasoningTokens := 0
		cacheReadTokens := 0
		var pendingPseudoBuf strings.Builder

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

			// 持续更新 Token 统计（Google Gemini 流式中 UsageMetadata 可能出现在最后的统计帧中）
			if gResp.UsageMetadata != nil {
				promptTokens = gResp.UsageMetadata.PromptTokenCount
				completionTokens = gResp.UsageMetadata.CandidatesTokenCount
				reasoningTokens = gResp.UsageMetadata.ThoughtsTokenCount
				if gResp.UsageMetadata.CachedContentTokenCount > 0 {
					cacheReadTokens = gResp.UsageMetadata.CachedContentTokenCount
				}
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
					textChunk := part.Text

					// 🎯 核心防幻觉拦截: 捕获并缓冲以 "I called tool", "Assistant invoked tool" 等开头的流式片段
					if pendingPseudoBuf.Len() > 0 || isPossiblePseudoCallStart(textChunk) {
						pendingPseudoBuf.WriteString(textChunk)
						buffered := pendingPseudoBuf.String()
						// 如果包含了闭合大括号 '}'，尝试完整匹配解析
						if strings.Contains(buffered, "}") {
							if fnName, fnArgs, ok := parsePseudoToolCall(buffered); ok && len(req.Tools) > 0 {
								hasToolCalls = true
								callID := "call_" + uuid.New().String()[:8]
								callIDToName.Store(callID, fnName)
								tcObj := gin.H{
									"id":   callID,
									"type": "function",
									"function": gin.H{
										"name":      fnName,
										"arguments": fnArgs,
									},
								}
								streamToolCalls = append(streamToolCalls, tcObj)
								emitToolCallDelta(c, flusher, chatCmplID, created, model, callID, fnName, fnArgs)
								pendingPseudoBuf.Reset()
								continue
							}
						}
						// 若缓冲区的现有前缀已不再可能是任何已知的伪调用前缀，立即刷盘释放（保证正常文本的零延迟）
						if !isPossiblePseudoCallStart(buffered) {
							flushStr := pendingPseudoBuf.String()
							pendingPseudoBuf.Reset()
							fullResponseText += flushStr
							emitTextDelta(c, flusher, chatCmplID, created, model, flushStr)
							continue
						}
						// 如果累积超过 65536 字符且仍然无法匹配为工具调用，放弃缓冲并正常下发
						if pendingPseudoBuf.Len() > 65536 {
							flushStr := pendingPseudoBuf.String()
							pendingPseudoBuf.Reset()
							fullResponseText += flushStr
							emitTextDelta(c, flusher, chatCmplID, created, model, flushStr)
						}
						continue
					}

					fullResponseText += textChunk
					emitTextDelta(c, flusher, chatCmplID, created, model, textChunk)
				}

				if part.FunctionCall != nil {
					if len(req.Tools) == 0 {
						log.Printf("⚠️ Suppressing unexpected FunctionCall '%s' from Google upstream because client requested NO tools", part.FunctionCall.Name)
						continue
					}
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
				if promptTokens > 0 || completionTokens > 0 || reasoningTokens > 0 {
					usage = gin.H{
						"prompt_tokens":     promptTokens,
						"completion_tokens": completionTokens,
						"total_tokens":      promptTokens + completionTokens,
						"completion_tokens_details": gin.H{
							"reasoning_tokens": reasoningTokens,
						},
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

		if pendingPseudoBuf.Len() > 0 {
			buffered := pendingPseudoBuf.String()
			if fnName, fnArgs, ok := parsePseudoToolCall(buffered); ok && len(req.Tools) > 0 {
				hasToolCalls = true
				callID := "call_" + uuid.New().String()[:8]
				callIDToName.Store(callID, fnName)
				tcObj := gin.H{
					"id":   callID,
					"type": "function",
					"function": gin.H{
						"name":      fnName,
						"arguments": fnArgs,
					},
				}
				streamToolCalls = append(streamToolCalls, tcObj)
				emitToolCallDelta(c, flusher, chatCmplID, created, model, callID, fnName, fnArgs)
			} else {
				fullResponseText += buffered
				emitTextDelta(c, flusher, chatCmplID, created, model, buffered)
			}
			pendingPseudoBuf.Reset()
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
		reportAuditLogAsync(chatCmplID, rawAuthHeader, model, realModel, promptTokens, completionTokens, reasoningTokens, cacheReadTokens, latency, http.StatusOK, structuredPrompt, string(respJSONBytes), nil)
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
			if fnName, fnArgs, ok := parsePseudoToolCall(part.Text); ok && len(req.Tools) > 0 {
				callID := "call_" + uuid.New().String()[:8]
				callIDToName.Store(callID, fnName)
				outToolCalls = append(outToolCalls, gin.H{
					"id":   callID,
					"type": "function",
					"function": gin.H{
						"name":      fnName,
						"arguments": fnArgs,
					},
				})
				continue
			}
			outContent += part.Text
		}
		if part.FunctionCall != nil {
			if len(req.Tools) == 0 {
				log.Printf("⚠️ Suppressing unexpected FunctionCall '%s' from Google upstream because client requested NO tools", part.FunctionCall.Name)
				continue
			}
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
			"completion_tokens_details": gin.H{
				"reasoning_tokens": gResp.UsageMetadata.ThoughtsTokenCount,
			},
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
	rTokens := 0
	cacheTokens := 0
	if gResp.UsageMetadata != nil {
		pTokens = gResp.UsageMetadata.PromptTokenCount
		cTokens = gResp.UsageMetadata.CandidatesTokenCount
		rTokens = gResp.UsageMetadata.ThoughtsTokenCount
		if gResp.UsageMetadata.CachedContentTokenCount > 0 {
			cacheTokens = gResp.UsageMetadata.CachedContentTokenCount
		}
	}
	latency := int(time.Since(startTime).Milliseconds())
	respMap := map[string]interface{}{
		"reply":      outContent,
		"tool_calls": outToolCalls,
	}
	respJSONBytes, _ := json.Marshal(respMap)
	reportAuditLogAsync(chatCmplID, rawAuthHeader, model, realModel, pTokens, cTokens, rTokens, cacheTokens, latency, http.StatusOK, structuredPrompt, string(respJSONBytes), nil)
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
	if key == "" {
		key = os.Getenv("GEMINI_API_KEY")
	}
	if token := c.GetHeader("x-goog-api-key"); token != "" && strings.HasPrefix(token, "AIza") {
		key = token
	}
	if key == "" {
		log.Printf("❌ Critical: GEMINI_API_KEY is not configured or empty")
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "gemini-adapter configuration error: GEMINI_API_KEY is empty",
		})
		return
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
