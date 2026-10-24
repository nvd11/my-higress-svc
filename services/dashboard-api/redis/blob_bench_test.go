package redis

import (
	"bytes"
	"fmt"
	"math/rand"
	"testing"
)

// 模拟真实报文: 高熵内容(Base64 图片/长文本)压缩最慢，作为最坏情况
func synthPayload(size int) []byte {
	buf := make([]byte, size)
	r := rand.New(rand.NewSource(42))
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	for i := range buf {
		buf[i] = alphabet[r.Intn(len(alphabet))]
	}
	return buf
}

func BenchmarkEncodeBlob(b *testing.B) {
	for _, size := range []int{64 << 10, 1 << 20, 10 << 20, 50 << 20} {
		raw := synthPayload(size)
		b.Run(fmt.Sprintf("%dMB", size>>20)+fmt.Sprintf("_%dKB", size>>10), func(b *testing.B) {
			b.SetBytes(int64(size))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if len(EncodeBlob(raw)) == 0 {
					b.Fatal("encode failed")
				}
			}
		})
	}
}

func BenchmarkDecodeBlob(b *testing.B) {
	for _, size := range []int{1 << 20, 10 << 20} {
		enc := EncodeBlob(bytes.Repeat([]byte("prompt"), size/6+1)[:size])
		b.Run(fmt.Sprintf("%dMB", size>>20), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if len(DecodeBlob(enc)) == 0 {
					b.Fatal("decode failed")
				}
			}
		})
	}
}
