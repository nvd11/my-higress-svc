package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func setupTestEngine() *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterHealthRoutes(r)
	v1 := r.Group("/api/v1")
	{
		RegisterLogsRoutes(v1, nil)
		RegisterMetricsRoutes(v1)
		RegisterPayloadRoutes(v1, nil)
		RegisterInternalRoutes(v1, nil)
	}
	return r
}

// 1. 验证健康检查探活接口
func TestHealthLiveliness(t *testing.T) {
	r := setupTestEngine()
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/health/liveliness", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed parsing json: %v", err)
	}
	if resp["status"] != "ok" {
		t.Errorf("expected status ok, got %s", resp["status"])
	}
}

// 2. 验证 Internal 审计入库端点的校验与接收能力
func TestInternalAuditLogIngestion(t *testing.T) {
	r := setupTestEngine()

	// 2.1 测试非法 Body (400 Bad Request)
	wBad := httptest.NewRecorder()
	reqBad, _ := http.NewRequest("POST", "/api/v1/internal/audit-log", bytes.NewReader([]byte("{invalid-json")))
	reqBad.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(wBad, reqBad)
	if wBad.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for bad json, got %d", wBad.Code)
	}

	// 2.2 测试合法 Body (200 OK)
	validRecord := WasmAuditRecordInput{
		ID:               "test-audit-id-1",
		RequestID:        "chatcmpl-test-abc",
		APIKeyAlias:      "cindy-test",
		ModelRequested:   "gemini-3.8-flash",
		ModelUsed:        "gemini-3.8-flash",
		Provider:         "google-gemini",
		ProviderKeyAlias: "OPENAI_API_KEY_FREE_3",
		PromptTokens:     100,
		CompletionTokens: 50,
		ReasoningTokens:  20,
		TotalTokens:      170,
		CostUSD:          0.0001,
		CostCNY:          0.000723,
		FxRate:           7.23,
		LatencyMS:        250,
		StatusCode:       200,
		Prompt:           `[{"role":"user","content":"test"}]`,
		Response:         `{"reply":"hello"}`,
	}
	b, _ := json.Marshal(validRecord)

	wGood := httptest.NewRecorder()
	reqGood, _ := http.NewRequest("POST", "/api/v1/internal/audit-log", bytes.NewReader(b))
	reqGood.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(wGood, reqGood)

	if wGood.Code != http.StatusOK {
		t.Fatalf("expected 200 for valid audit log, got %d, body: %s", wGood.Code, wGood.Body.String())
	}

	var resp map[string]string
	_ = json.Unmarshal(wGood.Body.Bytes(), &resp)
	if resp["status"] != "accepted" || resp["request_id"] != "chatcmpl-test-abc" {
		t.Errorf("unexpected internal response: %v", resp)
	}
}

// 3. 验证 Payload 抽屉接口的空降级与结构完整性
func TestPayloadEndpointDefault(t *testing.T) {
	r := setupTestEngine()
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/logs/non-existent-req-id/payload?full=false", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 graceful fallback, got %d", w.Code)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed parsing payload json: %v", err)
	}

	if resp["request_id"] != "non-existent-req-id" {
		t.Errorf("request_id mismatch, got %v", resp["request_id"])
	}
	if resp["prompt"] == nil || resp["response"] == nil {
		t.Errorf("expected non-nil prompt and response objects")
	}
}

// 4. [新增] 验证 GET /api/v1/logs 接口契约
func TestLogsEndpoint(t *testing.T) {
	r := setupTestEngine()
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/logs?page=1&page_size=10", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed parsing json: %v", err)
	}

	if resp["page"].(float64) != 1 || resp["page_size"].(float64) != 10 {
		t.Errorf("pagination param mismatch: %v", resp)
	}
	if resp["items"] == nil {
		t.Errorf("expected non-nil items list")
	}
}

// 5. [新增] 验证 GET /api/v1/metrics/summary 统计卡片与图表数据契约
func TestMetricsEndpoint(t *testing.T) {
	r := setupTestEngine()
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/metrics/summary?time_range=7d", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed parsing json: %v", err)
	}

	if resp["time_range"] != "7d" {
		t.Errorf("expected time_range 7d, got %v", resp["time_range"])
	}
	if resp["cards"] == nil || resp["model_distribution"] == nil || resp["daily_trends"] == nil {
		t.Errorf("missing essential dashboard components in response: %v", resp)
	}
}
