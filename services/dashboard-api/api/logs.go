package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"
	"github.com/nvd11/my-higress-svc/services/dashboard-api/db"
	"github.com/nvd11/my-higress-svc/services/dashboard-api/vlogs"
)

var cstZone = time.FixedZone("CST", 8*3600)

// cstDateToUTCBounds 将东八区 (UTC+8) 的日期字符串 YYYY-MM-DD 转换为数据库中存储的 UTC 起止时间字符串
func cstDateToUTCBounds(startDateStr, endDateStr string) (startUTC, endUTC string, ok bool) {
	s, err1 := time.ParseInLocation("2006-01-02", startDateStr, cstZone)
	e, err2 := time.ParseInLocation("2006-01-02", endDateStr, cstZone)
	if err1 != nil || err2 != nil {
		return "", "", false
	}
	startUTC = s.UTC().Format("2006-01-02 15:04:05")
	endUTC = e.Add(24 * time.Hour).UTC().Format("2006-01-02 15:04:05")
	return startUTC, endUTC, true
}

func RegisterLogsRoutes(rg *gin.RouterGroup, vlogsClient *vlogs.Client) {
	// 动态获取筛选下拉框选项 (模型列表与 Key 别名列表)
	rg.GET("/logs/filter-options", func(c *gin.Context) {
		if db.DB == nil {
			c.JSON(http.StatusOK, gin.H{
				"models": []string{
					"gemini-3.8-flash",
					"gemini-3.8-flash-search",
					"kimi-k3",
					"glm-5.3",
					"gpt-5.6-luna-a6",
					"gpt-5.6-luna-yuanheng",
					"yui",
					"rin",
				},
				"key_aliases": []string{"default"},
			})
			return
		}

		var models []string
		modelSql := `
			SELECT model_used 
			FROM llm_request_logs 
			WHERE model_used IS NOT NULL AND model_used != '' AND model_used != 'unknown' AND model_used NOT LIKE '%/%'
			GROUP BY model_used 
			ORDER BY COUNT(*) DESC
		`
		_ = db.DB.SelectContext(c.Request.Context(), &models, modelSql)
		if len(models) == 0 {
			models = []string{
				"gemini-3.8-flash",
				"gemini-3.8-flash-search",
				"kimi-k3",
				"glm-5.3",
				"gpt-5.6-luna-a6",
				"gpt-5.6-luna-yuanheng",
			}
		}

		var keyAliases []string
		keySql := `
			SELECT api_key_alias 
			FROM llm_request_logs 
			WHERE api_key_alias IS NOT NULL AND api_key_alias != '' AND api_key_alias != 'litellm-internal-health-check'
			GROUP BY api_key_alias 
			ORDER BY COUNT(*) DESC
		`
		_ = db.DB.SelectContext(c.Request.Context(), &keyAliases, keySql)
		if len(keyAliases) == 0 {
			keyAliases = []string{"default"}
		}

		c.JSON(http.StatusOK, gin.H{
			"models":      models,
			"key_aliases": keyAliases,
		})
	})

	rg.GET("/logs", func(c *gin.Context) {
		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		if page < 1 {
			page = 1
		}
		pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
		if pageSize < 1 || pageSize > 100 {
			pageSize = 20
		}

		model := c.Query("model_used")
		if model == "" {
			model = c.Query("model")
		}
		statusCodeStr := c.Query("status_code")
		keyAlias := c.Query("api_key_alias")
		if keyAlias == "" {
			keyAlias = c.Query("key_alias")
		}
		searchKeyword := c.Query("search")
		dateStr := c.Query("date")
		startDateStr := c.Query("start_date")
		endDateStr := c.Query("end_date")

		whereClauses := []string{"1=1"}
		args := []interface{}{}

		// 日期过滤条件 (统一按东八区 UTC+8 自然日折算为 UTC 时间区间，并直接命中 idx_logs_created_at 索引)
		if startDateStr != "" && endDateStr != "" {
			if sUTC, eUTC, ok := cstDateToUTCBounds(startDateStr, endDateStr); ok {
				whereClauses = append(whereClauses, "created_at >= ? AND created_at < ?")
				args = append(args, sUTC, eUTC)
			}
		} else if dateStr != "" {
			if sUTC, eUTC, ok := cstDateToUTCBounds(dateStr, dateStr); ok {
				whereClauses = append(whereClauses, "created_at >= ? AND created_at < ?")
				args = append(args, sUTC, eUTC)
			}
		} else if startDateStr != "" {
			if s, err := time.ParseInLocation("2006-01-02", startDateStr, cstZone); err == nil {
				whereClauses = append(whereClauses, "created_at >= ?")
				args = append(args, s.UTC().Format("2006-01-02 15:04:05"))
			}
		} else if endDateStr != "" {
			if e, err := time.ParseInLocation("2006-01-02", endDateStr, cstZone); err == nil {
				whereClauses = append(whereClauses, "created_at < ?")
				args = append(args, e.Add(24*time.Hour).UTC().Format("2006-01-02 15:04:05"))
			}
		}

		// 1. 若携带搜索关键词，先向 VictoriaLogs 检索匹配的 request_id 列表
		if searchKeyword != "" && vlogsClient != nil {
			rids, err := vlogsClient.SearchPayloads(c.Request.Context(), searchKeyword, 200)
			if err == nil && len(rids) > 0 {
				query, inArgs, inErr := sqlx.In("request_id IN (?)", rids)
				if inErr == nil {
					whereClauses = append(whereClauses, query)
					args = append(args, inArgs...)
				}
			} else {
				// 未命中任何包含关键词的报文
				c.JSON(http.StatusOK, gin.H{"total": 0, "page": page, "page_size": pageSize, "items": []interface{}{}})
				return
			}
		}

		if model != "" {
			whereClauses = append(whereClauses, "model_used = ?")
			args = append(args, model)
		}
		if statusCodeStr != "" {
			if sc, err := strconv.Atoi(statusCodeStr); err == nil {
				whereClauses = append(whereClauses, "status_code = ?")
				args = append(args, sc)
			}
		}
		if keyAlias != "" {
			whereClauses = append(whereClauses, "api_key_alias = ?")
			args = append(args, keyAlias)
		}

		whereSql := strings.Join(whereClauses, " AND ")

		if db.DB == nil {
			c.JSON(http.StatusOK, gin.H{"total": 0, "page": page, "page_size": pageSize, "items": []interface{}{}})
			return
		}

		// 统计总数
		var total int
		countSql := fmt.Sprintf("SELECT COUNT(*) FROM llm_request_logs WHERE %s", whereSql)
		if err := db.DB.GetContext(c.Request.Context(), &total, countSql, args...); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		// 分页查询明细
		offset := (page - 1) * pageSize
		selectSql := fmt.Sprintf("SELECT * FROM llm_request_logs WHERE %s ORDER BY created_at DESC LIMIT %d OFFSET %d",
			whereSql, pageSize, offset)

		var items []db.LLMRequestLog
		if err := db.DB.SelectContext(c.Request.Context(), &items, selectSql, args...); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		if items == nil {
			items = []db.LLMRequestLog{}
		}

		c.JSON(http.StatusOK, gin.H{
			"total":     total,
			"page":      page,
			"page_size": pageSize,
			"items":     items,
		})
	})
}
