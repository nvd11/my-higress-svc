package logic

import (
	"strings"
	"testing"
)

func TestTruncateTextSafely(t *testing.T) {
	// 短文本不截断
	shortText := "Hello, World!"
	tr, ok := TruncateTextSafely(shortText)
	if ok || tr != shortText {
		t.Fatalf("expected short text unchanged, got: %s", tr)
	}

	// 构造超过 5000 字符的长文本 (6000 字符)
	longText := strings.Repeat("A", 2500) + strings.Repeat("M", 2000) + strings.Repeat("Z", 1500)
	trLong, okLong := TruncateTextSafely(longText)
	if !okLong {
		t.Fatalf("expected text to be truncated")
	}

	if !strings.HasPrefix(trLong, strings.Repeat("A", 2500)) {
		t.Errorf("head mismatch")
	}
	if !strings.HasSuffix(trLong, strings.Repeat("Z", 1500)) {
		t.Errorf("tail mismatch")
	}
	if !strings.Contains(trLong, "自动智能抽样截断 2000 字符") {
		t.Errorf("missing truncation notice: %s", trLong)
	}
}

func TestTruncateContentRecursively(t *testing.T) {
	data := map[string]interface{}{
		"messages": []interface{}{
			map[string]interface{}{
				"role": "user",
				"content": "data:image/png;base64," + strings.Repeat("X", 800),
			},
		},
	}

	result, ok := TruncateContentRecursively(data)
	if !ok {
		t.Fatalf("expected recursion to truncate image base64")
	}

	resMap := result.(map[string]interface{})
	msgList := resMap["messages"].([]interface{})
	firstMsg := msgList[0].(map[string]interface{})
	contentStr := firstMsg["content"].(string)

	if !strings.Contains(contentStr, "自动智能折叠 Base64 图片数据") {
		t.Errorf("expected collapsed notice, got: %s", contentStr)
	}
}
