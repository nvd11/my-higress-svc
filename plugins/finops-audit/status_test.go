package main

import (
	"strings"
	"testing"
)

func TestParseStatusCode(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected int
	}{
		{"empty string defaults to 200", "", 200},
		{"standard 200", "200", 200},
		{"client error 400", "400", 400},
		{"auth error 401", "401", 401},
		{"forbidden 403", "403", 403},
		{"rate limit 429", "429", 429},
		{"server error 500", "500", 500},
		{"bad gateway 502", "502", 502},
		{"service unavailable 503", "503", 503},
		{"invalid string defaults to 200", "invalid", 200},
		{"negative number defaults to 200", "-1", 200},
		{"zero defaults to 200", "0", 200},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseStatusCode(tt.input)
			if got != tt.expected {
				t.Errorf("parseStatusCode(%q) = %d, want %d", tt.input, got, tt.expected)
			}
		})
	}
}

func TestExtractErrorMsg(t *testing.T) {
	tests := []struct {
		name        string
		statusCode  int
		response    string
		expectNil   bool
		expectedMsg string
	}{
		{
			name:        "status 200 returns nil",
			statusCode:  200,
			response:    `{"error": {"message": "should ignore for 200"}}`,
			expectNil:   true,
			expectedMsg: "",
		},
		{
			name:        "empty response returns nil",
			statusCode:  500,
			response:    "",
			expectNil:   true,
			expectedMsg: "",
		},
		{
			name:        "OpenAI error JSON format",
			statusCode:  400,
			response:    `{"error":{"message":"The model 'gemini-unknown' does not exist","type":"invalid_request_error"}}`,
			expectNil:   false,
			expectedMsg: "The model 'gemini-unknown' does not exist",
		},
		{
			name:        "Simple message JSON format",
			statusCode:  429,
			response:    `{"message":"Rate limit exceeded"}`,
			expectNil:   false,
			expectedMsg: "Rate limit exceeded",
		},
		{
			name:        "Plain text error",
			statusCode:  502,
			response:    "Bad Gateway: upstream connection refused",
			expectNil:   false,
			expectedMsg: "Bad Gateway: upstream connection refused",
		},
		{
			name:        "Truncates oversized error to 1024 bytes",
			statusCode:  500,
			response:    strings.Repeat("E", 2000),
			expectNil:   false,
			expectedMsg: strings.Repeat("E", 1024),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractErrorMsg(tt.statusCode, tt.response)
			if tt.expectNil {
				if got != nil {
					t.Errorf("extractErrorMsg(%d, %q) = %v, want nil", tt.statusCode, tt.response, *got)
				}
			} else {
				if got == nil {
					t.Fatalf("extractErrorMsg(%d, %q) returned nil, want %q", tt.statusCode, tt.response, tt.expectedMsg)
				}
				if *got != tt.expectedMsg {
					t.Errorf("extractErrorMsg(%d, %q) = %q, want %q", tt.statusCode, tt.response, *got, tt.expectedMsg)
				}
			}
		})
	}
}

func TestFallbackModelName(t *testing.T) {
	tests := []struct {
		name     string
		response string
		expected string
	}{
		{
			name:     "empty response defaults to unknown",
			response: "",
			expected: "unknown",
		},
		{
			name:     "invalid json defaults to unknown",
			response: "not a json",
			expected: "unknown",
		},
		{
			name:     "json without model defaults to unknown",
			response: `{"error":{"message":"Request body is too large"}}`,
			expected: "unknown",
		},
		{
			name:     "json with model field extracts correctly",
			response: `{"model":"gemini-3.8-flash","choices":[]}`,
			expected: "gemini-3.8-flash",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fallbackModelName(tt.response)
			if got != tt.expected {
				t.Errorf("fallbackModelName(%q) = %q, want %q", tt.response, got, tt.expected)
			}
		})
	}
}
