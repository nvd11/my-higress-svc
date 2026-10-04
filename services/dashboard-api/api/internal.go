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

		log.Printf("📥 Ingesting internal audit-log: req_id=%s, key=%s, model=%s", input.RequestID, input.APIKeyAlias, input.ModelRequested)

		// 同步执行三位一体落地
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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
				input.ID, input.RequestID, input.APIKeyAlias, input.ModelRequested, input.ModelUsed,
				input.Provider, input.ProviderKeyAlias, input.PromptTokens, input.CompletionTokens,
				input.TotalTokens, input.CostUSD, input.CostCNY, input.FxRate,
				input.LatencyMS, input.StatusCode, input.ErrorMsg, input.CreatedAt,
			)
			if err != nil {
				log.Printf("❌ Failed executing insertSql in MySQL: %v", err)
			} else {
				affected, _ := res.RowsAffected()
				log.Printf("✅ Inserted audit record %s into MySQL (rows affected: %d)", input.RequestID, affected)
			}
		} else {
			log.Printf("⚠️ Warning: db.DB is nil when attempting to insert audit log!")
		}

		// 解析 Prompt 和 Response 对象
		var pObj map[string]interface{}
		var rObj map[string]interface{}
		if err := json.Unmarshal([]byte(input.Prompt), &pObj); err != nil {
			pObj = map[string]interface{}{"raw_content": input.Prompt}
		}
		if err := json.Unmarshal([]byte(input.Response), &rObj); err != nil {
			rObj = map[string]interface{}{"reply": input.Response}
		}

		// 2. 写入 Redis (3天热缓存)
		_ = redis.SetPayloadCache(ctx, input.RequestID, pObj, rObj)

		// 3. 写入 VictoriaLogs (自动分片 + Gzip)
		if vlogsClient != nil {
			meta := map[string]interface{}{
				"timestamp":         input.CreatedAt.UTC().Format(time.RFC3339Nano),
				"model":             input.ModelUsed,
				"key_alias":         input.APIKeyAlias,
				"status_code":       input.StatusCode,
				"latency_ms":        input.LatencyMS,
				"prompt_tokens":     input.PromptTokens,
				"completion_tokens": input.CompletionTokens,
				"total_tokens":      input.TotalTokens,
				"spend":             input.CostUSD,
			}
			_ = vlogsClient.WritePayload(ctx, input.RequestID, pObj, rObj, meta)
		}

		c.JSON(http.StatusOK, gin.H{"status": "accepted", "request_id": input.RequestID})
	})
}
