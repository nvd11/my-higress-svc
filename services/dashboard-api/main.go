package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/nvd11/my-higress-svc/services/dashboard-api/api"
	"github.com/nvd11/my-higress-svc/services/dashboard-api/config"
	"github.com/nvd11/my-higress-svc/services/dashboard-api/db"
	"github.com/nvd11/my-higress-svc/services/dashboard-api/redis"
	"github.com/nvd11/my-higress-svc/services/dashboard-api/vlogs"
)

func main() {
	cfg := config.LoadConfig()

	// 1. 初始化数据库连接
	if _, err := db.InitDB(cfg); err != nil {
		log.Printf("⚠️ Warning: Initial MySQL connection failed: %v (will retry on requests)", err)
	} else {
		log.Printf("✅ Connected to MySQL HeatWave (%s:%s/%s)", cfg.MySQLHost, cfg.MySQLPort, cfg.MySQLDatabase)
	}

	// 2. 初始化 Redis 连接
	if _, err := redis.InitRedis(cfg); err != nil {
		log.Printf("⚠️ Warning: Initial Redis connection failed: %v", err)
	} else {
		log.Printf("✅ Connected to Redis (%s:%s)", cfg.RedisHost, cfg.RedisPort)
	}

	// 3. 初始化 VictoriaLogs 客户端
	vlogsClient := vlogs.NewClient(cfg)
	log.Printf("✅ VictoriaLogs Client ready (%s)", cfg.VictoriaLogsURL)

	// 4. 创建 Gin Engine
	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(gin.Logger())

	// 5. 跨域 CORS 支持
	r.Use(func(c *gin.Context) {
		c.Writer.Header().Set("Access-Control-Allow-Origin", "*")
		c.Writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS, PUT, DELETE")
		c.Writer.Header().Set("Access-Control-Allow-Headers", "Origin, Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization")
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	})

	// 6. 注册健康检查探针
	api.RegisterHealthRoutes(r)

	// 7. 注册 /api/v1 业务路由
	v1 := r.Group("/api/v1")
	{
		api.RegisterLogsRoutes(v1, vlogsClient)
		api.RegisterMetricsRoutes(v1)
		api.RegisterPayloadRoutes(v1, vlogsClient)
		api.RegisterInternalRoutes(v1, vlogsClient)
	}

	// 8. 静态资源托管与 SPA 前端 Fallback
	staticDir := "static"
	if _, err := os.Stat(staticDir); os.IsNotExist(err) {
		staticDir = "app/static"
	}
	if _, err := os.Stat(staticDir); err == nil {
		r.Static("/assets", filepath.Join(staticDir, "assets"))
		r.NoRoute(func(c *gin.Context) {
			path := c.Request.URL.Path
			if len(path) >= 4 && path[:4] == "/api" {
				c.JSON(http.StatusNotFound, gin.H{"error": "API route not found"})
				return
			}
			c.File(filepath.Join(staticDir, "index.html"))
		})
	}

	// 9. 启动 HTTP 服务与优雅停机
	srv := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: r,
	}

	go func() {
		log.Printf("🚀 Higress Dashboard API (Go Edition) listening on port :%s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("listen error: %s\n", err)
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("🛑 Shutting down server gracefully...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatal("Server forced to shutdown:", err)
	}
	log.Println("👋 Server exited.")
}
