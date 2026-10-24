package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jmoiron/sqlx"
	"github.com/nvd11/my-higress-svc/services/dashboard-api/config"
	"github.com/nvd11/my-higress-svc/services/dashboard-api/db"
	"github.com/nvd11/my-higress-svc/services/dashboard-api/vlogs"
)

func testRecord() WasmAuditRecordInput {
	return WasmAuditRecordInput{
		ID:             "wal-test-id",
		RequestID:      "wal-test-req",
		APIKeyAlias:    "cindy",
		ModelRequested: "deepseek-v4-pro-0813",
		ModelUsed:      "deepseek-v4-pro-0813",
		Provider:       "a6api",
		TotalTokens:    42,
		FxRate:         7.23,
		StatusCode:     200,
		CreatedAt:      time.Now(),
		Prompt:         `{"messages":[{"role":"user","content":"hi"}]}`,
		Response:       `{"reply":"hello"}`,
	}
}

// WAL 信封必须能完整往返（重放依赖它，字段丢了就补不回来）
func TestWALEnvelopeRoundtrip(t *testing.T) {
	rec := testRecord()
	raw, err := json.Marshal(walEnvelope{Attempts: 2, FirstAt: time.Now().UTC(), Record: rec})
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	var env walEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if env.Attempts != 2 {
		t.Errorf("attempts lost: %d", env.Attempts)
	}
	if env.Record.RequestID != rec.RequestID || env.Record.Prompt != rec.Prompt || env.Record.Response != rec.Response {
		t.Errorf("record payload lost: %+v", env.Record)
	}
}

// PersistRecord 成功时返回 nil，且 SQL 必须是幂等的 ON DUPLICATE 形式
func TestPersistRecordIdempotentInsert(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock init failed: %v", err)
	}
	defer mockDB.Close()
	db.DB = sqlx.NewDb(mockDB, "sqlmock")
	defer func() { db.DB = nil }()

	mock.ExpectExec("INSERT INTO llm_request_logs").WillReturnResult(sqlmock.NewResult(1, 1))

	if err := PersistRecord(context.Background(), testRecord(), nil); err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("sql expectation unmet: %v", err)
	}
}

// MySQL 写失败必须返回错误（调用方据此保留 WAL 等待重放）
func TestPersistRecordKeepsWALOnMySQLFailure(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock init failed: %v", err)
	}
	defer mockDB.Close()
	db.DB = sqlx.NewDb(mockDB, "sqlmock")
	defer func() { db.DB = nil }()

	mock.ExpectExec("INSERT INTO llm_request_logs").WillReturnError(context.DeadlineExceeded)

	if err := PersistRecord(context.Background(), testRecord(), nil); err == nil {
		t.Fatalf("expected error so WAL is retained, got nil")
	}
}

// VictoriaLogs 抖动时应自动重试，第二次成功即视为整体成功
func TestWriteVLogsWithRetry(t *testing.T) {
	var calls int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	client := vlogs.NewClient(&config.Config{VictoriaLogsURL: ts.URL})
	meta := map[string]interface{}{"model": "deepseek-v4-pro-0813"}

	err := writeVLogsWithRetry(context.Background(), client, "wal-test-req",
		map[string]interface{}{"messages": []interface{}{}}, map[string]interface{}{"reply": "ok"}, meta)
	if err != nil {
		t.Fatalf("expected retry to succeed on 3rd attempt, got: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("expected 3 attempts, got %d", got)
	}
}

// VictoriaLogs 持续失败时必须把错误抛出来（不再静默吞掉）
func TestWriteVLogsExhausted(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer ts.Close()

	client := vlogs.NewClient(&config.Config{VictoriaLogsURL: ts.URL})
	err := writeVLogsWithRetry(context.Background(), client, "wal-test-req",
		map[string]interface{}{}, map[string]interface{}{}, nil)
	if err == nil {
		t.Fatalf("expected error after exhausting retries")
	}
}

// 无 Redis 时重放协程必须安全空转（降级启动 / 单测环境）
func TestReplayWALNoRedis(t *testing.T) {
	if got := ReplayWAL(context.Background(), nil, 10); got != 0 {
		t.Errorf("expected 0 recovered records, got %d", got)
	}
}
