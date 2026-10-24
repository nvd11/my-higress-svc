package api

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nvd11/my-higress-svc/services/dashboard-api/db"
	"github.com/nvd11/my-higress-svc/services/dashboard-api/redis"
	"github.com/nvd11/my-higress-svc/services/dashboard-api/vlogs"
)

type WasmAuditRecordInput struct {
	RequestID              string    `json:"request_id"`
	APIKeyAlias            string    `json:"api_key_alias"`
	ModelRequested         string    `json:"model_requested"`
	ModelUsed              string    `json:"model_used"`
	Provider               string    `json:"provider"`
	ProviderKeyAlias       string    `json:"provider_key_alias"`
	PromptTokens           int       `json:"prompt_tokens"`
	CompletionTokens       int       `json:"completion_tokens"`
	ReasoningTokens        int       `json:"reasoning_tokens"`
	CacheReadTokens        int       `json:"cache_read_tokens"`
	TotalTokens            int       `json:"total_tokens"`
	PromptUnitPriceUSD     float64   `json:"prompt_unit_price_usd"`
	CacheUnitPriceUSD      float64   `json:"cache_unit_price_usd"`
	CompletionUnitPriceUSD float64   `json:"completion_unit_price_usd"`
	CostUSD                float64   `json:"cost_usd"`
	CostCNY                float64   `json:"cost_cny"`
	FxRate                 float64   `json:"fx_rate"`
	LatencyMS              int       `json:"latency_ms"`
	StatusCode             int       `json:"status_code"`
	ErrorMsg               *string   `json:"error_msg"`
	CreatedAt              time.Time `json:"created_at"`
	Prompt                 string    `json:"prompt"`
	Response               string    `json:"response"`
}

const (
	// persistTimeout 单条记录落盘总预算 (MySQL + Redis + VictoriaLogs)
	persistTimeout = 30 * time.Second

	// vlogsAttempts VictoriaLogs 写入重试次数
	vlogsAttempts = 3

	// vlogsRetryBackoff 重试间隔基数
	vlogsRetryBackoff = 300 * time.Millisecond
)

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

		// ① 同步预写 WAL: 必须在回 200 之前完成。
		//    此后即使进程立刻被杀死, 记录也已经落在 Redis 里,
		//    由 replay 协程在重启后幂等重放, 不会再出现"空 payload"。
		if err := writeWAL(c.Request.Context(), input); err != nil {
			log.Printf("⚠️ WAL prewrite failed for %s (continuing): %v", input.RequestID, err)
		}

		// ② 立即响应调用方, 毫秒级返回
		c.JSON(http.StatusOK, gin.H{"status": "accepted", "request_id": input.RequestID})

		// ③ 异步落盘, 三处全部成功才销账
		go func(rec WasmAuditRecordInput) {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("❌ Panic in internal audit-log async worker (WAL kept): %v", r)
				}
			}()

			ctx, cancel := context.WithTimeout(context.Background(), persistTimeout)
			defer cancel()

			if err := PersistRecord(ctx, rec, vlogsClient); err != nil {
				// 保留 WAL, 交给 replay 协程重试
				log.Printf("❌ Persist failed for %s (WAL kept for replay): %v", rec.RequestID, err)
				return
			}
			if err := redis.DelWAL(ctx, rec.RequestID); err != nil {
				log.Printf("⚠️ WAL ack failed for %s: %v", rec.RequestID, err)
			}
		}(input)
	})
}

// writeWAL 序列化并同步预写一条在途审计记录
func writeWAL(ctx context.Context, rec WasmAuditRecordInput) error {
	walCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	payload, err := json.Marshal(walEnvelope{Record: rec})
	if err != nil {
		return err
	}
	return redis.SetWAL(walCtx, rec.RequestID, payload)
}

// PersistRecord 幂等落盘: MySQL + Redis 热缓存 + VictoriaLogs。
// 实时路径与 WAL 重放路径共用本函数, 因此每一步都必须可重复执行。
// 返回 error 表示"数据尚未落全", 调用方需保留 WAL 以便重放。
func PersistRecord(ctx context.Context, rec WasmAuditRecordInput, vlogsClient *vlogs.Client) error {
	var failures []string

	// ---------- 1. MySQL 账本 (幂等: 主键冲突时原地覆盖) ----------
	if db.DB != nil {
		if err := insertAuditLog(ctx, rec); err != nil {
			failures = append(failures, fmt.Sprintf("mysql: %v", err))
		} else {
			log.Printf("✅ Inserted audit record %s into MySQL (key: %s)", rec.RequestID, rec.APIKeyAlias)
		}
	} else {
		failures = append(failures, "mysql: client not initialized")
	}

	// ---------- 2. Redis 热缓存 (失败不阻断: 冷归档仍有全量报文) ----------
	var pObj, rObj map[string]interface{}
	if err := json.Unmarshal([]byte(rec.Prompt), &pObj); err != nil {
		pObj = map[string]interface{}{"raw_content": rec.Prompt}
	}
	if err := json.Unmarshal([]byte(rec.Response), &rObj); err != nil {
		rObj = map[string]interface{}{"reply": rec.Response}
	}
	if err := redis.SetPayloadCache(ctx, rec.RequestID, pObj, rObj); err != nil {
		log.Printf("⚠️ Payload cache failed for %s (non-fatal): %v", rec.RequestID, err)
	}

	// ---------- 3. VictoriaLogs 冷归档 (失败需重试) ----------
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
		if err := writeVLogsWithRetry(ctx, vlogsClient, rec.RequestID, pObj, rObj, meta); err != nil {
			failures = append(failures, fmt.Sprintf("victorialogs: %v", err))
		}
	}

	if len(failures) > 0 {
		return fmt.Errorf("%s", strings.Join(failures, "; "))
	}
	return nil
}

// auditRowID 由 (request_id, created_at) 派生主键, 不再使用插件上报的伪随机 ID。
//
// 背景: finops-audit 的 id.go 把 XorShift 种子写成常量 0xda942042e4dd58b5,
// 每次 Wasm 模块重新实例化都会从 #0 重放同一串 UUID, 撞上历史行直接
// Error 1062 (Duplicate entry ... for key 'llm_request_logs.PRIMARY'), 导致静默丢单。
//
// 这里改用确定性哈希做幂等键:
//   - 同一份上报重复投递/WAL 重放 → 算出同一个 id → ON DUPLICATE KEY 幂等
//   - 同一 request_id 的不同次上报 → createdAt 参与哈希 → 不会互相覆盖
//   - 老数据的伪随机 id 与本哈希空间不重叠, 历史行完全不受影响
func auditRowID(rec WasmAuditRecordInput) string {
	seed := rec.RequestID + "|" + rec.CreatedAt.UTC().Format(time.RFC3339Nano)
	sum := md5.Sum([]byte(seed))
	return hex.EncodeToString(sum[:])
}

// insertAuditLog 幂等写入 MySQL (INSERT ... ON DUPLICATE KEY 原地覆盖, 支持 WAL 重放)
func insertAuditLog(ctx context.Context, rec WasmAuditRecordInput) error {
	dbID := auditRowID(rec)

	insertSql := `
					INSERT INTO llm_request_logs (
						id, request_id, api_key_alias, model_requested, model_used,
						provider, provider_key_alias, prompt_tokens, completion_tokens,
						reasoning_tokens, cache_read_tokens, total_tokens,
						prompt_unit_price_usd, cache_unit_price_usd, completion_unit_price_usd,
						cost_usd, cost_cny, fx_rate,
						latency_ms, status_code, error_msg, created_at
					) VALUES (
						?, ?, ?, ?, ?,
						?, ?, ?, ?,
						?, ?, ?,
						?, ?, ?,
						?, ?, ?,
						?, ?, ?, ?
					)
					ON DUPLICATE KEY UPDATE
						request_id = request_id
				`
	_, err := db.DB.ExecContext(ctx, insertSql,
		dbID, rec.RequestID, rec.APIKeyAlias, rec.ModelRequested, rec.ModelUsed,
		rec.Provider, rec.ProviderKeyAlias, rec.PromptTokens, rec.CompletionTokens,
		rec.ReasoningTokens, rec.CacheReadTokens, rec.TotalTokens,
		rec.PromptUnitPriceUSD, rec.CacheUnitPriceUSD, rec.CompletionUnitPriceUSD,
		rec.CostUSD, rec.CostCNY, rec.FxRate,
		rec.LatencyMS, rec.StatusCode, rec.ErrorMsg, rec.CreatedAt,
	)
	return err
}

// writeVLogsWithRetry VictoriaLogs 写入带有限重试, 失败不再静默吞掉
func writeVLogsWithRetry(ctx context.Context, client *vlogs.Client, requestID string, pObj, rObj, meta map[string]interface{}) error {
	var lastErr error
	for attempt := 1; attempt <= vlogsAttempts; attempt++ {
		if err := client.WritePayload(ctx, requestID, pObj, rObj, meta); err != nil {
			lastErr = err
			if attempt < vlogsAttempts {
				select {
				case <-ctx.Done():
					return fmt.Errorf("aborted after %d attempt(s): %w", attempt, lastErr)
				case <-time.After(time.Duration(attempt) * vlogsRetryBackoff):
				}
			}
			continue
		}
		return nil
	}
	return lastErr
}
