package main

import (
	"strings"
	"testing"

	"github.com/higress-group/wasm-go/pkg/wrapper"
)

// fakeCtx 只实现测试需要用到的 SetContext/GetContext, 其余方法由内嵌接口满足。
type fakeCtx struct {
	wrapper.HttpContext
	vals map[string]interface{}
}

func newFakeCtx() *fakeCtx { return &fakeCtx{vals: map[string]interface{}{}} }

func (f *fakeCtx) SetContext(key string, value interface{}) { f.vals[key] = value }
func (f *fakeCtx) GetContext(key string) interface{}        { return f.vals[key] }

// TestStreamingHookRepairsStrippedToolCalls 是防止回归的"接线级"测试:
// 它断言流式响应钩子确实调用了 tool_calls 修复逻辑。
//
// 历史教训: 修复函数本身在单测中是绿的, 但钩子忘了调用它 (4b16d96 回归),
// 于是线上"模型说完半句就停住"的 bug 长期复现。这个测试正是为了锁住这层接线。
func TestStreamingHookRepairsStrippedToolCalls(t *testing.T) {
	ctx := newFakeCtx()
	// 前序 chunk 已流过 Gemini 原始 functionCall (旁路回溯源)
	ctx.vals[ctxResponseBody] = "data: {\"candidates\":[{\"content\":{\"parts\":[{\"functionCall\":{\"name\":\"execute_code\",\"args\":{\"code\":\"print(1)\"}}}]}}]}\n\n"

	// Higress ai-proxy 吐出的空壳 chunk
	shell := []byte("data: {\"id\":\"chatcmpl-1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":null}],\"model\":\"gemini-3.8-flash\"}\n\n")

	out := onHttpStreamingResponseBody(ctx, PluginConfig{}, shell, false)

	if !strings.Contains(string(out), `"finish_reason":"tool_calls"`) {
		t.Fatalf("streaming hook did NOT restore stripped tool_calls; got: %s", out)
	}
	if !strings.Contains(string(out), "execute_code") {
		t.Fatalf("recovered tool call missing function name; got: %s", out)
	}
	if acc, _ := ctx.vals[ctxResponseBody].(string); !strings.Contains(acc, "execute_code") {
		t.Fatalf("accumulated (audited) response missing repaired chunk; got: %s", acc)
	}
}

// 健康 chunk 必须原样透传 (不能误伤正常流)
func TestStreamingHookLeavesHealthyChunkUntouched(t *testing.T) {
	ctx := newFakeCtx()
	healthy := []byte("data: {\"id\":\"c\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"你好\"},\"finish_reason\":null}]}\n\n")

	out := onHttpStreamingResponseBody(ctx, PluginConfig{}, healthy, false)

	if string(out) != string(healthy) {
		t.Fatalf("healthy chunk must pass through untouched; got: %s", out)
	}
}
