package finops

import (
	"testing"
)

func TestCalculateCost(t *testing.T) {
	// 针对 Gemini 3.8 Flash
	costUSD, costCNY := CalculateCost("gemini-3.8-flash", 1000000, 1000000, 0, 7.23)
	expectedUSD := 4.5
	expectedCNY := 32.535

	if costUSD != expectedUSD {
		t.Errorf("costUSD = %f; want %f", costUSD, expectedUSD)
	}
	if costCNY != expectedCNY {
		t.Errorf("costCNY = %f; want %f", costCNY, expectedCNY)
	}

	// 针对 DeepSeek V4 Pro
	dsUSD, dsCNY := CalculateCost("deepseek-v4-pro-0813", 1000000, 1000000, 0, 7.23)
	expectedDsUSD := 0.00192
	expectedDsCNY := 0.013882
	if dsUSD != expectedDsUSD {
		t.Errorf("deepseek costUSD = %f; want %f", dsUSD, expectedDsUSD)
	}
	if dsCNY != expectedDsCNY {
		t.Errorf("deepseek costCNY = %f; want %f", dsCNY, expectedDsCNY)
	}
}

func TestResolveVirtualKey(t *testing.T) {
	vkeys := map[string]string{
		"sk-cindy-secret": "cindy",
		"sk-moon-secret":  "moon",
	}

	alias, ok := ResolveVirtualKey("Bearer sk-cindy-secret", vkeys)
	if !ok || alias != "cindy" {
		t.Errorf("want cindy, got %s (ok=%v)", alias, ok)
	}

	alias, ok = ResolveVirtualKey("Bearer sk-invalid", vkeys)
	if ok || alias != "" {
		t.Errorf("want empty/false, got %s (ok=%v)", alias, ok)
	}

	alias, ok = ResolveVirtualKey("", vkeys)
	if ok || alias != "" {
		t.Errorf("want empty/false for empty authHeader, got %s (ok=%v)", alias, ok)
	}

	alias, ok = ResolveVirtualKey("Bearer   ", vkeys)
	if ok || alias != "" {
		t.Errorf("want empty/false for whitespace token, got %s (ok=%v)", alias, ok)
	}
}
