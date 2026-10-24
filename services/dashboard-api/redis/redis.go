package redis

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/nvd11/my-higress-svc/services/dashboard-api/config"
	"github.com/redis/go-redis/v9"
)

var Client *redis.Client

func InitRedis(cfg *config.Config) (*redis.Client, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr:         fmt.Sprintf("%s:%s", cfg.RedisHost, cfg.RedisPort),
		Password:     cfg.RedisPassword,
		DB:           0,
		PoolSize:     30,
		MinIdleConns: 5,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed connecting to Redis: %w", err)
	}

	Client = rdb
	return rdb, nil
}

func CheckHealth(ctx context.Context) error {
	if Client == nil {
		return fmt.Errorf("redis client not initialized")
	}
	return Client.Ping(ctx).Err()
}

// EncodeBlob 将任意字节压成 Gzip + Base64 字符串 (level 1 极速压缩)
func EncodeBlob(raw []byte) string {
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	if err != nil {
		// 压缩器都建不起来时退化为明文，保证数据不丢
		return base64.StdEncoding.EncodeToString(raw)
	}
	if _, err := zw.Write(raw); err != nil {
		_ = zw.Close()
		return base64.StdEncoding.EncodeToString(raw)
	}
	_ = zw.Close()
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

// DecodeBlob 还原 EncodeBlob 的输出，同时兼容历史明文 JSON 字符串
func DecodeBlob(val string) []byte {
	if val == "" {
		return nil
	}
	// Base64 编码后的 Gzip magic number 0x1f 0x8b 对应前缀 "H4sI"
	if strings.HasPrefix(val, "H4sI") {
		dec, err := base64.StdEncoding.DecodeString(val)
		if err != nil {
			return nil
		}
		zr, err := gzip.NewReader(bytes.NewReader(dec))
		if err != nil {
			return nil
		}
		defer zr.Close()
		out, err := io.ReadAll(zr)
		if err != nil {
			return nil
		}
		return out
	}
	return []byte(val)
}

// SetPayloadCache 将 Prompt 和 Response 压缩后写入 Redis (TTL 3天 = 259200秒)
func SetPayloadCache(ctx context.Context, requestID string, promptObj, responseObj interface{}) error {
	if Client == nil {
		return nil
	}

	rawJSON, err := json.Marshal(map[string]interface{}{
		"prompt":   promptObj,
		"response": responseObj,
	})
	if err != nil {
		return err
	}

	cacheKey := fmt.Sprintf("litellm:payload:%s", requestID)
	return Client.Set(ctx, cacheKey, EncodeBlob(rawJSON), 86400*3*time.Second).Err()
}

// GetPayloadCache 从 Redis 读取并解压 Payload (<1ms 极速直出)
func GetPayloadCache(ctx context.Context, requestID string) (promptData map[string]interface{}, responseData map[string]interface{}, found bool) {
	if Client == nil {
		return nil, nil, false
	}

	cacheKey := fmt.Sprintf("litellm:payload:%s", requestID)
	cachedRaw, err := Client.Get(ctx, cacheKey).Result()
	if err != nil || cachedRaw == "" {
		return nil, nil, false
	}

	jsonBytes := DecodeBlob(cachedRaw)
	if len(jsonBytes) == 0 {
		return nil, nil, false
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(jsonBytes, &parsed); err != nil {
		return nil, nil, false
	}

	p, _ := parsed["prompt"].(map[string]interface{})
	r, _ := parsed["response"].(map[string]interface{})
	if p == nil && r == nil {
		return nil, nil, false
	}

	return p, r, true
}

// ============================================================================
// WAL (Write-Ahead Log) 预写日志: 保证网关侧上报的审计记录不会被进程重启吞掉
//
// 写入路径: 处理函数在回 200 之前先同步 SETEX 一条 audit:wip:{request_id},
//
//	三处(MySQL/Redis/VictoriaLogs)全部落盘成功后才 DEL 销账。
//	进程若在中途被杀, 新进程的 replay 协程会扫到残留 key 并幂等重放。
//
// ============================================================================
const (
	WALPrefix     = "audit:wip:"
	WALDeadPrefix = "audit:dead:"
	WALTTL        = 24 * time.Hour
	WALDeadTTL    = 7 * 24 * time.Hour
)

// SetWAL 同步预写一条待落盘的审计记录
func SetWAL(ctx context.Context, requestID string, payload []byte) error {
	if Client == nil || requestID == "" {
		return nil
	}
	key := WALPrefix + requestID
	return Client.Set(ctx, key, EncodeBlob(payload), WALTTL).Err()
}

// GetWAL 读取残留的预写记录, found=false 表示已销账或从未写入
func GetWAL(ctx context.Context, requestID string) ([]byte, bool) {
	if Client == nil || requestID == "" {
		return nil, false
	}
	val, err := Client.Get(ctx, WALPrefix+requestID).Result()
	if err != nil || val == "" {
		return nil, false
	}
	raw := DecodeBlob(val)
	if len(raw) == 0 {
		return nil, false
	}
	return raw, true
}

// DelWAL 三处落盘成功后销账
func DelWAL(ctx context.Context, requestID string) error {
	if Client == nil || requestID == "" {
		return nil
	}
	return Client.Del(ctx, WALPrefix+requestID).Err()
}

// MoveWALToDead 重放超过上限仍失败时转入死信, 保留 7 天供人工排查
func MoveWALToDead(ctx context.Context, requestID string, payload []byte) error {
	if Client == nil || requestID == "" {
		return nil
	}
	return Client.Set(ctx, WALDeadPrefix+requestID, EncodeBlob(payload), WALDeadTTL).Err()
}

// ListWALKeys 用 SCAN 游标分批扫描在途记录 (禁用 KEYS, 避免阻塞 Redis)
func ListWALKeys(ctx context.Context, limit int64) ([]string, error) {
	if Client == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 200
	}

	keys := make([]string, 0, limit)
	var cursor uint64
	for {
		batch, next, err := Client.Scan(ctx, cursor, WALPrefix+"*", 200).Result()
		if err != nil {
			return keys, err
		}
		keys = append(keys, batch...)
		cursor = next
		if cursor == 0 || int64(len(keys)) >= limit {
			break
		}
	}
	if int64(len(keys)) > limit {
		keys = keys[:limit]
	}
	return keys, nil
}
