package model

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fakeJWT(claims map[string]any) string {
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	return enc(map[string]string{"alg": "none"}) + "." + enc(claims) + ".sig"
}

// writeCodexAuth writes an auth.json like `codex login` does and returns its path.
func writeCodexAuth(t *testing.T, access string) string {
	path := filepath.Join(t.TempDir(), "auth.json")
	auth := map[string]any{
		"auth_mode":      "chatgpt",
		"OPENAI_API_KEY": nil,
		"tokens": map[string]any{
			"access_token":  access,
			"refresh_token": "r1",
			"id_token":      fakeJWT(map[string]any{"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct"}}),
		},
		"last_refresh": "2026-01-01T00:00:00Z",
	}
	data, _ := json.Marshal(auth)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const codexEvents = `event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"Hel"}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"lo"}

event: response.output_item.done
data: {"type":"response.output_item.done","item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello"}]}}

event: response.output_item.done
data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","name":"read_file","arguments":"{\"path\":\"a\"}"}}

event: response.completed
data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":12,"output_tokens":5}}}

`

func TestToResponses(t *testing.T) {
	instr, input := toResponses([]Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "ok", ToolCalls: []ToolCall{{ID: "c1", Function: FunctionCall{Name: "read_file", Arguments: "{}"}}}},
		{Role: "tool", ToolCallID: "c1", Content: "data"},
	})
	if instr != "sys" {
		t.Errorf("instructions = %q", instr)
	}
	got, _ := json.Marshal(input)
	want := `[{"content":[{"text":"hi","type":"input_text"}],"role":"user","type":"message"},` +
		`{"content":[{"text":"ok","type":"output_text"}],"role":"assistant","type":"message"},` +
		`{"arguments":"{}","call_id":"c1","name":"read_file","type":"function_call"},` +
		`{"call_id":"c1","output":"data","type":"function_call_output"}]`
	if string(got) != want {
		t.Errorf("input =\n%s\nwant\n%s", got, want)
	}
}

func TestCodexQuery(t *testing.T) {
	access := fakeJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" || r.Header.Get("Authorization") != "Bearer "+access ||
			r.Header.Get("chatgpt-account-id") != "acct" || r.Header.Get("session_id") == "" {
			t.Errorf("bad request: %s %v", r.URL.Path, r.Header)
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["model"] != "gpt-5.5" || body["instructions"] != "sys" || body["stream"] != true || body["store"] != false {
			t.Errorf("body = %v", body)
		}
		if tools, _ := body["tools"].([]any); len(tools) != 1 || tools[0].(map[string]any)["name"] != "read_file" {
			t.Errorf("tools = %v", body["tools"])
		}
		io.WriteString(w, codexEvents)
	}))
	defer srv.Close()

	var streamed strings.Builder
	c := &Codex{BaseURL: srv.URL, AuthFile: writeCodexAuth(t, access), Model: "gpt-5.5", OnText: func(s string) { streamed.WriteString(s) }}
	msg, cost, err := c.Query(context.Background(), []Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "hi"}}, testTools)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Content != "Hello" || streamed.String() != "Hello" {
		t.Errorf("content = %q, streamed = %q", msg.Content, streamed.String())
	}
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].ID != "call_1" || msg.ToolCalls[0].Function.Arguments != `{"path":"a"}` {
		t.Errorf("tool calls = %+v", msg.ToolCalls)
	}
	if cost.USD != 0 || cost.InputTokens != 12 || cost.OutputTokens != 5 {
		t.Errorf("cost = %+v", cost)
	}
}

// An expired token is refreshed before the request, and the new tokens are
// saved without losing other fields in auth.json.
func TestCodexRefreshesExpiredToken(t *testing.T) {
	fresh := fakeJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if body["grant_type"] != "refresh_token" || body["refresh_token"] != "r1" || body["client_id"] != codexClientID {
			t.Errorf("refresh body = %v", body)
		}
		fmt.Fprintf(w, `{"access_token":%q,"refresh_token":"r2","id_token":"i2"}`, fresh)
	})
	mux.HandleFunc("/responses", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+fresh {
			t.Errorf("used stale token: %s", r.Header.Get("Authorization"))
		}
		io.WriteString(w, codexEvents)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	expired := fakeJWT(map[string]any{"exp": time.Now().Add(-time.Hour).Unix()})
	path := writeCodexAuth(t, expired)
	c := &Codex{BaseURL: srv.URL, TokenURL: srv.URL + "/oauth/token", AuthFile: path, Model: "m"}
	if _, _, err := c.Query(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil); err != nil {
		t.Fatal(err)
	}

	var saved map[string]any
	data, _ := os.ReadFile(path)
	json.Unmarshal(data, &saved)
	tokens := saved["tokens"].(map[string]any)
	if tokens["access_token"] != fresh || tokens["refresh_token"] != "r2" || tokens["id_token"] != "i2" {
		t.Errorf("saved tokens = %v", tokens)
	}
	if saved["auth_mode"] != "chatgpt" || saved["last_refresh"] == "2026-01-01T00:00:00Z" {
		t.Errorf("saved = %v", saved)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v", fi.Mode())
	}
}

// A 401 with an unexpired-looking token triggers one refresh and a retry.
func TestCodexRefreshesOn401(t *testing.T) {
	var calls int
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth/token", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"access_token":"new"}`)
	})
	mux.HandleFunc("/responses", func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer new" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		io.WriteString(w, codexEvents)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := &Codex{BaseURL: srv.URL, TokenURL: srv.URL + "/oauth/token", AuthFile: writeCodexAuth(t, "opaque"), Model: "m"}
	if _, _, err := c.Query(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("calls = %d, want 2", calls)
	}
}

func TestCodexStreamFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"usage limit reached\"}}}\n\n")
	}))
	defer srv.Close()
	c := &Codex{BaseURL: srv.URL, AuthFile: writeCodexAuth(t, "opaque"), Model: "m"}
	_, _, err := c.Query(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "usage limit reached") {
		t.Errorf("err = %v", err)
	}
}

func TestCodexMissingLogin(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	if _, err := New("codex/gpt-5.5", nil); err == nil || !strings.Contains(err.Error(), "codex login") {
		t.Errorf("err = %v", err)
	}
}
