package model

import (
	"errors"
	"testing"
)

func TestContextWindow(t *testing.T) {
	for spec, want := range map[string]int{
		"openai/gpt-4o":                      128_000,
		"gpt-4o-mini":                        128_000,
		"openai/gpt-4.1-mini":                1_047_576,
		"anthropic/claude-sonnet-4":          200_000,
		"openrouter/anthropic/claude-opus-4": 200_000,
		"ollama/llama3":                      0,
		"openai/unknown":                     0,
	} {
		if got := ContextWindow(spec); got != want {
			t.Errorf("ContextWindow(%q) = %d, want %d", spec, got, want)
		}
	}
}

func TestIsContextOverflow(t *testing.T) {
	yes := []string{
		`HTTP 400: {"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 210000 tokens > 200000 maximum"}}`,
		`HTTP 400: {"error":{"code":"context_length_exceeded","message":"..."}}`,
		`HTTP 400: This model's maximum context length is 128000 tokens.`,
	}
	for _, s := range yes {
		if !IsContextOverflow(errors.New(s)) {
			t.Errorf("IsContextOverflow(%q) = false", s)
		}
	}
	for _, s := range []string{"HTTP 401: bad key", "HTTP 500: overloaded"} {
		if IsContextOverflow(errors.New(s)) {
			t.Errorf("IsContextOverflow(%q) = true", s)
		}
	}
	if IsContextOverflow(nil) {
		t.Error("IsContextOverflow(nil) = true")
	}
}
