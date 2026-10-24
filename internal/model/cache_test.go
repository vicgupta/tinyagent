package model

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// anthropicServer answers every request with resp and passes each raw request
// body to check.
func anthropicServer(t *testing.T, check func(body []byte), resp string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		check(b)
		io.WriteString(w, resp)
	}))
}

func TestAnthropicRequestsCaching(t *testing.T) {
	for _, streamed := range []bool{false, true} {
		var got map[string]any
		check := func(b []byte) { json.Unmarshal(b, &got) }
		var srv *httptest.Server
		if streamed {
			srv = sseServer(t, func(body map[string]any) { got = body }, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
		} else {
			srv = anthropicServer(t, check, `{"content":[],"usage":{}}`)
		}
		m := &Anthropic{BaseURL: srv.URL, Model: "claude-test"}
		if streamed {
			m.OnText = func(string) {}
		}
		if _, _, err := m.Query(context.Background(), []Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "hi"}}, testTools); err != nil {
			t.Fatal(err)
		}
		srv.Close()
		cc, ok := got["cache_control"].(map[string]any)
		if !ok || cc["type"] != "ephemeral" || len(cc) != 1 {
			t.Errorf("streamed=%v: cache_control = %v, want {type: ephemeral}", streamed, got["cache_control"])
		}
	}
}

func TestAnthropicPrefixStableAcrossSteps(t *testing.T) {
	var bodies []map[string]json.RawMessage
	srv := anthropicServer(t, func(b []byte) {
		var body map[string]json.RawMessage
		json.Unmarshal(b, &body)
		bodies = append(bodies, body)
	}, `{"content":[{"type":"text","text":"ok"}],"usage":{}}`)
	defer srv.Close()

	m := &Anthropic{BaseURL: srv.URL, Model: "claude-test"}
	history := []Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "task"}}
	for i := range 3 {
		if _, _, err := m.Query(context.Background(), history, testTools); err != nil {
			t.Fatal(err)
		}
		history = append(history,
			Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "c", Type: "function", Function: FunctionCall{Name: "read_file", Arguments: `{"path":"x"}`}}}},
			Message{Role: "tool", ToolCallID: "c", Content: "step " + string(rune('0'+i))})
	}
	for i := 1; i < len(bodies); i++ {
		for _, k := range []string{"tools", "system", "cache_control"} {
			if string(bodies[i][k]) != string(bodies[0][k]) {
				t.Errorf("request %d %s = %s, want %s", i, k, bodies[i][k], bodies[0][k])
			}
		}
	}
}

func TestAnthropicCacheUsage(t *testing.T) {
	// claude-sonnet-4-5: $3 in / $15 out, standard 0.1x cache reads.
	want := (100*3 + 2000*3*1.25 + 30000*3*0.1 + 50*15) / 1e6

	srv := anthropicServer(t, func([]byte) {}, `{"content":[{"type":"text","text":"ok"}],
		"usage":{"input_tokens":100,"cache_creation_input_tokens":2000,"cache_read_input_tokens":30000,"output_tokens":50}}`)
	defer srv.Close()
	_, cost, err := (&Anthropic{BaseURL: srv.URL, Model: "claude-sonnet-4-5"}).Query(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	checkCacheCost(t, "non-streamed", cost, want)

	sse := sseServer(t, func(map[string]any) {}, `event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":100,"cache_creation_input_tokens":2000,"cache_read_input_tokens":30000,"output_tokens":1}}}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":50}}

event: message_stop
data: {"type":"message_stop"}

`)
	defer sse.Close()
	_, cost, err = (&Anthropic{BaseURL: sse.URL, Model: "claude-sonnet-4-5", OnText: func(string) {}}).Query(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	checkCacheCost(t, "streamed", cost, want)
}

func checkCacheCost(t *testing.T, name string, c Cost, wantUSD float64) {
	t.Helper()
	if c.InputTokens != 32100 || c.OutputTokens != 50 || c.CacheWriteTokens != 2000 || c.CacheReadTokens != 30000 {
		t.Errorf("%s: cost = %+v, want 32,100 input (incl. 2,000 written and 30,000 read) and 50 output", name, c)
	}
	if !approx(c.USD, wantUSD) {
		t.Errorf("%s: USD = %v, want %v", name, c.USD, wantUSD)
	}
}

func TestCachedPriceMultipliers(t *testing.T) {
	cases := []struct {
		model string
		want  float64 // USD for 1M cache-read tokens
	}{
		{"claude-sonnet-4-5", 0.30}, // default 0.1x of $3
		{"claude-haiku-4-5", 0.10},  // default 0.1x of $1
		{"claude-sonnet-5-5", 0.10}, // 0.05x of $2
		{"claude-opus-5-5", 0.20},   // 0.05x of $4
		{"claude-fable-5-1", 0.25},  // 0.025x of $10
		{"unknown-model", 0},
	}
	for _, c := range cases {
		if got := cachedPrice(c.model, 0, 0, 1e6, 0); !approx(got.USD, c.want) {
			t.Errorf("%s: 1M cache reads = $%v, want $%v", c.model, got.USD, c.want)
		}
	}
	// Writes are 1.25x regardless of the read multiplier.
	if got := cachedPrice("claude-opus-5-5", 0, 1e6, 0, 0); !approx(got.USD, 5) {
		t.Errorf("opus 5.5: 1M cache writes = $%v, want $5", got.USD)
	}
}

func TestCachedPriceWithOverride(t *testing.T) {
	PriceOverride = [2]float64{10, 20}
	t.Cleanup(func() { PriceOverride = [2]float64{} })

	if got := cachedPrice("unknown-model", 1e6, 1e6, 1e6, 1e6); !approx(got.USD, 10+12.5+1+20) {
		t.Errorf("override, default multiplier: $%v, want $43.5", got.USD)
	}
	// The table's multiplier still applies on top of an overridden base price.
	if got := cachedPrice("claude-sonnet-5-5", 0, 0, 1e6, 0); !approx(got.USD, 0.5) {
		t.Errorf("override, 0.05x model: $%v, want $0.5", got.USD)
	}
}

func TestOpenAIRequestHasNoCacheControl(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{}}`)
	}))
	defer srv.Close()
	if _, _, err := (&OpenAI{BaseURL: srv.URL, Model: "gpt-4o"}).Query(context.Background(), []Message{{Role: "user", Content: "hi"}}, testTools); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["cache_control"]; ok {
		t.Errorf("openai request has cache_control: %v", got)
	}
}
