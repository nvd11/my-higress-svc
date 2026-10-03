package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nvd11/my-higress-svc/services/dashboard-api/db"
	"github.com/nvd11/my-higress-svc/services/dashboard-api/logic"
	"github.com/nvd11/my-higress-svc/services/dashboard-api/redis"
	"github.com/nvd11/my-higress-svc/services/dashboard-api/vlogs"
)

func RegisterPayloadRoutes(rg *gin.RouterGroup, vlogsClient *vlogs.Client) {
	rg.GET("/logs/:request_id/payload", func(c *gin.Context) {
		requestID := c.Param("request_id")
		dateQuery := c.Query("date")
		full, _ := strconv.ParseBool(c.DefaultQuery("full", "false"))

		var promptData map[string]interface{}
		var responseData map[string]interface{}
		dateStr := dateQuery

		// 1. 优先尝试从 Redis L2 缓存获取 (<1ms 极速直出)
		pCached, rCached, found := redis.GetPayloadCache(c.Request.Context(), requestID)
		if found {
			promptData = pCached
			responseData = rCached
			if dateStr == "" {
				dateStr = time.Now().UTC().Format("2006-01-02")
			}
		} else {
			// 2. Redis 未命中: 先到 MySQL 检索该记录创建时间 (作为 VictoriaLogs 日期加速分区)
			if dateStr == "" {
				var createdAt time.Time
				var rowErr error
				if db.DB != nil {
					rowErr = db.DB.GetContext(c.Request.Context(), &createdAt,
						"SELECT created_at FROM llm_request_logs WHERE request_id = ? LIMIT 1", requestID)
				}
				if rowErr == nil && !createdAt.IsZero() {
					dateStr = createdAt.Format("2006-01-02")
				} else {
					dateStr = time.Now().UTC().Format("2006-01-02")
				}
			}

			// 3. 兜底向 VictoriaLogs 检索多分片并动态重组还原
			if vlogsClient != nil {
				pVlogs, rVlogs, err := vlogsClient.ReadPayload(c.Request.Context(), requestID, dateStr)
				if err == nil && (len(pVlogs) > 0 || len(rVlogs) > 0) {
					promptData = pVlogs
					responseData = rVlogs
				}
			}
		}

		if promptData == nil {
			promptData = map[string]interface{}{}
		}
		if responseData == nil {
			responseData = map[string]interface{}{}
		}

		// 4. 安全抽样截断处理: 除非明确指定 full=true, 否则执行递归 Base64 折叠与超长安全截断
		finalPrompt := interface{}(promptData)
		finalResponse := interface{}(responseData)
		if !full {
			finalPrompt, _ = logic.TruncateContentRecursively(promptData)
			finalResponse, _ = logic.TruncateContentRecursively(responseData)
		}

		c.JSON(http.StatusOK, gin.H{
			"request_id":   requestID,
			"date":         dateStr,
			"prompt":       finalPrompt,
			"response":     finalResponse,
			"prompt_url":   "",
			"response_url": "",
		})
	})
}
