package model

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// withProviders sets defs for the test and restores the registry after.
func withProviders(t *testing.T, defs ...ProviderDef) {
	t.Helper()
	old := configured
	SetProviders(defs)
	t.Cleanup(func() { configured = old })
}

type captured struct {
	mu      sync.Mutex
	path    string
	headers http.Header
	body    map[string]any
}

func captureServer(t *testing.T, reply string) (*httptest.Server, *captured) {
	c := &captured{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.mu.Lock()
		c.path, c.headers = r.URL.Path, r.Header.Clone()
		json.NewDecoder(r.Body).Decode(&c.body)
		c.mu.Unlock()
		io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return srv, c
}

const (
	cpChatReply      = `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1000000,"completion_tokens":1000000}}`
	cpResponsesReply = `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1000000,"output_tokens":1000000}}`
	cpAnthropicReply = `{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1000000,"output_tokens":1000000}}`
)

// TestConfiguredProviderRequests covers each api's path, model, auth and
// headers (AC1-AC4) and configured pricing (AC7).
func TestConfiguredProviderRequests(t *testing.T) {
	for _, c := range []struct {
		api, reply, path, authHeader, authValue string
	}{
		{ProviderAPIChat, cpChatReply, "/chat/completions", "Authorization", "Bearer k"},
		{ProviderAPIResponses, cpResponsesReply, "/responses", "Authorization", "Bearer k"},
		{ProviderAPIAnthropic, cpAnthropicReply, "/v1/messages", "X-Api-Key", "k"},
	} {
		t.Run(c.api, func(t *testing.T) {
			srv, got := captureServer(t, c.reply)
			t.Setenv("GW_KEY_VAR", "k")
			t.Setenv("GW_TENANT_VAR", "tenant-1")
			withProviders(t, ProviderDef{
				ID: "gw", API: c.api, BaseURL: srv.URL + "/", APIKeyEnv: "GW_KEY_VAR",
				Headers:    map[string]string{"X-Static": "s"},
				EnvHeaders: map[string]string{"X-Tenant": "GW_TENANT_VAR"},
				Models:     map[string]ModelMeta{"vendor/m1": {InputPricePerMTok: 2, OutputPricePerMTok: 3}},
			})
			m, err := New("gw/vendor/m1", nil)
			if err != nil {
				t.Fatal(err)
			}
			_, cost, err := m.Query(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got.path != c.path || got.body["model"] != "vendor/m1" {
				t.Errorf("path %q, model %v", got.path, got.body["model"])
			}
			if v := got.headers.Get(c.authHeader); v != c.authValue {
				t.Errorf("%s = %q", c.authHeader, v)
			}
			if got.headers.Get("X-Static") != "s" || got.headers.Get("X-Tenant") != "tenant-1" {
				t.Errorf("headers %v", got.headers)
			}
			if cost.USD != 5 {
				t.Errorf("cost = %v, want 5 from configured prices", cost.USD)
			}
		})
	}
}

func TestConfiguredProviderNoKey(t *testing.T) {
	for _, c := range []struct{ api, reply string }{
		{ProviderAPIChat, cpChatReply}, {ProviderAPIResponses, cpResponsesReply}, {ProviderAPIAnthropic, cpAnthropicReply},
	} {
		srv, got := captureServer(t, c.reply)
		withProviders(t, ProviderDef{ID: "local", API: c.api, BaseURL: srv.URL})
		m, err := New("local/m1", nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := m.Query(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil); err != nil {
			t.Fatal(err)
		}
		if _, ok := got.headers["Authorization"]; ok {
			t.Errorf("%s: Authorization sent without api_key_env", c.api)
		}
		if _, ok := got.headers["X-Api-Key"]; ok {
			t.Errorf("%s: x-api-key sent without api_key_env", c.api)
		}
	}
}

func TestConfiguredProviderEnvErrors(t *testing.T) {
	withProviders(t, ProviderDef{ID: "gw", API: "chat", BaseURL: "http://127.0.0.1:1", APIKeyEnv: "GATEWAY_API_KEY", EnvHeaders: map[string]string{"X-T": "GW_T"}})
	t.Setenv("GATEWAY_API_KEY", "")
	t.Setenv("GW_T", "v")
	err := CheckProviderEnv("gw/m")
	var envErr *ProviderEnvError
	if !errors.As(err, &envErr) || !strings.Contains(err.Error(), "GATEWAY_API_KEY") || !strings.Contains(err.Error(), `"gw"`) {
		t.Fatalf("err = %v", err)
	}
	if _, err := New("gw/m", nil); err == nil {
		t.Error("New built a model with a missing key")
	}
	t.Setenv("GATEWAY_API_KEY", "k")
	t.Setenv("GW_T", "")
	if err := CheckProviderEnv("gw/m"); err == nil || !strings.Contains(err.Error(), "GW_T") || strings.Contains(err.Error(), "k\"") {
		t.Errorf("env_headers err = %v", err)
	}
	if err := CheckProviderEnv("openai/gpt-5"); err != nil {
		t.Errorf("built-in: %v", err)
	}
}

func TestUnknownProviderListsIDs(t *testing.T) {
	withProviders(t)
	_, err := New("nope/x", nil)
	if err == nil || err.Error() != `unknown provider "nope" (want openai, openrouter, anthropic, ollama, codex, claude-code, codex-exec, or opencode)` {
		t.Errorf("no providers: %v", err)
	}
	withProviders(t, ProviderDef{ID: "zeta", API: "chat", BaseURL: "http://x"}, ProviderDef{ID: "alpha", API: "chat", BaseURL: "http://x"})
	_, err = New("nope/x", nil)
	if err == nil || !strings.Contains(err.Error(), "openai, openrouter, anthropic, ollama, codex, claude-code, codex-exec, opencode; configured: alpha, zeta") {
		t.Errorf("with providers: %v", err)
	}
}

// TestConfiguredMetadataPrecedence covers AC7-AC9 at the model layer.
func TestConfiguredMetadataPrecedence(t *testing.T) {
	withProviders(t, ProviderDef{ID: "gw", API: "chat", BaseURL: "http://x", Models: map[string]ModelMeta{
		"priced":   {InputPricePerMTok: 4, OutputPricePerMTok: 8, CacheReadMultiplier: 0.5, ContextWindow: 12345},
		"gpt-4o":   {CacheReadMultiplier: 0.25},
		"windowed": {ContextWindow: 999},
	}})
	if Unpriced("gw/priced") || !Unpriced("gw/mystery") || Unpriced("gw/gpt-4o-mini") {
		t.Error("Unpriced wrong for configured models")
	}
	p := providerPrice("gw/priced")
	if c := cachedPriceWith("priced", p, 0, 0, 1e6, 0); c.USD != 2 {
		t.Errorf("cache read cost = %v, want 2 (0.5 x $4)", c.USD)
	}
	if p := providerPrice("gw/gpt-4o"); p == nil || p[2] != 0.25 || p[0] != Prices["gpt-4o"][0] {
		t.Errorf("multiplier-only price = %v", p)
	}
	if ContextWindow("gw/priced") != 12345 || ContextWindow("gw/windowed") != 999 || ContextWindow("gw/unknown") != 0 {
		t.Error("configured context windows")
	}
	old := PriceOverride
	t.Cleanup(func() { PriceOverride = old })
	PriceOverride = [2]float64{1, 1}
	if c := cachedPriceWith("priced", p, 1e6, 0, 0, 1e6); c.USD != 2 {
		t.Errorf("override cost = %v, want 2", c.USD)
	}
	if Unpriced("gw/mystery") {
		t.Error("override should price any configured model")
	}
}

func TestConfiguredProviderOptions(t *testing.T) {
	withProviders(t, ProviderDef{ID: "gw", API: "chat", BaseURL: "http://x"}, ProviderDef{ID: "gr", API: "responses", BaseURL: "http://x"})
	m, _ := New("gw/m", nil)
	if err := SetReasoningEffort(m, "gw/m", "high"); err != nil || m.(*OpenAI).ReasoningEffort != "high" {
		t.Errorf("chat effort: %v", err)
	}
	if err := MaxOutputTokensSupported("gw/m"); err != nil {
		t.Errorf("max output: %v", err)
	}
	r, _ := New("gr/m", nil)
	if err := SetReasoningEffort(r, "gr/m", "low"); err != nil || r.(*OpenAIResponses).ReasoningEffort != "low" {
		t.Errorf("responses effort: %v", err)
	}
	if OpenAITransport("gw/m", "responses") != "" {
		t.Error("openai_api applied to a configured provider")
	}
}

func TestConfiguredHints(t *testing.T) {
	withProviders(t, ProviderDef{ID: "gw", API: "chat", BaseURL: "http://x", APIKeyEnv: "GW_KEY"}, ProviderDef{ID: "open", API: "chat", BaseURL: "http://x"})
	if h := Hint("gw/m", "HTTP 401: bad key"); !strings.Contains(h, "GW_KEY") {
		t.Errorf("401 hint %q", h)
	}
	if h := Hint("open/m", "HTTP 403: no"); !strings.Contains(h, "no api_key_env") {
		t.Errorf("403 hint %q", h)
	}
	if h := Hint("gw/m", "HTTP 404: model not found"); !strings.Contains(h, `"m"`) {
		t.Errorf("404 hint %q", h)
	}
}

func TestInsecureBaseURL(t *testing.T) {
	for url, want := range map[string]bool{
		"http://localhost:11434": false, "http://127.0.0.1:8080": false, "http://[::1]:1": false,
		"https://gw.example.com": false, "http://gw.example.com": true, "http://10.0.0.5": true,
	} {
		if got := InsecureBaseURL(ProviderDef{BaseURL: url}); got != want {
			t.Errorf("%s: %v", url, got)
		}
	}
}
