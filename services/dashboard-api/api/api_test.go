package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"
	"github.com/nvd11/my-higress-svc/services/dashboard-api/config"
	"github.com/nvd11/my-higress-svc/services/dashboard-api/db"
	"github.com/nvd11/my-higress-svc/services/dashboard-api/vlogs"
)

func setupTestEngineWithVLogs(vlogsClient *vlogs.Client) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	RegisterHealthRoutes(r)
	v1 := r.Group("/api/v1")
	{
		RegisterLogsRoutes(v1, vlogsClient)
		RegisterMetricsRoutes(v1)
		RegisterPayloadRoutes(v1, vlogsClient)
		RegisterInternalRoutes(v1, vlogsClient)
	}
	return r
}

// 1. 验证健康检查探活接口
func TestHealthLiveliness(t *testing.T) {
	r := setupTestEngineWithVLogs(nil)
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

// 2. 验证就绪探针在无外部连接时的优雅降级 (503 Service Unavailable)
func TestHealthReadinessUnhealthy(t *testing.T) {
	r := setupTestEngineWithVLogs(nil)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/health/readiness", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when services uninitialized, got: %d", w.Code)
	}
}

// 3. 验证 Internal 审计入库端点的校验与接收能力
func TestInternalAuditLogIngestion(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed opening sqlmock: %v", err)
	}
	defer mockDB.Close()
	db.DB = sqlx.NewDb(mockDB, "sqlmock")
	defer func() { db.DB = nil }()

	mock.ExpectExec("INSERT INTO llm_request_logs").
		WillReturnResult(sqlmock.NewResult(1, 1))

	r := setupTestEngineWithVLogs(nil)

	// 3.1 测试非法 Body (400 Bad Request)
	wBad := httptest.NewRecorder()
	reqBad, _ := http.NewRequest("POST", "/api/v1/internal/audit-log", bytes.NewReader([]byte("{invalid-json")))
	reqBad.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(wBad, reqBad)
	if wBad.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for bad json, got %d", wBad.Code)
	}

	// 3.2 测试合法 Body (200 OK)
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

	time.Sleep(20 * time.Millisecond)
}

// 4. 验证 Payload 抽屉接口的无缓存降级与 full=true 全量返回分支
func TestPayloadEndpointBranches(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{\"shard_index\":1,\"prompt\":\"[test prompt]\",\"response\":\"[test reply]\"}\n"))
	}))
	defer ts.Close()

	mockVLogs := vlogs.NewClient(&config.Config{VictoriaLogsURL: ts.URL})
	r := setupTestEngineWithVLogs(mockVLogs)

	// 4.1 测试 full=false (触发截断逻辑分支)
	w1 := httptest.NewRecorder()
	req1, _ := http.NewRequest("GET", "/api/v1/logs/req-123/payload?full=false&date=2026-10-03", nil)
	r.ServeHTTP(w1, req1)
	if w1.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w1.Code)
	}

	// 4.2 测试 full=true (跳过截断逻辑分支)
	w2 := httptest.NewRecorder()
	req2, _ := http.NewRequest("GET", "/api/v1/logs/req-123/payload?full=true", nil)
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w2.Code)
	}
}

// 5. 验证 GET /api/v1/logs 的多条件分支过滤覆盖并真实执行 Mock SQL
func TestLogsEndpointParameters(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed opening sqlmock: %v", err)
	}
	defer mockDB.Close()
	db.DB = sqlx.NewDb(mockDB, "sqlmock")
	defer func() { db.DB = nil }()

	// Mock COUNT
	mock.ExpectQuery("SELECT COUNT").
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	// Mock SELECT
	mock.ExpectQuery("SELECT .* FROM llm_request_logs").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "request_id", "api_key_alias", "model_requested", "model_used",
			"provider", "provider_key_alias", "prompt_tokens", "completion_tokens",
			"reasoning_tokens", "total_tokens", "cost_usd", "cost_cny", "fx_rate",
			"latency_ms", "status_code", "error_msg", "created_at",
		}).AddRow(
			"uuid-1", "req-1", "cindy", "gemini-3.8-flash", "gemini-3.8-flash",
			"google-gemini", "KEY3", 10, 20, 5, 35, 0.001, 0.007, 7.23, 200, 200, nil, time.Now(),
		))

	r := setupTestEngineWithVLogs(nil)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/logs?model=gemini-3.8-flash&status_code=200&key_alias=cindy&page=1&page_size=20", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if resp["total"].(float64) != 1 {
		t.Errorf("expected total 1, got: %v", resp["total"])
	}
}

// 6. 验证 GET /api/v1/metrics/summary 的真实 Mock SQL 聚合逻辑
func TestMetricsEndpointWithMockDB(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("failed opening sqlmock: %v", err)
	}
	defer mockDB.Close()
	db.DB = sqlx.NewDb(mockDB, "sqlmock")
	defer func() { db.DB = nil }()

	// 1. Mock Summary Cards
	rate := 100.0
	mock.ExpectQuery(`(?s)SELECT.*total_requests.*FROM llm_request_logs`).
		WillReturnRows(sqlmock.NewRows([]string{
			"total_requests", "success_rate", "total_tokens", "prompt_tokens",
			"completion_tokens", "reasoning_tokens", "total_cost_usd", "total_cost_cny", "avg_latency_ms",
		}).AddRow(10, &rate, 1000, 400, 600, 100, 0.05, 0.36, 150.0))

	// 2. Mock Model Distribution
	mock.ExpectQuery(`(?s)SELECT.*model_used.*FROM llm_request_logs`).
		WillReturnRows(sqlmock.NewRows([]string{
			"model_used", "spend_usd", "spend_cny", "call_count",
		}).AddRow("gemini-3.8-flash", 0.05, 0.36, 10))

	// 3. Mock Daily Trends
	mock.ExpectQuery(`(?s)SELECT.*date_str.*FROM llm_request_logs`).
		WillReturnRows(sqlmock.NewRows([]string{
			"date_str", "spend_usd", "spend_cny", "requests",
		}).AddRow("2026-10-03", 0.05, 0.36, 10))

	r := setupTestEngineWithVLogs(nil)

	w := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/api/v1/metrics/summary?time_range=24h", nil)
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	cards := resp["cards"].(map[string]interface{})
	if cards["total_requests"].(float64) != 10 {
		t.Errorf("expected total_requests 10, got: %v", cards["total_requests"])
	}
}
