package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nvd11/my-higress-svc/services/dashboard-api/db"
)

type ActiveKeyMetric struct {
	Alias   string  `db:"api_key_alias" json:"alias"`
	Count   int     `db:"cnt" json:"count"`
	Tokens  int     `db:"tokens" json:"tokens"`
	CostCNY float64 `db:"cost_cny" json:"cost_cny"`
}

type ModelBreakdownMetric struct {
	Model   string  `db:"model_used" json:"model"`
	Count   int     `db:"cnt" json:"count"`
	Tokens  int     `db:"tokens" json:"tokens"`
	CostCNY float64 `db:"cost_cny" json:"cost_cny"`
}

type SummaryMetricsResponse struct {
	Date            string                 `json:"date"`
	TodayRequests   int                    `json:"today_requests"`
	TodayTokens     int                    `json:"today_tokens"`
	TodayCostCNY    float64                `json:"today_cost_cny"`
	TodayCostUSD    float64                `json:"today_cost_usd"`
	AvgLatencyMS    int                    `json:"avg_latency_ms"`
	SuccessRate     float64                `json:"success_rate"`
	ActiveKeys      []ActiveKeyMetric      `json:"active_keys"`
	ModelsBreakdown []ModelBreakdownMetric `json:"models_breakdown"`
}

func RegisterMetricsRoutes(rg *gin.RouterGroup) {
	rg.GET("/metrics/summary", func(c *gin.Context) {
		dateStr := c.Query("date")
		if dateStr == "" {
			dateStr = time.Now().Format("2006-01-02")
		}

		apiKeyAlias := c.Query("api_key_alias")
		modelUsed := c.Query("model_used")
		statusCodeStr := c.Query("status_code")

		whereClauses := []string{"DATE(created_at) = ?"}
		args := []interface{}{dateStr}

		if apiKeyAlias != "" {
			whereClauses = append(whereClauses, "api_key_alias = ?")
			args = append(args, apiKeyAlias)
		}
		if modelUsed != "" {
			whereClauses = append(whereClauses, "model_used = ?")
			args = append(args, modelUsed)
		}
		if statusCodeStr != "" {
			if sc, err := strconv.Atoi(statusCodeStr); err == nil {
				whereClauses = append(whereClauses, "status_code = ?")
				args = append(args, sc)
			}
		}

		whereSql := strings.Join(whereClauses, " AND ")

		resp := SummaryMetricsResponse{
			Date:            dateStr,
			ActiveKeys:      []ActiveKeyMetric{},
			ModelsBreakdown: []ModelBreakdownMetric{},
		}

		if db.DB == nil {
			c.JSON(http.StatusOK, resp)
			return
		}

		// 1. 聚合当日总体大盘卡片数据
		type SummaryAgg struct {
			TotalRequests int      `db:"total_requests"`
			SuccessRate   *float64 `db:"success_rate"`
			TotalTokens   int      `db:"total_tokens"`
			TotalCostCNY  float64  `db:"total_cost_cny"`
			TotalCostUSD  float64  `db:"total_cost_usd"`
			AvgLatencyMS  float64  `db:"avg_latency_ms"`
		}

		summarySql := fmt.Sprintf(`
			SELECT 
				COUNT(*) as total_requests,
				COALESCE(SUM(CASE WHEN status_code = 200 THEN 1 ELSE 0 END), 0) * 100.0 / NULLIF(COUNT(*), 0) as success_rate,
				COALESCE(SUM(total_tokens), 0) as total_tokens,
				COALESCE(SUM(cost_cny), 0.0) as total_cost_cny,
				COALESCE(SUM(cost_usd), 0.0) as total_cost_usd,
				COALESCE(AVG(latency_ms), 0.0) as avg_latency_ms
			FROM llm_request_logs 
			WHERE %s
		`, whereSql)

		var agg SummaryAgg
		if err := db.DB.GetContext(c.Request.Context(), &agg, summarySql, args...); err == nil {
			resp.TodayRequests = agg.TotalRequests
			if agg.SuccessRate != nil {
				resp.SuccessRate = *agg.SuccessRate
			}
			resp.TodayTokens = agg.TotalTokens
			resp.TodayCostCNY = agg.TotalCostCNY
			resp.TodayCostUSD = agg.TotalCostUSD
			resp.AvgLatencyMS = int(agg.AvgLatencyMS)
		}

		// 2. 聚合 Active Keys
		keysSql := fmt.Sprintf(`
			SELECT 
				api_key_alias,
				COUNT(*) as cnt,
				COALESCE(SUM(total_tokens), 0) as tokens,
				COALESCE(SUM(cost_cny), 0.0) as cost_cny
			FROM llm_request_logs
			WHERE %s
			GROUP BY api_key_alias
			ORDER BY cost_cny DESC
		`, whereSql)
		var keys []ActiveKeyMetric
		_ = db.DB.SelectContext(c.Request.Context(), &keys, keysSql, args...)
		if keys != nil {
			resp.ActiveKeys = keys
		}

		// 3. 聚合 Models Breakdown
		modelsSql := fmt.Sprintf(`
			SELECT 
				model_used,
				COUNT(*) as cnt,
				COALESCE(SUM(total_tokens), 0) as tokens,
				COALESCE(SUM(cost_cny), 0.0) as cost_cny
			FROM llm_request_logs
			WHERE %s
			GROUP BY model_used
			ORDER BY cost_cny DESC
		`, whereSql)
		var models []ModelBreakdownMetric
		_ = db.DB.SelectContext(c.Request.Context(), &models, modelsSql, args...)
		if models != nil {
			resp.ModelsBreakdown = models
		}

		c.JSON(http.StatusOK, resp)
	})
}
