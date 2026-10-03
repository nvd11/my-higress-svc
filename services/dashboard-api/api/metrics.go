package api

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nvd11/my-higress-svc/services/dashboard-api/db"
)

type SummaryCards struct {
	TotalRequests    int     `json:"total_requests"`
	SuccessRate      float64 `json:"success_rate"`
	TotalTokens      int     `json:"total_tokens"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	ReasoningTokens  int     `json:"reasoning_tokens"`
	TotalCostUSD     float64 `json:"total_cost_usd"`
	TotalCostCNY     float64 `json:"total_cost_cny"`
	AvgLatencyMS     float64 `json:"avg_latency_ms"`
}

type ModelSpendDistribution struct {
	Model      string  `db:"model_used" json:"model"`
	SpendUSD   float64 `db:"spend_usd" json:"spend_usd"`
	SpendCNY   float64 `db:"spend_cny" json:"spend_cny"`
	CallCount  int     `db:"call_count" json:"call_count"`
	Percentage float64 `json:"percentage"`
}

type DailySpendTrend struct {
	Date     string  `db:"date_str" json:"date"`
	SpendUSD float64 `db:"spend_usd" json:"spend_usd"`
	SpendCNY float64 `db:"spend_cny" json:"spend_cny"`
	Requests int     `db:"requests" json:"requests"`
}

func RegisterMetricsRoutes(rg *gin.RouterGroup) {
	rg.GET("/metrics/summary", func(c *gin.Context) {
		timeRange := c.DefaultQuery("time_range", "24h")
		var duration time.Duration
		switch timeRange {
		case "7d":
			duration = 7 * 24 * time.Hour
		case "30d":
			duration = 30 * 24 * time.Hour
		default:
			duration = 24 * time.Hour
		}

		startTime := time.Now().Add(-duration)

		// 1. 聚合四大核心指标卡片
		var cards SummaryCards
		summarySql := `
			SELECT 
				COUNT(*) as total_requests,
				COALESCE(SUM(CASE WHEN status_code = 200 THEN 1 ELSE 0 END), 0) * 100.0 / NULLIF(COUNT(*), 0) as success_rate,
				COALESCE(SUM(total_tokens), 0) as total_tokens,
				COALESCE(SUM(prompt_tokens), 0) as prompt_tokens,
				COALESCE(SUM(completion_tokens), 0) as completion_tokens,
				COALESCE(SUM(reasoning_tokens), 0) as reasoning_tokens,
				COALESCE(SUM(cost_usd), 0.0) as total_cost_usd,
				COALESCE(SUM(cost_cny), 0.0) as total_cost_cny,
				COALESCE(AVG(latency_ms), 0.0) as avg_latency_ms
			FROM llm_request_logs 
			WHERE created_at >= ?
		`
		type RawSummary struct {
			TotalRequests    int      `db:"total_requests"`
			SuccessRate      *float64 `db:"success_rate"`
			TotalTokens      int      `db:"total_tokens"`
			PromptTokens     int      `db:"prompt_tokens"`
			CompletionTokens int      `db:"completion_tokens"`
			ReasoningTokens  int      `db:"reasoning_tokens"`
			TotalCostUSD     float64  `db:"total_cost_usd"`
			TotalCostCNY     float64  `db:"total_cost_cny"`
			AvgLatencyMS     float64  `db:"avg_latency_ms"`
		}
		var raw RawSummary
		if err := db.DB.GetContext(c.Request.Context(), &raw, summarySql, startTime); err == nil {
			cards.TotalRequests = raw.TotalRequests
			if raw.SuccessRate != nil {
				cards.SuccessRate = *raw.SuccessRate
			}
			cards.TotalTokens = raw.TotalTokens
			cards.PromptTokens = raw.PromptTokens
			cards.CompletionTokens = raw.CompletionTokens
			cards.ReasoningTokens = raw.ReasoningTokens
			cards.TotalCostUSD = raw.TotalCostUSD
			cards.TotalCostCNY = raw.TotalCostCNY
			cards.AvgLatencyMS = raw.AvgLatencyMS
		}

		// 2. 聚合模型费用环形图
		modelSql := `
			SELECT 
				model_used,
				COALESCE(SUM(cost_usd), 0.0) as spend_usd,
				COALESCE(SUM(cost_cny), 0.0) as spend_cny,
				COUNT(*) as call_count
			FROM llm_request_logs
			WHERE created_at >= ?
			GROUP BY model_used
			ORDER BY spend_usd DESC
		`
		var modelDist []ModelSpendDistribution
		_ = db.DB.SelectContext(c.Request.Context(), &modelDist, modelSql, startTime)
		if modelDist == nil {
			modelDist = []ModelSpendDistribution{}
		}
		for i := range modelDist {
			if cards.TotalCostUSD > 0 {
				modelDist[i].Percentage = (modelDist[i].SpendUSD / cards.TotalCostUSD) * 100.0
			}
		}

		// 3. 聚合每日花费趋势折线图
		dailySql := `
			SELECT 
				DATE_FORMAT(created_at, '%Y-%m-%d') as date_str,
				COALESCE(SUM(cost_usd), 0.0) as spend_usd,
				COALESCE(SUM(cost_cny), 0.0) as spend_cny,
				COUNT(*) as requests
			FROM llm_request_logs
			WHERE created_at >= ?
			GROUP BY DATE_FORMAT(created_at, '%Y-%m-%d')
			ORDER BY date_str ASC
		`
		var dailyTrends []DailySpendTrend
		_ = db.DB.SelectContext(c.Request.Context(), &dailyTrends, dailySql, startTime)
		if dailyTrends == nil {
			dailyTrends = []DailySpendTrend{}
		}

		c.JSON(http.StatusOK, gin.H{
			"time_range":          timeRange,
			"cards":               cards,
			"model_distribution":  modelDist,
			"daily_trends":        dailyTrends,
		})
	})
}
