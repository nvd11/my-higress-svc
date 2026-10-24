package main

import (
	"encoding/json"
	"net/http"

	"github.com/higress-group/wasm-go/pkg/wrapper"
)

// SendRejectSink 异步非阻塞将 401/400 阻断审计账本推送到 Dashboard-API 内部写端点
func SendRejectSink(client wrapper.HttpClient, targetURL string, record GatekeeperRejectRecord) error {
	if targetURL == "" {
		return nil
	}

	bodyBytes, err := json.Marshal(record)
	if err != nil {
		return err
	}

	headers := [][2]string{
		{"Content-Type", "application/json"},
		{"X-Internal-Source", "higress-gatekeeper-wasm"},
	}

	// 异步非阻塞 POST 派发
	return client.Post(targetURL, headers, bodyBytes, func(statusCode int, responseHeaders http.Header, responseBody []byte) {
		// 阻断审计留痕完成
	}, 2000)
}
