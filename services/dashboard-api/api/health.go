package api

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nvd11/my-higress-svc/services/dashboard-api/db"
	"github.com/nvd11/my-higress-svc/services/dashboard-api/redis"
)

func RegisterHealthRoutes(r *gin.Engine) {
	r.GET("/health/liveliness", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})

	r.GET("/health/readiness", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
		defer cancel()

		dbErr := db.CheckHealth(ctx)
		redisErr := redis.CheckHealth(ctx)

		// 核心真理: MySQL 为主存储 (核心强依赖), Redis 仅作为 L2 辅助缓存 (非阻塞降级)
		if dbErr != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"status": "unhealthy",
				"mysql":  false,
				"redis":  redisErr == nil,
			})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"status": "healthy",
			"mysql":  true,
			"redis":  redisErr == nil,
		})
	})
}
