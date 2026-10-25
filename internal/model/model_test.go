package model

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var testTools = []ToolSpec{{Type: "function", Function: FunctionSpec{
	Name: "read_file", Description: "Read", Parameters: map[string]any{"type": "object"},
}}}

func TestNewResolvesProviders(t *testing.T) {
	cases := map[string]string{
		"gpt-4o":                        "*model.OpenAI",
		"openai/gpt-4o":                 "*model.OpenAI",
		"openrouter/anthropic/claude-x": "*model.OpenAI",
		"anthropic/claude-sonnet-4-5":   "*model.Anthropic",
		"ollama/llama3.1":               "*model.OpenAI",
	}
	for spec, want := range cases {
		m, err := New(spec, nil)
		if err != nil {
			t.Fatalf("New(%q): %v", spec, err)
		}
		if got := typeName(m); got != want {
			t.Errorf("New(%q) = %s, want %s", spec, got, want)
		}
	}
	if m, _ := New("openrouter/anthropic/claude-x", nil); m.(*OpenAI).Model != "anthropic/claude-x" {
		t.Errorf("openrouter model name = %q", m.(*OpenAI).Model)
	}
	if _, err := New("bogus/x", nil); err == nil {
		t.Error("unknown provider accepted")
	}
}

func typeName(v any) string {
	switch v.(type) {
	case *OpenAI:
		return "*model.OpenAI"
	case *Anthropic:
		return "*model.Anthropic"
	}
	return "?"
}

func TestOpenAIQueryWithRetry(t *testing.T) {
	retryBase = time.Millisecond
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		if r.Header.Get("Authorization") != "Bearer k" || r.URL.Path != "/v1/chat/completions" {
			t.Errorf("bad request: %s %v", r.URL.Path, r.Header)
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["model"] != "gpt-4o" || body["tools"] == nil {
			t.Errorf("body = %v", body)
		}
		io.WriteString(w, `{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","content":null,
			"tool_calls":[{"id":"c1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"a\"}"}}]}}],
			"usage":{"prompt_tokens":1000000,"completion_tokens":0}}`)
	}))
	defer srv.Close()

	m := &OpenAI{BaseURL: srv.URL + "/v1", APIKey: "k", Model: "gpt-4o"}
	msg, cost, err := m.Query(context.Background(), []Message{{Role: "user", Content: "hi"}}, testTools)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Errorf("calls = %d, want 2 (one retry)", calls.Load())
	}
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].Function.Arguments != `{"path":"a"}` {
		t.Errorf("msg = %+v", msg)
	}
	if cost.USD != 2.5 {
		t.Errorf("cost = %v, want 2.5", cost.USD)
	}
}

func TestNoRetryOn400(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "bad", http.StatusBadRequest)
	}))
	defer srv.Close()
	m := &OpenAI{BaseURL: srv.URL, Model: "x"}
	if _, _, err := m.Query(context.Background(), nil, nil); err == nil {
		t.Fatal("want error")
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d, want 1", calls.Load())
	}
}

func TestToAnthropic(t *testing.T) {
	system, msgs := toAnthropic([]Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "task"},
		{Role: "assistant", Content: "looking", ToolCalls: []ToolCall{
			{ID: "t1", Function: FunctionCall{Name: "read_file", Arguments: `{"path":"a"}`}},
			{ID: "t2", Function: FunctionCall{Name: "bash", Arguments: ``}},
		}},
		{Role: "tool", ToolCallID: "t1", Content: "A"},
		{Role: "tool", ToolCallID: "t2", Content: "B"},
	})
	if system != "sys" {
		t.Errorf("system = %q", system)
	}
	if len(msgs) != 3 {
		t.Fatalf("got %d messages, want 3 (tool results merged): %+v", len(msgs), msgs)
	}
	asst := msgs[1]
	if asst.Role != "assistant" || len(asst.Content) != 3 || block(t, asst.Content[2]).Type != "tool_use" || string(block(t, asst.Content[2]).Input) != "{}" {
		t.Errorf("assistant = %+v", asst)
	}
	res := msgs[2]
	if res.Role != "user" || len(res.Content) != 2 || block(t, res.Content[1]).ToolUseID != "t2" || block(t, res.Content[1]).Content != "B" {
		t.Errorf("tool results = %+v", res)
	}
}

func TestAnthropicQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "k" || r.Header.Get("anthropic-version") == "" {
			t.Errorf("bad request: %s %v", r.URL.Path, r.Header)
		}
		var body struct {
			System string           `json:"system"`
			Tools  []anthropicTool  `json:"tools"`
			Msgs   []map[string]any `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		if body.System != "sys" || len(body.Tools) != 1 || body.Tools[0].InputSchema == nil {
			t.Errorf("body = %+v", body)
		}
		io.WriteString(w, `{"stop_reason":"tool_use","content":[
			{"type":"text","text":"ok"},
			{"type":"tool_use","id":"tu1","name":"read_file","input":{"path":"x"}}],
			"usage":{"input_tokens":10,"output_tokens":5}}`)
	}))
	defer srv.Close()

	m := &Anthropic{BaseURL: srv.URL, APIKey: "k", Model: "claude-test"}
	msg, cost, err := m.Query(context.Background(), []Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "hi"}}, testTools)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Content != "ok" || len(msg.ToolCalls) != 1 || msg.ToolCalls[0].ID != "tu1" || msg.ToolCalls[0].Function.Arguments != `{"path":"x"}` {
		t.Errorf("msg = %+v", msg)
	}
	if cost.InputTokens != 10 || cost.OutputTokens != 5 {
		t.Errorf("cost = %+v", cost)
	}
}

func TestPricePrefix(t *testing.T) {
	if c := price("gpt-4o-mini-2024-07-18", 1e6, 0); c.USD != 0.15 {
		t.Errorf("gpt-4o-mini price = %v (longest prefix should win)", c.USD)
	}
	if c := price("unknown", 1e6, 1e6); c.USD != 0 {
		t.Errorf("unknown price = %v", c.USD)
	}
}

func TestOllamaBaseURL(t *testing.T) {
	cases := map[string]string{
		"":                        "http://127.0.0.1:11434",
		"0.0.0.0":                 "http://127.0.0.1:11434",
		"0.0.0.0:11434":           "http://127.0.0.1:11434",
		"[::]:9000":               "http://127.0.0.1:9000",
		"localhost":               "http://localhost:11434",
		"http://gpu-box:8080":     "http://gpu-box:8080",
		"https://ollama.example/": "https://ollama.example:11434",
	}
	for in, want := range cases {
		if got := ollamaBaseURL(in); got != want {
			t.Errorf("ollamaBaseURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func sseServer(t *testing.T, check func(body map[string]any), events string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		check(body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, events)
	}))
}

func TestOpenAIStream(t *testing.T) {
	srv := sseServer(t, func(body map[string]any) {
		if body["stream"] != true {
			t.Errorf("stream not requested: %v", body)
		}
	}, `data: {"choices":[{"delta":{"role":"assistant","content":"Hel"}}]}

data: {"choices":[{"delta":{"content":"lo"}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"read_file","arguments":""}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"path\":"}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":1,"id":"c2","function":{"name":"bash","arguments":"{}"}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"a\"}"}}]},"finish_reason":"tool_calls"}]}

data: {"choices":[],"usage":{"prompt_tokens":1000000,"completion_tokens":0}}

data: [DONE]

`)
	defer srv.Close()

	var streamed []string
	m := &OpenAI{BaseURL: srv.URL, Model: "gpt-4o", OnText: func(s string) { streamed = append(streamed, s) }}
	msg, cost, err := m.Query(context.Background(), []Message{{Role: "user", Content: "hi"}}, testTools)
	if err != nil {
		t.Fatal(err)
	}
	if len(streamed) != 2 || msg.Content != "Hello" {
		t.Errorf("streamed %q, content %q", streamed, msg.Content)
	}
	if len(msg.ToolCalls) != 2 || msg.ToolCalls[0].Function.Arguments != `{"path":"a"}` ||
		msg.ToolCalls[1].Function.Name != "bash" || msg.ToolCalls[1].Type != "function" {
		t.Errorf("tool calls = %+v", msg.ToolCalls)
	}
	if cost.USD != 2.5 || msg.Extra["finish_reason"] != "tool_calls" {
		t.Errorf("cost %v, extra %v", cost.USD, msg.Extra)
	}
}

func TestOpenAIStreamError(t *testing.T) {
	srv := sseServer(t, func(map[string]any) {}, "data: {\"error\":{\"message\":\"boom\"}}\n\n")
	defer srv.Close()
	m := &OpenAI{BaseURL: srv.URL, Model: "x", OnText: func(string) {}}
	if _, _, err := m.Query(context.Background(), nil, nil); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v", err)
	}
}

func TestAnthropicStream(t *testing.T) {
	srv := sseServer(t, func(body map[string]any) {
		if body["stream"] != true {
			t.Errorf("stream not requested: %v", body)
		}
	}, `event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":10,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: ping
data: {"type":"ping"}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Look"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ing"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tu1","name":"read_file","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"x\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: content_block_start
data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"tu2","name":"list_files","input":{}}}

event: content_block_stop
data: {"type":"content_block_stop","index":2}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":42}}

event: message_stop
data: {"type":"message_stop"}

`)
	defer srv.Close()

	var streamed strings.Builder
	m := &Anthropic{BaseURL: srv.URL, Model: "claude-test", OnText: func(s string) { streamed.WriteString(s) }}
	msg, cost, err := m.Query(context.Background(), []Message{{Role: "user", Content: "hi"}}, testTools)
	if err != nil {
		t.Fatal(err)
	}
	if streamed.String() != "Looking" || msg.Content != "Looking" {
		t.Errorf("streamed %q, content %q", streamed.String(), msg.Content)
	}
	if len(msg.ToolCalls) != 2 || msg.ToolCalls[0].Function.Arguments != `{"path":"x"}` || msg.ToolCalls[1].Function.Arguments != "{}" {
		t.Errorf("tool calls = %+v", msg.ToolCalls)
	}
	if cost.InputTokens != 10 || cost.OutputTokens != 42 || msg.Extra["stop_reason"] != "tool_use" {
		t.Errorf("cost %+v, extra %v", cost, msg.Extra)
	}
}

func TestAnthropicStreamError(t *testing.T) {
	srv := sseServer(t, func(map[string]any) {}, "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n")
	defer srv.Close()
	m := &Anthropic{BaseURL: srv.URL, Model: "x", OnText: func(string) {}}
	if _, _, err := m.Query(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil); err == nil || !strings.Contains(err.Error(), "Overloaded") {
		t.Errorf("err = %v", err)
	}
}

func TestUnpriced(t *testing.T) {
	cases := map[string]bool{
		"openai/gpt-4o":                   false,
		"gpt-4o-mini-2024-07-18":          false,
		"anthropic/claude-sonnet-4-5":     false,
		"openai/unlisted-model":           true,
		"unlisted-model":                  true,
		"openrouter/meta-llama/llama-3.1": true,
		"anthropic/claude-2":              true,
		"ollama/llama3.1":                 false,
		"codex/gpt-5":                     false,
		"nope/model":                      false,
	}
	for spec, want := range cases {
		if got := Unpriced(spec); got != want {
			t.Errorf("Unpriced(%q) = %v, want %v", spec, got, want)
		}
	}
	t.Cleanup(func() { PriceOverride = [2]float64{} })
	for _, o := range [][2]float64{{1, 2}, {1, 0}, {0, 2}} {
		PriceOverride = o
		if Unpriced("openai/unlisted-model") {
			t.Errorf("override %v: want priced", o)
		}
	}
}

func block(t *testing.T, raw json.RawMessage) anthropicBlock {
	t.Helper()
	var b anthropicBlock
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	return b
}
