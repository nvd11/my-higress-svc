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

// A6APIBilling A6 API 渠道账单与余额。
type A6APIBilling struct {
	TotalUsageUSD    float64 `json:"total_usage_usd"`
	TotalUsageCNY    float64 `json:"total_usage_cny"`
	SoftLimitUSD     float64 `json:"soft_limit_usd"`
	HardLimitUSD     float64 `json:"hard_limit_usd"`
	HasPaymentMethod bool    `json:"has_payment_method"`
	Status           string  `json:"status"` // "active", "error", "unconfigured"
	ErrorMsg         string  `json:"error_msg,omitempty"`
	LastUpdated      string  `json:"last_updated"`
}

// A6BillingResponse 第二个 tab 的 A6 账单数据。
type A6BillingResponse struct {
	A6API       *A6APIBilling `json:"a6api,omitempty"`
	LastUpdated string        `json:"last_updated"`
}

var (
	a6BillingCache     *A6BillingResponse
	a6BillingCacheLock sync.RWMutex
	a6BillingCacheTime time.Time
)

// RegisterA6BillingRoutes 注册 A6 渠道账单接口。
func RegisterA6BillingRoutes(rg *gin.RouterGroup) {
	rg.GET("/a6/billing", func(c *gin.Context) {
		forceRefresh := c.Query("refresh") == "true"

		a6BillingCacheLock.RLock()
		if !forceRefresh && a6BillingCache != nil && time.Since(a6BillingCacheTime) < time.Minute {
			resp := a6BillingCache
			a6BillingCacheLock.RUnlock()
			c.JSON(http.StatusOK, resp)
			return
		}
		a6BillingCacheLock.RUnlock()

		ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
		defer cancel()

		billing := fetchA6APIBilling(ctx)
		resp := &A6BillingResponse{
			A6API:       billing,
			LastUpdated: time.Now().UTC().Format(time.RFC3339),
		}
		if billing != nil && billing.Status == "error" {
			log.Printf("⚠️ A6 billing fetch failed: %s", billing.ErrorMsg)
		}

		a6BillingCacheLock.Lock()
		a6BillingCache = resp
		a6BillingCacheTime = time.Now()
		a6BillingCacheLock.Unlock()

		c.JSON(http.StatusOK, resp)
	})
}

// fetchA6APIBilling 查询 A6 API 的订阅限额与累计用量。
func fetchA6APIBilling(ctx context.Context) *A6APIBilling {
	apiKey := os.Getenv("A6_API_KEY")
	if apiKey == "" {
		// 安全红线: 绝不硬编码上游真实 Key。未配置时优雅降级, 面板显示不可用。
		return &A6APIBilling{
			Status:      "unconfigured",
			ErrorMsg:    "A6_API_KEY not configured",
			LastUpdated: time.Now().UTC().Format(time.RFC3339),
		}
	}

	result := &A6APIBilling{
		Status:      "active",
		LastUpdated: time.Now().UTC().Format(time.RFC3339),
	}

	client := &http.Client{Timeout: 6 * time.Second}

	// 1. 获取 subscription / limit
	subReq, err := http.NewRequestWithContext(ctx, "GET", "https://api.a6api.com/v1/dashboard/billing/subscription", nil)
	if err == nil {
		subReq.Header.Set("Authorization", "Bearer "+apiKey)
		if resp, err := client.Do(subReq); err == nil {
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				var sub struct {
					HasPaymentMethod bool    `json:"has_payment_method"`
					SoftLimitUSD     float64 `json:"soft_limit_usd"`
					HardLimitUSD     float64 `json:"hard_limit_usd"`
				}
				if err := json.NewDecoder(resp.Body).Decode(&sub); err == nil {
					result.HasPaymentMethod = sub.HasPaymentMethod
					result.SoftLimitUSD = sub.SoftLimitUSD
					result.HardLimitUSD = sub.HardLimitUSD
				}
			}
		}
	}

	// 2. 获取 usage
	usageReq, err := http.NewRequestWithContext(ctx, "GET", "https://api.a6api.com/v1/dashboard/billing/usage", nil)
	if err != nil {
		result.Status = "error"
		result.ErrorMsg = err.Error()
		return result
	}
	usageReq.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := client.Do(usageReq)
	if err != nil {
		result.Status = "error"
		result.ErrorMsg = err.Error()
		return result
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		result.Status = "error"
		result.ErrorMsg = fmt.Sprintf("A6 API HTTP %d: %s", resp.StatusCode, string(b))
		return result
	}

	var usageResp struct {
		TotalUsage float64 `json:"total_usage"` // 单位: 美分 / points
	}
	if err := json.NewDecoder(resp.Body).Decode(&usageResp); err != nil {
		result.Status = "error"
		result.ErrorMsg = err.Error()
		return result
	}

	// total_usage 是美分或点数 (例如 727.07 = $7.2707)
	result.TotalUsageUSD = usageResp.TotalUsage / 100.0
	result.TotalUsageCNY = result.TotalUsageUSD * 7.23 // 默认对齐 7.23 汇率

	return result
}
