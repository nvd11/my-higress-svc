package finops

var (
	idSeed uint64 = 0xda942042e4dd58b5
	chars  = "0123456789abcdef"
)

// nextRand64 fast 64-bit XorShift PRNG (100% deterministic, zero sync/goroutine/syscall)
func nextRand64() uint64 {
	idSeed ^= idSeed << 13
	idSeed ^= idSeed >> 7
	idSeed ^= idSeed << 17
	return idSeed
}

// GenerateRandomHex 生成指定长度的纯随机十六进制字符串
func GenerateRandomHex(length int) string {
	b := make([]byte, length)
	for i := 0; i < length; i++ {
		r := nextRand64()
		b[i] = chars[r%16]
	}
	return string(b)
}

// GenerateRequestID 生成符合 OpenAI 规范的请求 ID: chatcmpl-xxxxxxxx
func GenerateRequestID() string {
	return "chatcmpl-" + GenerateRandomHex(24)
}

// GenerateUUID 生成标准格式的 32 字符伪随机 UUID
func GenerateUUID() string {
	return GenerateRandomHex(32)
}

// GenerateCallID 生成 Tool Call ID: call_xxxxxxxx
func GenerateCallID() string {
	return "call_" + GenerateRandomHex(8)
}
