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

		if dbErr != nil || redisErr != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"status": "unhealthy",
				"mysql":  dbErr == nil,
				"redis":  redisErr == nil,
			})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"status": "healthy",
			"mysql":  true,
			"redis":  true,
		})
	})
}
