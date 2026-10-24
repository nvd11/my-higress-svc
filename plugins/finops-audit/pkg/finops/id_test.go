package finops

import (
	"strings"
	"testing"
)

// resetState 模拟一次全新的 Wasm 模块实例化 (种子与计数器全部归零)
func resetState() {
	idSeed = 0
	seeded = false
	generated = 0
}

// Seed 只应在首次调用生效, 后续请求不得改写实例级种子
func TestSeedOnlyAppliesOnce(t *testing.T) {
	resetState()
	Seed("req-1", "1.1.1.1")
	first := idSeed

	Seed("req-2", "2.2.2.2")
	if idSeed != first {
		t.Errorf("seed must be sticky within a VM instance")
	}
}

// 没有任何熵可用时也不能退回 0 状态 (否则又重新变成固定剧本)
func TestSeedWithoutEntropyIsNotZero(t *testing.T) {
	resetState()
	Seed()
	if idSeed == 0 {
		t.Fatalf("seed must never stay zero")
	}
}

// 同一实例内连续生成的 ID 必须互不相同
func TestGenerateRandomHexUnique(t *testing.T) {
	resetState()
	Seed("req-seed", "1.2.3.4")

	seen := make(map[string]bool, 1000)
	for i := 0; i < 1000; i++ {
		id := GenerateRandomHex("salt", 32)
		if len(id) != 32 {
			t.Fatalf("expected 32 hex chars, got %d", len(id))
		}
		if seen[id] {
			t.Fatalf("duplicate id generated: %s", id)
		}
		seen[id] = true
	}
}

// 事故核心回归: 两个"实例"用不同熵播种, 首个 ID 必须不同
// (旧实现固定种子, 两次实例化的第 #0 个 ID 完全一样)
func TestDifferentInstancesNeverReplaySequence(t *testing.T) {
	resetState()
	Seed("instance-A", "10.0.0.1")
	a := GenerateRandomHex("same-salt", 32)

	resetState()
	Seed("instance-B", "10.0.0.2")
	b := GenerateRandomHex("same-salt", 32)

	if a == b {
		t.Fatalf("two instances replayed the same id sequence: %s", a)
	}
}

// 不同 salt 必须产出不同 ID (同一实例内也不能靠 salt 撞车)
func TestSaltSeparatesIDs(t *testing.T) {
	resetState()
	Seed("req-seed")
	if GenerateRandomHex("salt-1", 32) == GenerateRandomHex("salt-1", 32) {
		t.Errorf("consecutive calls with same salt must differ (counter mixing)")
	}

	resetState()
	Seed("req-seed")
	x := GenerateRandomHex("alpha", 32)
	y := GenerateRandomHex("beta", 32)
	if x == y {
		t.Errorf("different salts must not collapse to same id")
	}
}

// 未显式 Seed 时生成器必须自愈播种, 而不是沿用零状态
func TestGenerateWithoutExplicitSeedSelfHeals(t *testing.T) {
	resetState()
	if !seeded {
		_ = GenerateRequestID("auto-salt")
	}
	if !seeded {
		t.Fatalf("generator must self-seed when Seed was never called")
	}
}

// 前缀与长度契约
func TestIDFormats(t *testing.T) {
	resetState()
	Seed("fmt")

	reqID := GenerateRequestID("x")
	if !strings.HasPrefix(reqID, "chatcmpl-") || len(reqID) != len("chatcmpl-")+24 {
		t.Errorf("unexpected request id format: %s", reqID)
	}

	callID := GenerateCallID("y")
	if !strings.HasPrefix(callID, "call_") || len(callID) != len("call_")+8 {
		t.Errorf("unexpected call id format: %s", callID)
	}
}
