package main

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestCleanRequestID(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
		isUUID   bool
	}{
		{
			name:   "empty string generates new UUID",
			input:  "",
			isUUID: true,
		},
		{
			name:     "single clean UUID",
			input:    "123e4567-e89b-12d3-a456-426614174000",
			expected: "123e4567-e89b-12d3-a456-426614174000",
		},
		{
			name:     "multi-hop comma joined UUIDs takes first",
			input:    "first-uuid-111,second-uuid-222,third-uuid-333",
			expected: "first-uuid-111",
		},
		{
			name:     "multi-hop with spaces",
			input:    "  clean-uuid-aaa , another-uuid-bbb ",
			expected: "clean-uuid-aaa",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cleanRequestID(tt.input)
			if tt.isUUID {
				if len(got) < 32 {
					t.Errorf("cleanRequestID(%q) = %q, expected valid UUID", tt.input, got)
				}
			} else if got != tt.expected {
				t.Errorf("cleanRequestID(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestExtractBearerToken(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"standard Bearer", "Bearer sk-test-123", "sk-test-123"},
		{"lowercase bearer", "bearer sk-test-456", "sk-test-456"},
		{"raw key without prefix", "sk-test-789", "sk-test-789"},
		{"with surrounding spaces", "  Bearer sk-test-space  ", "sk-test-space"},
		{"empty string", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractBearerToken(tt.input)
			if got != tt.expected {
				t.Errorf("extractBearerToken(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestValidateAndResolveModel(t *testing.T) {
	canonicalModels := defaultCanonicalModels
	aliases := defaultAliases

	// 1. Verify all 10 canonical models are allowed and recognized as canonical
	for _, m := range canonicalModels {
		t.Run("canonical_"+m, func(t *testing.T) {
			resolved, isAlias, allowed := validateAndResolveModel(m, canonicalModels, aliases)
			if !allowed {
				t.Fatalf("expected canonical model %s to be allowed", m)
			}
			if isAlias {
				t.Errorf("expected canonical model %s to not be flagged as alias", m)
			}
			if resolved != m {
				t.Errorf("expected resolved model %s, got %s", m, resolved)
			}
		})
	}

	// 2. Verify all 9 aliases resolve correctly
	aliasExpectations := map[string]string{
		"gemini-4":         "gemini-4-argon",
		"gemini-4-flash":   "gemini-4-argon",
		"gemini-3.8":       "gemini-3.8-flash",
		"gemini-2.5-flash": "gemini-3.8-flash",
		"kimi":             "kimi-k3",
		"glm":              "glm-5.3",
		"hunyuan3":         "hy3",
		"luna":             "gpt-5.6-luna",
		"gpt-5.6-luna-a6":  "gpt-5.6-luna",
	}

	if len(aliasExpectations) != 9 {
		t.Fatalf("expected exactly 9 alias test expectations, got %d", len(aliasExpectations))
	}

	for alias, expectedCanonical := range aliasExpectations {
		t.Run("alias_"+alias, func(t *testing.T) {
			resolved, isAlias, allowed := validateAndResolveModel(alias, canonicalModels, aliases)
			if !allowed {
				t.Fatalf("expected alias %s to be allowed", alias)
			}
			if !isAlias {
				t.Errorf("expected alias %s to be flagged as alias", alias)
			}
			if resolved != expectedCanonical {
				t.Errorf("alias %s resolved to %s, want %s", alias, resolved, expectedCanonical)
			}
		})
	}

	// 3. Verify unknown/unauthorized models are rejected
	illegalModels := []string{
		"gemini-unknown",
		"gpt-4o",
		"claude-3-5-sonnet",
		"gemini-3.8-owner",
		"gemini-3.8-alice",
		"gemini-4-argon-search",
		"gemini-4:search",
		"gemini-3.8-flash-search",
		"gemini-3.8-flash:search",
		"llama-3",
		"",
	}

	for _, m := range illegalModels {
		t.Run("illegal_"+m, func(t *testing.T) {
			_, _, allowed := validateAndResolveModel(m, canonicalModels, aliases)
			if allowed {
				t.Errorf("expected model %q to be rejected, but was allowed", m)
			}
		})
	}
}

func TestRewriteBodyModel(t *testing.T) {
	origJSON := []byte(`{"model":"gemini-4","messages":[{"role":"user","content":"hello"}],"temperature":0.7}`)
	newJSON, err := rewriteBodyModel(origJSON, "gemini-4-argon")
	if err != nil {
		t.Fatalf("rewriteBodyModel failed: %v", err)
	}

	parsedModel := gjson.GetBytes(newJSON, "model").String()
	if parsedModel != "gemini-4-argon" {
		t.Errorf("parsed model = %q, want 'gemini-4-argon'", parsedModel)
	}

	// Check that other fields are untouched
	parsedMsg := gjson.GetBytes(newJSON, "messages.0.content").String()
	if parsedMsg != "hello" {
		t.Errorf("message content changed: %q", parsedMsg)
	}

	parsedTemp := gjson.GetBytes(newJSON, "temperature").Float()
	if parsedTemp != 0.7 {
		t.Errorf("temperature changed: %f", parsedTemp)
	}
}

func TestBuildModelsResponse(t *testing.T) {
	models := []string{"gemini-4-argon", "deepseek-v4-pro-0813"}
	respJSON := buildModelsResponse(models)

	parsed := gjson.Parse(respJSON)
	if parsed.Get("object").String() != "list" {
		t.Errorf("object != 'list', got %q", parsed.Get("object").String())
	}

	data := parsed.Get("data").Array()
	if len(data) != 2 {
		t.Fatalf("expected 2 items in data, got %d", len(data))
	}

	if data[0].Get("id").String() != "gemini-4-argon" {
		t.Errorf("data[0].id = %q, want 'gemini-4-argon'", data[0].Get("id").String())
	}
	if data[1].Get("id").String() != "deepseek-v4-pro-0813" {
		t.Errorf("data[1].id = %q, want 'deepseek-v4-pro-0813'", data[1].Get("id").String())
	}
}

func TestParseConfigDefaults(t *testing.T) {
	rawConfig := `{}`
	parsed := gjson.Parse(rawConfig)

	var cfg PluginConfig
	err := parseConfig(parsed, &cfg)
	if err != nil {
		t.Fatalf("parseConfig failed: %v", err)
	}

	// Should fallback to default 8 canonical models
	if len(cfg.CanonicalModels) != 8 {
		t.Errorf("expected 8 default canonical models, got %d", len(cfg.CanonicalModels))
	}

	// Should fallback to default 9 aliases
	if len(cfg.Aliases) != 9 {
		t.Errorf("expected 9 default aliases, got %d", len(cfg.Aliases))
	}

	// Virtual keys should be empty
	if len(cfg.VirtualKeys) != 0 {
		t.Errorf("expected 0 virtual keys, got %d", len(cfg.VirtualKeys))
	}
}
