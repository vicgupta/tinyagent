package model

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// stubModel returns its errors in turn, then a reply naming itself.
type stubModel struct {
	name  string
	errs  []error
	calls int
	seen  [][]Message
}

func (s *stubModel) Query(_ context.Context, msgs []Message, _ []ToolSpec) (Message, Cost, error) {
	s.calls++
	s.seen = append(s.seen, msgs)
	if len(s.errs) > 0 {
		err := s.errs[0]
		s.errs = s.errs[1:]
		return Message{}, Cost{}, err
	}
	return Message{Role: "assistant", Content: "from " + s.name}, Cost{USD: 0.01}, nil
}

func outage(reason string) error {
	return &RetryExhaustedError{Attempts: 6, Reason: reason, Err: errors.New("HTTP 503: overloaded")}
}

func TestPostReturnsRetryExhausted(t *testing.T) {
	defer func(b time.Duration) { retryBase = b }(retryBase)
	retryBase = time.Millisecond
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		io.WriteString(w, "overloaded")
	}))
	defer srv.Close()
	var out map[string]any
	err := postJSON(context.Background(), srv.URL, nil, map[string]any{}, &out)
	var re *RetryExhaustedError
	if !errors.As(err, &re) || re.Reason != "http_503" || re.Attempts != maxAttempts || int(n.Load()) != maxAttempts {
		t.Fatalf("err = %v (%#v), %d requests", err, re, n.Load())
	}
	if !strings.HasPrefix(err.Error(), "giving up after 6 attempts: HTTP 503") {
		t.Errorf("message changed: %q", err)
	}
}

func TestPostNonRetryableIsNotExhausted(t *testing.T) {
	for _, code := range []int{400, 401, 403, 404} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
			io.WriteString(w, "nope")
		}))
		var out map[string]any
		err := postJSON(context.Background(), srv.URL, nil, map[string]any{}, &out)
		srv.Close()
		if err == nil || IsRetryExhausted(err) {
			t.Errorf("HTTP %d: err = %v, retry-exhausted = %v", code, err, IsRetryExhausted(err))
		}
	}
}

func TestFallbackSwitchesAndStays(t *testing.T) {
	a := &stubModel{name: "a", errs: []error{outage("http_503")}}
	b := &stubModel{name: "b"}
	f := NewFallback(FallbackEntry{"openai/a", a}, FallbackEntry{"anthropic/b", b})
	var got []Switch
	ctx := WithFallbackObserver(context.Background(), func(s Switch) { got = append(got, s) })
	msg, _, err := f.Query(ctx, []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil || msg.Content != "from b" || msg.Extra[ExtraModel] != "anthropic/b" {
		t.Fatalf("msg = %+v, err = %v", msg, err)
	}
	if len(got) != 1 || got[0] != (Switch{From: "openai/a", To: "anthropic/b", Reason: "http_503", Attempts: 6}) {
		t.Errorf("switches = %+v", got)
	}
	if f.Active() != "anthropic/b" {
		t.Errorf("active = %s", f.Active())
	}
	// Later requests stay on the fallback.
	if msg, _, _ := f.Query(ctx, nil, nil); msg.Content != "from b" || a.calls != 1 {
		t.Errorf("second query: %+v, primary calls %d", msg, a.calls)
	}
}

func TestFallbackChainWalkAndExhaustion(t *testing.T) {
	a := &stubModel{name: "a", errs: []error{outage("http_503")}}
	b := &stubModel{name: "b", errs: []error{outage("network")}}
	c := &stubModel{name: "c"}
	f := NewFallback(FallbackEntry{"openai/a", a}, FallbackEntry{"anthropic/b", b}, FallbackEntry{"ollama/c", c})
	if msg, _, err := f.Query(context.Background(), nil, nil); err != nil || msg.Content != "from c" {
		t.Fatalf("msg = %+v, err = %v", msg, err)
	}

	a = &stubModel{name: "a", errs: []error{outage("http_503")}}
	b = &stubModel{name: "b", errs: []error{outage("http_429")}}
	f = NewFallback(FallbackEntry{"openai/a", a}, FallbackEntry{"anthropic/b", b})
	_, _, err := f.Query(context.Background(), nil, nil)
	if err == nil || !strings.Contains(err.Error(), "openai/a (http_503)") || !strings.Contains(err.Error(), "anthropic/b (http_429)") || !IsRetryExhausted(err) {
		t.Errorf("exhausted err = %v", err)
	}
}

func TestFallbackNonRetryableErrors(t *testing.T) {
	auth := errors.New("HTTP 401: bad key")
	a := &stubModel{name: "a", errs: []error{auth}}
	b := &stubModel{name: "b"}
	f := NewFallback(FallbackEntry{"openai/a", a}, FallbackEntry{"anthropic/b", b})
	if _, _, err := f.Query(context.Background(), nil, nil); err != auth || b.calls != 0 {
		t.Errorf("primary 4xx: err = %v, fallback calls %d", err, b.calls)
	}
	overflow := errors.New("HTTP 400: prompt is too long")
	a = &stubModel{name: "a", errs: []error{overflow}}
	f = NewFallback(FallbackEntry{"openai/a", a}, FallbackEntry{"anthropic/b", b})
	if _, _, err := f.Query(context.Background(), nil, nil); !IsContextOverflow(err) || b.calls != 0 {
		t.Errorf("overflow: err = %v, fallback calls %d", err, b.calls)
	}
	// A fallback's own non-retryable error names it.
	a = &stubModel{name: "a", errs: []error{outage("http_503")}}
	b = &stubModel{name: "b", errs: []error{auth}}
	f = NewFallback(FallbackEntry{"openai/a", a}, FallbackEntry{"anthropic/b", b})
	if _, _, err := f.Query(context.Background(), nil, nil); err == nil || !strings.HasPrefix(err.Error(), "model anthropic/b: HTTP 401") || !errors.Is(err, auth) {
		t.Errorf("fallback 4xx: err = %v", err)
	}
}

func TestFallbackNotOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a := &stubModel{name: "a", errs: []error{outage("network")}}
	b := &stubModel{name: "b"}
	f := NewFallback(FallbackEntry{"openai/a", a}, FallbackEntry{"anthropic/b", b})
	if _, _, err := f.Query(ctx, nil, nil); err == nil || b.calls != 0 || f.Active() != "openai/a" {
		t.Errorf("cancelled: err = %v, fallback calls %d", err, b.calls)
	}
}

func TestForModelStripsForeignReasoning(t *testing.T) {
	msgs := []Message{
		{Role: "user", Content: "q"},
		{Role: "assistant", Content: "untagged", Extra: map[string]any{ExtraAnthropicContent: []any{"t"}}},
		{Role: "assistant", Content: "codex", Extra: map[string]any{ExtraModel: "codex/x", ExtraCodexReasoning: []any{"r"}}},
		{Role: "assistant", Content: "plain", Extra: map[string]any{ExtraModel: "openai/y"}},
	}
	got := ForModel(msgs, "openai/y", "anthropic/p")
	if _, ok := got[1].Extra[ExtraAnthropicContent]; ok {
		t.Error("anthropic thinking sent to openai")
	}
	if _, ok := got[2].Extra[ExtraCodexReasoning]; ok {
		t.Error("codex reasoning sent to openai")
	}
	if got[2].Extra[ExtraModel] != "codex/x" {
		t.Error("model tag dropped")
	}
	// The input (the trajectory) keeps its state.
	if _, ok := msgs[1].Extra[ExtraAnthropicContent]; !ok {
		t.Error("input modified")
	}
	// The same provider keeps its own state; nothing foreign means no copy.
	same := ForModel(msgs, "anthropic/other", "anthropic/p")
	if _, ok := same[1].Extra[ExtraAnthropicContent]; !ok {
		t.Error("same-provider thinking dropped")
	}
	if clean := msgs[3:]; &ForModel(clean, "anthropic/z", "openai/y")[0] != &clean[0] {
		t.Error("copied without foreign state")
	}
}

// TestFallbackSendsNoForeignReasoning sends a history holding Codex
// reasoning items to an OpenAI Responses fallback served locally.
func TestFallbackSendsNoForeignReasoning(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&body)
		io.WriteString(w, `{"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer srv.Close()
	primary := &stubModel{name: "codex", errs: []error{outage("http_502")}}
	fb := &OpenAIResponses{BaseURL: srv.URL, APIKey: "k", Model: "gpt-x"}
	f := NewFallback(FallbackEntry{"codex/c", primary}, FallbackEntry{"openai/gpt-x", fb})
	history := []Message{
		{Role: "system", Content: "s"},
		{Role: "user", Content: "q"},
		{Role: "assistant", Content: "a", Extra: map[string]any{ExtraCodexReasoning: []any{map[string]any{"type": "reasoning", "encrypted_content": "SECRET"}}}},
		{Role: "user", Content: "more"},
	}
	msg, _, err := f.Query(context.Background(), history, nil)
	if err != nil || msg.Content != "ok" {
		t.Fatalf("msg = %+v, err = %v", msg, err)
	}
	b, _ := json.Marshal(body)
	if strings.Contains(string(b), "SECRET") || strings.Contains(string(b), `"reasoning"`) {
		t.Errorf("foreign reasoning sent: %s", b)
	}
	if _, ok := history[2].Extra[ExtraCodexReasoning]; !ok {
		t.Error("history lost its reasoning state")
	}
}
