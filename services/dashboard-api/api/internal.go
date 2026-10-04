package api

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nvd11/my-higress-svc/services/dashboard-api/db"
	"github.com/nvd11/my-higress-svc/services/dashboard-api/redis"
	"github.com/nvd11/my-higress-svc/services/dashboard-api/vlogs"
)

type WasmAuditRecordInput struct {
	ID               string    `json:"id"`
	RequestID        string    `json:"request_id"`
	APIKeyAlias      string    `json:"api_key_alias"`
	ModelRequested   string    `json:"model_requested"`
	ModelUsed        string    `json:"model_used"`
	Provider         string    `json:"provider"`
	ProviderKeyAlias string    `json:"provider_key_alias"`
	PromptTokens     int       `json:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens"`
	ReasoningTokens  int       `json:"reasoning_tokens"`
	TotalTokens      int       `json:"total_tokens"`
	CostUSD          float64   `json:"cost_usd"`
	CostCNY          float64   `json:"cost_cny"`
	FxRate           float64   `json:"fx_rate"`
	LatencyMS        int       `json:"latency_ms"`
	StatusCode       int       `json:"status_code"`
	ErrorMsg         *string   `json:"error_msg"`
	CreatedAt        time.Time `json:"created_at"`
	Prompt           string    `json:"prompt"`
	Response         string    `json:"response"`
}

func RegisterInternalRoutes(rg *gin.RouterGroup, vlogsClient *vlogs.Client) {
	rg.POST("/internal/audit-log", func(c *gin.Context) {
		var input WasmAuditRecordInput
		if err := c.ShouldBindJSON(&input); err != nil {
			log.Printf("❌ Failed binding audit-log JSON: %v", err)
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		if input.CreatedAt.IsZero() {
			input.CreatedAt = time.Now()
		}

		// 立即响应调用方，毫秒级返回
		c.JSON(http.StatusOK, gin.H{"status": "accepted", "request_id": input.RequestID})

		// 异步并发执行三位一体落地，绝不阻塞客户端
		go func(rec WasmAuditRecordInput) {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("❌ Panic in internal audit-log async worker: %v", r)
				}
			}()

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			// 1. 写入 MySQL HeatWave (llm_request_logs)
			if db.DB != nil {
				insertSql := `
					INSERT INTO llm_request_logs (
						id, request_id, api_key_alias, model_requested, model_used,
						provider, provider_key_alias, prompt_tokens, completion_tokens,
						total_tokens, cost_usd, cost_cny, fx_rate,
						latency_ms, status_code, error_msg, created_at
					) VALUES (
						?, ?, ?, ?, ?,
						?, ?, ?, ?,
						?, ?, ?, ?,
						?, ?, ?, ?
					)
				`
				res, err := db.DB.ExecContext(ctx, insertSql,
					rec.ID, rec.RequestID, rec.APIKeyAlias, rec.ModelRequested, rec.ModelUsed,
					rec.Provider, rec.ProviderKeyAlias, rec.PromptTokens, rec.CompletionTokens,
					rec.TotalTokens, rec.CostUSD, rec.CostCNY, rec.FxRate,
					rec.LatencyMS, rec.StatusCode, rec.ErrorMsg, rec.CreatedAt,
				)
				if err != nil {
					log.Printf("❌ Failed executing insertSql in MySQL: %v", err)
				} else {
					affected, _ := res.RowsAffected()
					log.Printf("✅ Inserted audit record %s into MySQL (rows affected: %d, key: %s)", rec.RequestID, affected, rec.APIKeyAlias)
				}
			}

			// 解析 Prompt 和 Response 对象
			var pObj map[string]interface{}
			var rObj map[string]interface{}
			if err := json.Unmarshal([]byte(rec.Prompt), &pObj); err != nil {
				pObj = map[string]interface{}{"raw_content": rec.Prompt}
			}
			if err := json.Unmarshal([]byte(rec.Response), &rObj); err != nil {
				rObj = map[string]interface{}{"reply": rec.Response}
			}

			// 2. 写入 Redis (3天热缓存)
			_ = redis.SetPayloadCache(ctx, rec.RequestID, pObj, rObj)

			// 3. 写入 VictoriaLogs (自动分片 + Gzip)
			if vlogsClient != nil {
				meta := map[string]interface{}{
					"timestamp":         rec.CreatedAt.UTC().Format(time.RFC3339Nano),
					"model":             rec.ModelUsed,
					"key_alias":         rec.APIKeyAlias,
					"status_code":       rec.StatusCode,
					"latency_ms":        rec.LatencyMS,
					"prompt_tokens":     rec.PromptTokens,
					"completion_tokens": rec.CompletionTokens,
					"total_tokens":      rec.TotalTokens,
					"spend":             rec.CostUSD,
				}
				_ = vlogsClient.WritePayload(ctx, rec.RequestID, pObj, rObj, meta)
			}
		}(input)
	})
}
