package main

import (
	"encoding/json"

	"github.com/higress-group/wasm-go/pkg/wrapper"
)

// SendToMySQLSink 异步将审计账本数据推送到 Dashboard-API 内部写端点
func SendToMySQLSink(client wrapper.HttpClient, targetURL string, record AuditLogRecord) error {
	if targetURL == "" {
		return nil
	}

	bodyBytes, err := json.Marshal(record)
	if err != nil {
		return err
	}

	headers := [][2]string{
		{"Content-Type", "application/json"},
		{"X-Internal-Source", "higress-finops-wasm"},
	}

	// 异步非阻塞 POST 派发
	return client.Post(targetURL, headers, bodyBytes, func(statusCode int, responseHeaders [][2]string, responseBody []byte) {
		// 旁路落库回调
	}, 3000)
}
