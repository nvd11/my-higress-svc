package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// OpenCodeUsageWindow OpenCode Go 单个额度窗口 (rolling / weekly / monthly)。
type OpenCodeUsageWindow struct {
	Status   string  `json:"status"`
	Percent  float64 `json:"percent"`
	ResetsAt string  `json:"resetsAt"`
}

// OpenCodeUsageResponse OpenCode Go 额度接口响应。
type OpenCodeUsageResponse struct {
	Usage       map[string]OpenCodeUsageWindow `json:"usage"`
	LastUpdated string                         `json:"last_updated"`
}

var (
	openCodeCache     *OpenCodeUsageResponse
	openCodeCacheLock sync.RWMutex
	openCodeCacheTime time.Time
)

const openCodeUsageURL = "https://opencode.ai/inference/go/v1/usage"

// RegisterOpenCodeQuotaRoutes 注册 OpenCode Go 额度接口。
func RegisterOpenCodeQuotaRoutes(rg *gin.RouterGroup) {
	rg.GET("/opencode/quota", func(c *gin.Context) {
		forceRefresh := c.Query("refresh") == "true"

		openCodeCacheLock.RLock()
		if !forceRefresh && openCodeCache != nil && time.Since(openCodeCacheTime) < time.Minute {
			resp := openCodeCache
			openCodeCacheLock.RUnlock()
			c.JSON(http.StatusOK, resp)
			return
		}
		openCodeCacheLock.RUnlock()

		apiKey := os.Getenv("OPENCODE_GO_API_KEY")
		if apiKey == "" {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"error": "OPENCODE_GO_API_KEY not configured",
			})
			return
		}

		ctx, cancel := context.WithTimeout(c.Request.Context(), 12*time.Second)
		defer cancel()

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, openCodeUsageURL, nil)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		req.Header.Set("Authorization", "Bearer "+apiKey)

		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			log.Printf("⚠️ OpenCode usage fetch failed: %v", err)
			openCodeCacheLock.RLock()
			if openCodeCache != nil {
				cached := openCodeCache
				openCodeCacheLock.RUnlock()
				c.JSON(http.StatusOK, cached)
				return
			}
			openCodeCacheLock.RUnlock()
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			msg := fmt.Sprintf("OpenCode usage HTTP %d: %s", resp.StatusCode, string(body))
			log.Printf("⚠️ %s", msg)
			c.JSON(http.StatusBadGateway, gin.H{"error": msg})
			return
		}

		var raw struct {
			Usage map[string]OpenCodeUsageWindow `json:"usage"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}

		out := &OpenCodeUsageResponse{
			Usage:       raw.Usage,
			LastUpdated: time.Now().UTC().Format(time.RFC3339),
		}

		openCodeCacheLock.Lock()
		openCodeCache = out
		openCodeCacheTime = time.Now()
		openCodeCacheLock.Unlock()

		c.JSON(http.StatusOK, out)
	})
}
