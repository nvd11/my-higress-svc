package finops

import (
	"encoding/binary"
	"hash/fnv"
)

// Wasm 沙箱里没有 /dev/urandom, 也没有 crypto/rand, 只能拿"请求自带的熵"播种。
//
// 历史事故: 旧实现把种子写死成常量 0xda942042e4dd58b5, 于是每次 Wasm 模块
// 重新实例化都会从 #0 重放同一串 ID, 撞上历史行的 PRIMARY KEY, INSERT 直接
// Error 1062 (Duplicate entry ... for key 'llm_request_logs.PRIMARY'), 记录被
// 静默丢弃。现在改为: 请求熵播种 + 调用计数器 + salt 三重混合, 任何实例重启
// 都不再是同一套剧本。

const (
	// fallbackSeed 仅在无任何熵可用时兜底 (黄金比例常数), 保证状态机不卡在 0
	fallbackSeed uint64 = 0x9e3779b97f4a7c15
	chars               = "0123456789abcdef"
)

var (
	idSeed    uint64
	seeded    bool
	generated uint64
)

// Seed 用请求熵初始化 VM 级随机状态, 仅首次调用生效。
// entropy 建议传 x-request-id / X-Forwarded-For / User-Agent 等"每个实例都不同"的值。
func Seed(entropy ...string) {
	if seeded {
		return
	}
	idSeed = digest(entropy...) ^ fallbackSeed
	if idSeed == 0 {
		idSeed = fallbackSeed
	}
	seeded = true
}

func digest(parts ...string) uint64 {
	h := fnv.New64a()
	for _, p := range parts {
		_, _ = h.Write([]byte(p))
		_, _ = h.Write([]byte{0})
	}
	return h.Sum64()
}

// nextRand64 fast 64-bit XorShift PRNG (零 syscall, 状态由 Seed 决定)
func nextRand64() uint64 {
	idSeed ^= idSeed << 13
	idSeed ^= idSeed >> 7
	idSeed ^= idSeed << 17
	return idSeed
}

// GenerateRandomHex 生成指定长度的十六进制字符串。
// 每一步都把 (当前状态, 调用序号, salt) 搅回状态机, 因此同一实例内连续生成
// 不会退化成固定序列, 跨实例也不会重播 (种子来自请求熵)。
func GenerateRandomHex(salt string, length int) string {
	if !seeded {
		// 调用方没显式 Seed 时的自愈, 至少保证不是固定的初始常量
		Seed(salt)
	}
	generated++

	var buf [8]byte
	h := fnv.New64a()
	binary.BigEndian.PutUint64(buf[:], idSeed)
	_, _ = h.Write(buf[:])
	binary.BigEndian.PutUint64(buf[:], generated)
	_, _ = h.Write(buf[:])
	_, _ = h.Write([]byte(salt))

	idSeed ^= h.Sum64()
	if idSeed == 0 {
		idSeed = fallbackSeed
	}

	out := make([]byte, length)
	for i := range out {
		out[i] = chars[nextRand64()%16]
	}
	return string(out)
}

// GenerateRequestID 生成符合 OpenAI 规范的请求 ID: chatcmpl-xxxxxxxx
func GenerateRequestID(salt string) string {
	return "chatcmpl-" + GenerateRandomHex(salt, 24)
}

// GenerateCallID 生成 Tool Call ID: call_xxxxxxxx
func GenerateCallID(salt string) string {
	return "call_" + GenerateRandomHex(salt, 8)
}
