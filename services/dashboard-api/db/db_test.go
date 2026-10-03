package db

import (
	"testing"
	"time"
)

func TestLLMRequestLogModel(t *testing.T) {
	record := LLMRequestLog{
		ID:               "test-id",
		RequestID:        "chatcmpl-test",
		APIKeyAlias:      "default",
		ModelRequested:   "gemini-3.8-flash",
		ModelUsed:        "gemini-3.8-flash",
		Provider:         "google-gemini",
		ProviderKeyAlias: "OPENAI_API_KEY_FREE_3",
		PromptTokens:     10,
		CompletionTokens: 20,
		ReasoningTokens:  5,
		TotalTokens:      35,
		CostUSD:          0.001,
		CostCNY:          0.007,
		FxRate:           7.23,
		LatencyMS:        150,
		StatusCode:       200,
		CreatedAt:        time.Now(),
	}

	if record.ReasoningTokens != 5 {
		t.Errorf("expected ReasoningTokens 5, got %d", record.ReasoningTokens)
	}
	if record.TotalTokens != 35 {
		t.Errorf("expected TotalTokens 35, got %d", record.TotalTokens)
	}
}
