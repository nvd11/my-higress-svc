package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/alicebob/miniredis/v2"
	"github.com/jmoiron/sqlx"
	goredis "github.com/redis/go-redis/v9"

	"github.com/nvd11/my-higress-svc/services/dashboard-api/db"
	dredis "github.com/nvd11/my-higress-svc/services/dashboard-api/redis"
)

// setupMiniRedis 用纯内存 Redis 替换全局客户端，模拟真实的 Redis 行为
func setupMiniRedis(t *testing.T) *miniredis.Miniredis {
	t.Helper()
	mr := miniredis.RunT(t)
	dredis.Client = goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { dredis.Client = nil })
	return mr
}

// 端到端：预写成功 → 落盘"被杀死"(MySQL 不可用) → 重启后重放补回并销账
func TestWALCrashRecoveryEndToEnd(t *testing.T) {
	mr := setupMiniRedis(t)

	// 模拟"落盘阶段进程被杀"：此刻 MySQL 尚不可用，异步落盘必然失败
	db.DB = nil

	rec := testRecord()
	body, _ := json.Marshal(rec)

	r := setupTestEngineWithVLogs(nil)
	w := httptest.NewRecorder()
	req, _ := http.NewRequest("POST", "/api/v1/internal/audit-log", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 accepted, got %d", w.Code)
	}

	// 等异步 goroutine 失败退出
	time.Sleep(150 * time.Millisecond)

	// ① 关键断言：进程"死"了，但 WAL 还在，且信封可解码
	walKey := dredis.WALPrefix + rec.RequestID
	if !mr.Exists(walKey) {
		t.Fatalf("WAL entry missing after failed persist — 兜底失效！")
	}
	raw, found := dredis.GetWAL(context.Background(), rec.RequestID)
	if !found {
		t.Fatalf("WAL entry unreadable")
	}
	var env walEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("WAL envelope corrupt: %v", err)
	}
	if env.Record.RequestID != rec.RequestID || env.Record.Prompt != rec.Prompt || env.Record.Response != rec.Response {
		t.Fatalf("WAL record content lost: %+v", env.Record)
	}

	// ② 模拟"新进程启动"：MySQL 恢复，重放协程把记录补回
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock init failed: %v", err)
	}
	defer mockDB.Close()
	db.DB = sqlx.NewDb(mockDB, "sqlmock")
	defer func() { db.DB = nil }()

	mock.ExpectExec("INSERT INTO llm_request_logs").WillReturnResult(sqlmock.NewResult(1, 1))

	recovered := ReplayWAL(context.Background(), nil, 100)
	if recovered != 1 {
		t.Fatalf("expected 1 recovered record, got %d", recovered)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Errorf("MySQL insert not replayed: %v", err)
	}

	// ③ 销账：WAL 必须被删除，且热缓存里已经有全量报文
	if mr.Exists(walKey) {
		t.Errorf("WAL entry not acknowledged after successful replay")
	}
	cacheKey := "litellm:payload:" + rec.RequestID
	if !mr.Exists(cacheKey) {
		t.Errorf("payload cache not repopulated by replay")
	}
}

// 重放持续失败必须转死信，不能无限重试拖垮 Redis
func TestWALDeadLetterAfterMaxAttempts(t *testing.T) {
	mr := setupMiniRedis(t)

	env := walEnvelope{
		Attempts: walMaxAttempts - 1,
		FirstAt:  time.Now().UTC(),
		Record:   testRecord(),
	}
	payload, _ := json.Marshal(env)
	if err := dredis.SetWAL(context.Background(), env.Record.RequestID, payload); err != nil {
		t.Fatalf("seed WAL failed: %v", err)
	}

	// MySQL 不可用 → 重放必失败
	db.DB = nil

	recovered := ReplayWAL(context.Background(), nil, 100)
	if recovered != 0 {
		t.Errorf("expected 0 recovered, got %d", recovered)
	}

	rid := env.Record.RequestID
	if mr.Exists(dredis.WALPrefix + rid) {
		t.Errorf("in-flight WAL should be removed after exhausting attempts")
	}
	if !mr.Exists(dredis.WALDeadPrefix + rid) {
		t.Errorf("exhausted WAL should be moved to dead letter")
	}
}

// 多个在途记录必须被 SCAN 全部捞到（不能漏单）
func TestWALListScanMultiple(t *testing.T) {
	mr := setupMiniRedis(t)

	for _, id := range []string{"r1", "r2", "r3"} {
		if err := dredis.SetWAL(context.Background(), id, []byte(`{"record":{}}`)); err != nil {
			t.Fatalf("seed %s failed: %v", id, err)
		}
	}
	// 干扰项：热缓存 key 不应被 WAL 扫描捞到
	mr.Set("litellm:payload:r9", "noise")

	keys, err := dredis.ListWALKeys(context.Background(), 100)
	if err != nil {
		t.Fatalf("scan failed: %v", err)
	}
	if len(keys) != 3 {
		t.Fatalf("expected 3 WAL keys, got %d: %v", len(keys), keys)
	}
}
