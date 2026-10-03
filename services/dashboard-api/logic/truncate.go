package logic

import (
	"fmt"
	"strings"
)

const (
	MaxSingleMessageChars = 5000
	TruncateHeadChars     = 2500
	TruncateTailChars     = 1500
)

// TruncateTextSafely 若单段文本超过字数上限，保留首尾关键上下文并注入明确的智能抽样折叠提示
func TruncateTextSafely(text string) (string, bool) {
	runes := []rune(text)
	totalLen := len(runes)
	if totalLen <= MaxSingleMessageChars {
		return text, false
	}

	omitted := totalLen - TruncateHeadChars - TruncateTailChars
	head := string(runes[:TruncateHeadChars])
	tail := string(runes[totalLen-TruncateTailChars:])

	truncated := fmt.Sprintf("%s\n\n（... 此处已自动智能抽样截断 %d 字符，该消息单段总长 %d 字符；点击下方「加载全量完整报文」可获取全部未截断内容 ...）\n\n%s",
		head, omitted, totalLen, tail)
	return truncated, true
}

// TruncateContentRecursively 递归检查并安全截断超长字符串、Base64 图片或多模态结构中的大文本
func TruncateContentRecursively(content interface{}) (interface{}, bool) {
	if content == nil {
		return nil, false
	}

	switch val := content.(type) {
	case string:
		// 针对独立 Base64 图片进行识别与折叠
		if len(val) > 500 && strings.HasPrefix(val, "data:image") {
			preview := val
			if len(val) > 60 {
				preview = val[:60]
			}
			msg := fmt.Sprintf("%s... （此处已自动智能折叠 Base64 图片数据 %d 字符；点击下方「加载全量完整报文」可获取全部内容）",
				preview, len(val))
			return msg, true
		}
		return TruncateTextSafely(val)

	case map[string]interface{}:
		newMap := make(map[string]interface{}, len(val))
		anyTruncated := false
		for k, v := range val {
			if k == "image_url" {
				if imgMap, ok := v.(map[string]interface{}); ok {
					if urlStr, ok := imgMap["url"].(string); ok && (len(urlStr) > 500 || strings.HasPrefix(urlStr, "data:image")) {
						preview := urlStr
						if len(urlStr) > 60 {
							preview = urlStr[:60]
						}
						newMap[k] = map[string]interface{}{
							"url": fmt.Sprintf("%s... （此处已自动智能折叠 Base64 图片数据 %d 字符；点击下方「加载全量完整报文」可获取全部内容）",
								preview, len(urlStr)),
						}
						anyTruncated = true
						continue
					}
				}
			}

			newV, tr := TruncateContentRecursively(v)
			if tr {
				anyTruncated = true
			}
			newMap[k] = newV
		}
		return newMap, anyTruncated

	case []interface{}:
		newList := make([]interface{}, len(val))
		anyTruncated := false
		for i, item := range val {
			newV, tr := TruncateContentRecursively(item)
			if tr {
				anyTruncated = true
			}
			newList[i] = newV
		}
		return newList, anyTruncated

	default:
		return content, false
	}
}
