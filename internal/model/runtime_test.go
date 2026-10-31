package model

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// fastRetryRuntime is an isolated runtime with a 1ms retry base, so retry
// tests run quickly without touching DefaultRuntime.
func fastRetryRuntime() *Runtime {
	rt := NewRuntime()
	rt.RetryBase = time.Millisecond
	return rt
}

// TestRuntimeDefaults pins today's production behavior: a nil runtime and
// NewRuntime both mean a 10-minute HTTP timeout, a 1s retry base, 6
// attempts, the built-in price table and no override.
func TestRuntimeDefaults(t *testing.T) {
	t.Parallel()
	for name, rt := range map[string]*Runtime{"nil": nil, "NewRuntime": NewRuntime(), "zero": {}} {
		if got := rt.httpClient().Timeout; got != 10*time.Minute {
			t.Errorf("%s: HTTP timeout = %v, want 10m", name, got)
		}
		if got := rt.retryBase(); got != time.Second {
			t.Errorf("%s: retry base = %v, want 1s", name, got)
		}
		if got := rt.maxAttempts(); got != 6 {
			t.Errorf("%s: max attempts = %d, want 6", name, got)
		}
		if got := rt.prices(); len(got) != len(builtinPrices) || got["gpt-4o"] != [3]float64{2.50, 10.00} {
			t.Errorf("%s: prices = %v", name, got)
		}
	}
	if (*Runtime)(nil).priceOverride() != DefaultRuntime.PriceOverride {
		t.Error("nil runtime should read DefaultRuntime's override")
	}
	if NewRuntime().PriceOverride != [2]float64{} {
		t.Error("NewRuntime should have no price override")
	}
}

// TestNewWithRuntime checks that NewWith hands Options.Runtime to every
// HTTP adapter, and that nil leaves the adapter on DefaultRuntime.
func TestNewWithRuntime(t *testing.T) {
	rt := NewRuntime()
	for _, spec := range []string{"openai/gpt-4o", "openrouter/x/y", "anthropic/claude-sonnet-4", "ollama/llama3"} {
		m, err := NewWith(spec, nil, Options{OpenAIAPI: "chat", Runtime: rt})
		if err != nil {
			t.Fatalf("%s: %v", spec, err)
		}
		var got *Runtime
		switch a := m.(type) {
		case *OpenAI:
			got = a.Runtime
		case *OpenAIResponses:
			got = a.Runtime
		case *Anthropic:
			got = a.Runtime
		}
		if got != rt {
			t.Errorf("%s: adapter runtime = %p, want %p", spec, got, rt)
		}
		m, err = NewWith(spec, nil, Options{OpenAIAPI: "chat"})
		if err != nil {
			t.Fatal(err)
		}
		if a, ok := m.(*OpenAI); ok && a.Runtime != nil {
			t.Errorf("%s: nil Options.Runtime should leave the adapter on the default", spec)
		}
	}
}

// TestRuntimeIsolation shows two runtimes with different retry and pricing
// settings working side by side, which package globals could not do.
func TestRuntimeIsolation(t *testing.T) {
	t.Parallel()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		io.WriteString(w, "down")
	}))
	defer srv.Close()
	rt := &Runtime{RetryBase: time.Millisecond, MaxAttempts: 2, HTTP: srv.Client()}
	var out map[string]any
	err := rt.postJSON(context.Background(), srv.URL, nil, map[string]any{}, &out)
	if re, ok := err.(*RetryExhaustedError); !ok || re.Attempts != 2 || n.Load() != 2 {
		t.Fatalf("err = %v, requests = %d, want 2 attempts", err, n.Load())
	}

	cheap := &Runtime{Prices: map[string][3]float64{"m": {1, 1}}}
	pricey := &Runtime{Prices: map[string][3]float64{"m": {1, 1}}, PriceOverride: [2]float64{100, 100}}
	if c := cheap.price("m-1", 1e6, 0); c.USD != 1 {
		t.Errorf("cheap = %v, want 1", c.USD)
	}
	if c := pricey.price("m-1", 1e6, 0); c.USD != 100 {
		t.Errorf("pricey = %v, want 100", c.USD)
	}
	if !(&Runtime{Prices: map[string][3]float64{}}).Unpriced("openai/gpt-4o") {
		t.Error("an empty price table should leave gpt-4o unpriced")
	}
}
