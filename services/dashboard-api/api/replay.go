package api

import (
	"context"
	"encoding/json"
	"log"
	"strings"
	"time"

	"github.com/nvd11/my-higress-svc/services/dashboard-api/redis"
	"github.com/nvd11/my-higress-svc/services/dashboard-api/vlogs"
)

// walEnvelope WAL 在途记录信封 (带重放次数, 便于死信判定)
type walEnvelope struct {
	Attempts int                  `json:"attempts"`
	FirstAt  time.Time            `json:"first_at"`
	Record   WasmAuditRecordInput `json:"record"`
}

const (
	// walMaxAttempts 重放次数上限, 超过即转入死信 (audit:dead:*)
	walMaxAttempts = 5

	// replayTimeout 单条重放落盘预算
	replayTimeout = 30 * time.Second
)

// StartWALReplay 启动 WAL 重放守护协程: 启动即补一次, 随后按 interval 轮询。
// 这是"预写兜底"的最后一环 —— 进程被杀导致的中断由它补回来。
func StartWALReplay(ctx context.Context, vlogsClient *vlogs.Client, interval time.Duration, limit int64) {
	if interval <= 0 {
		interval = 60 * time.Second
	}
	if limit <= 0 {
		limit = 200
	}

	log.Printf("🛡️ WAL replay guardian started (interval=%s, batch=%d)", interval, limit)

	replaySwallowed(ctx, vlogsClient, limit)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Println("🛡️ WAL replay guardian stopped")
			return
		case <-ticker.C:
			replaySwallowed(ctx, vlogsClient, limit)
		}
	}
}

func replaySwallowed(ctx context.Context, vlogsClient *vlogs.Client, limit int64) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("❌ Panic in WAL replay: %v", r)
		}
	}()
	if done := ReplayWAL(ctx, vlogsClient, limit); done > 0 {
		log.Printf("🔁 WAL replay recovered %d record(s)", done)
	}
}

// ReplayWAL 扫描残留 WAL 并幂等重放, 返回成功销账条数
func ReplayWAL(ctx context.Context, vlogsClient *vlogs.Client, limit int64) int {
	keys, err := redis.ListWALKeys(ctx, limit)
	if err != nil {
		log.Printf("⚠️ WAL scan failed: %v", err)
	}
	if len(keys) == 0 {
		return 0
	}

	log.Printf("🔁 WAL replay: %d pending record(s) found", len(keys))
	acknowledged := 0

	for _, key := range keys {
		if ctx.Err() != nil {
			break
		}
		requestID := strings.TrimPrefix(key, redis.WALPrefix)
		if requestID == "" {
			continue
		}

		raw, found := redis.GetWAL(ctx, requestID)
		if !found {
			continue
		}

		var env walEnvelope
		if err := json.Unmarshal(raw, &env); err != nil {
			// 信封损坏: 转死信, 不阻塞后续重放
			log.Printf("☠️ Corrupt WAL entry %s, moved to dead: %v", requestID, err)
			_ = redis.MoveWALToDead(ctx, requestID, raw)
			_ = redis.DelWAL(ctx, requestID)
			continue
		}

		if env.FirstAt.IsZero() {
			env.FirstAt = time.Now().UTC()
		}
		env.Attempts++
		rec := env.Record
		if rec.RequestID == "" {
			rec.RequestID = requestID
		}

		rctx, cancel := context.WithTimeout(ctx, replayTimeout)
		persistErr := PersistRecord(rctx, rec, vlogsClient)
		cancel()

		if persistErr == nil {
			if derr := redis.DelWAL(ctx, requestID); derr != nil {
				log.Printf("⚠️ WAL ack failed for %s: %v", requestID, derr)
			} else {
				acknowledged++
			}
			continue
		}

		if env.Attempts >= walMaxAttempts {
			stored, _ := json.Marshal(env)
			_ = redis.MoveWALToDead(ctx, requestID, stored)
			_ = redis.DelWAL(ctx, requestID)
			log.Printf("☠️ WAL replay exhausted (%d attempts), moved to audit:dead:%s: %v",
				env.Attempts, requestID, persistErr)
			continue
		}

		stored, mErr := json.Marshal(env)
		if mErr != nil {
			log.Printf("⚠️ WAL re-marshal failed for %s: %v", requestID, mErr)
			continue
		}
		if sErr := redis.SetWAL(ctx, requestID, stored); sErr != nil {
			log.Printf("⚠️ WAL rewrite failed for %s: %v", requestID, sErr)
		}
		log.Printf("🔁 WAL replay retry %d/%d for %s: %v",
			env.Attempts, walMaxAttempts, requestID, persistErr)
	}

	return acknowledged
}
