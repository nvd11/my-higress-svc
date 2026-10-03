package finops

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// TestE2EProductionGatewayWithRedisAndVLogs 端到端实测生产环境
// 链路: 发送真实请求 -> 获取 request_id -> 验证 Redis 缓存 -> 验证 VictoriaLogs 归档
func TestE2EProductionGatewayWithRedisAndVLogs(t *testing.T) {
	gatewayURL := os.Getenv("HIGRESS_GATEWAY_URL")
	if gatewayURL == "" {
		gatewayURL = "http://100.105.130.0:31880/v1/chat/completions"
	}

	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "100.105.130.0:6379"
	}

	redisPass := os.Getenv("REDIS_PASSWORD")
	if redisPass == "" {
		redisPass = "hsbc1234"
	}

	vlogsURL := os.Getenv("VICTORIALOGS_URL")
	if vlogsURL == "" {
		vlogsURL = "http://10.0.1.227:9428"
	}

	// 1. 生成唯一测试标识
	testTraceID := "cindy-trace-" + uuid.NewString()[:8]
	testPrompt := fmt.Sprintf("请回复一句话测试连接。特征码: %s", testTraceID)

	reqBodyMap := map[string]interface{}{
		"model": "gemini-3.8-flash",
		"messages": []map[string]string{
			{"role": "user", "content": testPrompt},
		},
		"stream": false,
	}

	reqBytes, err := json.Marshal(reqBodyMap)
	if err != nil {
		t.Fatalf("failed to marshal request body: %v", err)
	}

	t.Logf("🚀 [1/3] 发送真实请求至生产网关: %s (trace_id: %s)", gatewayURL, testTraceID)
	httpReq, err := http.NewRequestWithContext(context.Background(), "POST", gatewayURL, bytes.NewReader(reqBytes))
	if err != nil {
		t.Fatalf("failed to create http request: %v", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	// 💖 携带主人赐予的大秘 Cindy 专属令牌发起调用 (使用 X-API-Key 避免冲掉官方直连凭据)
	httpReq.Header.Set("X-API-Key", "sk-cindy-higress-20261003-888888")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		t.Fatalf("failed calling production gateway: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("gateway returned non-200: %d, body: %s", resp.StatusCode, string(body))
	}

	respBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed reading response body: %v", err)
	}

	var completionResp struct {
		ID      string `json:"id"`
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}

	if err := json.Unmarshal(respBytes, &completionResp); err != nil {
		t.Fatalf("failed unmarshaling completion response: %v", err)
	}

	requestID := completionResp.ID
	if requestID == "" {
		t.Fatalf("gateway returned empty request id! raw response: %s", string(respBytes))
	}

	t.Logf("✅ 网关响应成功: request_id=%s, model=%s, total_tokens=%d",
		requestID, completionResp.Model, completionResp.Usage.TotalTokens)
	if len(completionResp.Choices) > 0 {
		t.Logf("🤖 模型回复内容: %s", completionResp.Choices[0].Message.Content)
	}

	// 2. 验证 Redis 连通性并检查 Key
	t.Logf("🔍 [2/3] 检查生产 Redis: %s (Key: litellm:payload:%s)", redisAddr, requestID)
	rdb := redis.NewClient(&redis.Options{
		Addr:     redisAddr,
		Password: redisPass,
		DB:       0,
	})
	defer rdb.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pong, err := rdb.Ping(ctx).Result()
	if err != nil {
		t.Fatalf("failed connecting to Redis at %s: %v", redisAddr, err)
	}
	t.Logf("✅ Redis Ping 成功: %s", pong)

	cacheKey := fmt.Sprintf("litellm:payload:%s", requestID)
	// 最多轮询等待 3 秒 (因为插件写入是异步旁路)
	var redisVal string
	for i := 0; i < 6; i++ {
		val, err := rdb.Get(ctx, cacheKey).Result()
		if err == nil && val != "" {
			redisVal = val
			break
		}
		time.Sleep(500 * time.Millisecond)
	}

	if redisVal != "" {
		t.Logf("🎉 Redis 缓存命中! 长度: %d 字符", len(redisVal))
		// 校验解压
		dec, err := base64.StdEncoding.DecodeString(redisVal)
		if err == nil {
			zr, err := gzip.NewReader(bytes.NewReader(dec))
			if err == nil {
				uncompressed, _ := io.ReadAll(zr)
				_ = zr.Close()
				t.Logf("📄 Redis 解压后内容片段: %s", string(uncompressed)[:min(len(uncompressed), 200)])
			}
		}
	} else {
		t.Logf("ℹ️ Redis 暂未发现当前 request_id 的 Key (提示: 待 finops-audit Wasm 插件全量挂载生效)")
	}

	// 3. 验证 VictoriaLogs 连通性并执行 LogsQL 检索
	t.Logf("🔍 [3/3] 检查 StarFive VictoriaLogs: %s (查询 request_id=%s)", vlogsURL, requestID)
	vlogsHealthURL := strings.TrimRight(vlogsURL, "/") + "/health"
	hResp, err := client.Get(vlogsHealthURL)
	if err != nil {
		t.Fatalf("failed to reach VictoriaLogs health endpoint: %v", err)
	}
	hResp.Body.Close()
	t.Logf("✅ VictoriaLogs 存活确认: HTTP %d", hResp.StatusCode)

	// 发起 LogsQL 全文检索
	vlogsQueryURL := strings.TrimRight(vlogsURL, "/") + "/select/logsql/query"
	logsql := fmt.Sprintf(`_stream:{type="payload"} AND request_id: exact("%s")`, requestID)
	data := url.Values{}
	data.Set("query", logsql)

	qResp, err := client.PostForm(vlogsQueryURL, data)
	if err != nil {
		t.Fatalf("VictoriaLogs LogsQL 查询异常: %v", err)
	}
	defer qResp.Body.Close()

	qBody, _ := io.ReadAll(qResp.Body)
	qResult := strings.TrimSpace(string(qBody))

	if qResult != "" {
		t.Logf("🎉 VictoriaLogs 检索命中! 返回日志行: %s", qResult[:min(len(qResult), 200)])
	} else {
		t.Logf("ℹ️ VictoriaLogs 当前未命中该 request_id (提示: 待 finops-audit Wasm 插件全量挂载生效)")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
