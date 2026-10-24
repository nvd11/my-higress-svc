package api

import (
	"encoding/hex"
	"testing"
	"time"
)

// 主键必须确定性、定长、十六进制 —— 这是幂等与防撞车的根基
func TestAuditRowIDDeterministic(t *testing.T) {
	rec := testRecord()

	id1 := auditRowID(rec)
	id2 := auditRowID(rec)

	if id1 != id2 {
		t.Fatalf("row id must be deterministic: %s != %s", id1, id2)
	}
	if len(id1) != 32 {
		t.Errorf("row id must be 32 hex chars, got %d: %s", len(id1), id1)
	}
	if _, err := hex.DecodeString(id1); err != nil {
		t.Errorf("row id is not valid hex: %v", err)
	}
}

// 时区无关: 同一时刻换一种表示，必须算出同一个 id（WAL 反序列化后时区可能变化）
func TestAuditRowIDTimezoneInsensitive(t *testing.T) {
	rec := testRecord()
	base := rec.CreatedAt

	recUTC := rec
	recUTC.CreatedAt = base.UTC()
	recCST := rec
	recCST.CreatedAt = base.In(time.FixedZone("CST", 8*3600))

	if auditRowID(recUTC) != auditRowID(recCST) {
		t.Errorf("same instant with different location must yield same id")
	}
}

// 同一 request_id 的不同次上报不得互相覆盖（历史上已有 720 组重复 request_id）
func TestAuditRowIDSeparatesSameRequestID(t *testing.T) {
	rec := testRecord()

	other := rec
	other.CreatedAt = rec.CreatedAt.Add(time.Nanosecond)

	if auditRowID(rec) == auditRowID(other) {
		t.Errorf("different createdAt must yield different id")
	}

	// 不同 request_id 更必须不同
	third := rec
	third.RequestID = rec.RequestID + "-v2"
	if auditRowID(rec) == auditRowID(third) {
		t.Errorf("different request_id must yield different id")
	}
}

// 复现线上事故: 旧插件会用重放的伪随机 ID 上报多条不同请求，
// 行主键必须只由 (request_id, created_at) 决定, 与插件自报 ID 无关。
func TestAuditRowIDIgnoresPluginID(t *testing.T) {
	a := testRecord()
	b := testRecord()
	b.RequestID = "another-request"
	b.CreatedAt = a.CreatedAt // 连创建时间都相同, 也必须区分开

	if auditRowID(a) == auditRowID(b) {
		t.Fatalf("row id must depend solely on request_id + created_at")
	}
}
