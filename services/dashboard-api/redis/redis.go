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

	// 1. 内存 Gzip (level 1 极速压缩)
	var buf bytes.Buffer
	zw, err := gzip.NewWriterLevel(&buf, gzip.BestSpeed)
	if err != nil {
		return err
	}
	if _, err := zw.Write(rawJSON); err != nil {
		return err
	}
	_ = zw.Close()

	// 2. Base64 编码
	val := base64.StdEncoding.EncodeToString(buf.Bytes())
	cacheKey := fmt.Sprintf("litellm:payload:%s", requestID)

	return Client.Set(ctx, cacheKey, val, 86400*3*time.Second).Err()
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

	var jsonBytes []byte
	// 兼容判断: 是否为 Gzip+Base64 编码 (Base64 编码后的 Gzip magic number 0x1f 0x8b 对应前缀为 "H4sI")
	if strings.HasPrefix(cachedRaw, "H4sI") {
		dec, err := base64.StdEncoding.DecodeString(cachedRaw)
		if err == nil {
			zr, err := gzip.NewReader(bytes.NewReader(dec))
			if err == nil {
				jsonBytes, _ = io.ReadAll(zr)
				_ = zr.Close()
			}
		}
	} else {
		// 兼容历史纯明文 JSON 字符串
		jsonBytes = []byte(cachedRaw)
	}

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
