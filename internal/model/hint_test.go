package model

import (
	"strings"
	"testing"
)

func TestHint(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OLLAMA_HOST", "")
	cases := []struct {
		spec, err string
		want      string // substring; "" means no hint
	}{
		{"gpt-4o", "HTTP 401: invalid key", "export OPENAI_API_KEY="},
		{"openai/gpt-4o", "HTTP 401: invalid key", "export OPENAI_API_KEY="},
		{"openai/gpt-nope", "HTTP 404: model not found", `"gpt-nope"`},
		{"openrouter/qwen/qwen3-coder", "HTTP 401: no auth", "export OPENROUTER_API_KEY="},
		{"anthropic/claude-sonnet-4-5", "HTTP 401: x-api-key header is required", "export ANTHROPIC_API_KEY="},
		{"anthropic/claude-sonnet-4-5", "HTTP 403: forbidden", "export ANTHROPIC_API_KEY="},
		{"ollama/llama3.1", `giving up after 6 attempts: Post "http://127.0.0.1:11434/v1/chat/completions": dial tcp 127.0.0.1:11434: connect: connection refused`, "ollama serve"},
		{"ollama/llama3.1", `HTTP 404: {"error":{"message":"model \"llama3.1\" not found"}}`, "ollama pull llama3.1"},
		{"codex/gpt-5.5", "HTTP 400: The 'gpt-5.5' model is not supported when using Codex with a ChatGPT account.", "Codex CLI's model picker"},
		{"codex/gpt-5.6-luna", "codex: no login at /home/u/.codex/auth.json; run `codex login` and sign in with ChatGPT", "codex login"},
		// Ordinary failures keep just the error.
		{"openai/gpt-4o", "giving up after 6 attempts: HTTP 503: overloaded", ""},
		{"anthropic/claude-sonnet-4-5", "stream error: overloaded_error: Overloaded", ""},
		{"ollama/llama3.1", "HTTP 400: bad request", ""},
		{"codex/gpt-5.6-luna", "codex: response failed", ""},
	}
	for _, c := range cases {
		got := Hint(c.spec, c.err)
		if c.want == "" && got != "" || !strings.Contains(got, c.want) {
			t.Errorf("Hint(%q, %q) = %q, want it to contain %q", c.spec, c.err, got, c.want)
		}
	}
}

func TestHintKeySetButRejected(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-secret-value")
	got := Hint("openai/gpt-4o", "HTTP 401: Incorrect API key provided")
	if !strings.Contains(got, "OPENAI_API_KEY") || strings.Contains(got, "export") {
		t.Errorf("hint = %q, want a check-your-key hint", got)
	}
	if strings.Contains(got, "sk-secret-value") {
		t.Errorf("hint leaks the key: %q", got)
	}
}

func TestHintOllamaHost(t *testing.T) {
	t.Setenv("OLLAMA_HOST", "gpu-box:9999")
	got := Hint("ollama/llama3.1", "dial tcp: lookup gpu-box: no such host")
	if !strings.Contains(got, "http://gpu-box:9999") || !strings.Contains(got, "OLLAMA_HOST") {
		t.Errorf("hint = %q, want the resolved Ollama URL and OLLAMA_HOST", got)
	}
}
