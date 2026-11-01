package model

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestResolveOpenAIAPI(t *testing.T) {
	for _, c := range []struct{ setting, base, want string }{
		{"", "", "responses"},
		{"auto", "", "responses"},
		{"auto", "https://api.openai.com/v1", "responses"},
		{"auto", "https://api.openai.com/v1/", "responses"},
		{"auto", "http://api.openai.com/v1", "chat"},
		{"auto", "http://localhost:8000/v1", "chat"},
		{"", "https://example.com/v1", "chat"},
		{"responses", "http://localhost:8000/v1", "responses"},
		{"chat", "", "chat"},
	} {
		if got := ResolveOpenAIAPI(c.setting, c.base); got != c.want {
			t.Errorf("ResolveOpenAIAPI(%q, %q) = %q, want %q", c.setting, c.base, got, c.want)
		}
	}
	for _, v := range []string{"", "auto", "responses", "chat"} {
		if !ValidOpenAIAPI(v) {
			t.Errorf("%q rejected", v)
		}
	}
	if ValidOpenAIAPI("completions") {
		t.Error("completions accepted")
	}
}

// TestNewOpenAITransport covers auto resolution (AC1, AC5), explicit
// overrides (AC6), and other providers ignoring the setting (AC7).
func TestNewOpenAITransport(t *testing.T) {
	for _, c := range []struct {
		spec, base, setting, want string
	}{
		{"openai/gpt-5", "", "", "*model.OpenAIResponses"},
		{"gpt-5", "https://api.openai.com/v1/", "auto", "*model.OpenAIResponses"},
		{"openai/gpt-5", "http://localhost:9/v1", "", "*model.OpenAI"},
		{"openai/gpt-5", "", "chat", "*model.OpenAI"},
		{"openai/gpt-5", "http://localhost:9/v1", "responses", "*model.OpenAIResponses"},
		{"openrouter/openai/gpt-5", "", "responses", "*model.OpenAI"},
		{"ollama/llama3.1", "", "responses", "*model.OpenAI"},
	} {
		t.Setenv("OPENAI_BASE_URL", c.base)
		m, err := NewWith(c.spec, nil, Options{OpenAIAPI: c.setting})
		if err != nil {
			t.Fatal(err)
		}
		if got := typeName(m); got != c.want {
			t.Errorf("%s base=%q openai_api=%q: %s, want %s", c.spec, c.base, c.setting, got, c.want)
		}
	}
	t.Setenv("OPENAI_BASE_URL", "http://localhost:9/v1")
	if m, _ := NewWith("openai/gpt-5", nil, Options{OpenAIAPI: "responses"}); m.(*OpenAIResponses).BaseURL != "http://localhost:9/v1" {
		t.Errorf("base = %q", m.(*OpenAIResponses).BaseURL)
	}
	if _, err := NewWith("openai/gpt-5", nil, Options{OpenAIAPI: "bogus"}); err == nil || !strings.Contains(err.Error(), "auto, responses, chat") {
		t.Errorf("err = %v", err)
	}
	if got := OpenAITransport("anthropic/claude-x", "responses"); got != "" {
		t.Errorf("anthropic transport = %q", got)
	}
}

const responsesReasoningEvents = `event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"Look"}

event: response.output_text.delta
data: {"type":"response.output_text.delta","delta":"ing"}

event: response.output_item.done
data: {"type":"response.output_item.done","item":{"type":"reasoning","id":"rs_1","summary":[],"encrypted_content":"ENC1"}}

event: response.output_item.done
data: {"type":"response.output_item.done","item":{"type":"reasoning","id":"rs_2","summary":[]}}

event: response.output_item.done
data: {"type":"response.output_item.done","item":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Looking"}]}}

event: response.output_item.done
data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"call_1","name":"read_file","arguments":"{\"path\":\"a\"}"}}

event: response.completed
data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":1000,"output_tokens":100,"input_tokens_details":{"cached_tokens":800}}}}

`

const responsesFinalJSON = `{"status":"completed","output":[{"type":"reasoning","id":"rs_3","encrypted_content":"ENC3"},{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Done."}]}],"usage":{"input_tokens":10,"output_tokens":2}}`

type responsesServer struct {
	paths  []string
	auth   []string
	bodies []map[string]any
	reply  func(n int) string
}

func (s *responsesServer) start(t *testing.T) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		s.paths = append(s.paths, r.URL.Path)
		s.auth = append(s.auth, r.Header.Get("Authorization"))
		s.bodies = append(s.bodies, b)
		io.WriteString(w, s.reply(len(s.bodies)))
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/v1"
}

// TestOpenAIResponsesToolLoop covers the request shape (AC1, AC2), tool call
// replay (AC3), reasoning replay (AC4), streaming (AC8), and cached-token
// pricing (AC9).
func TestOpenAIResponsesToolLoop(t *testing.T) {
	s := &responsesServer{reply: func(n int) string {
		if n == 1 {
			return responsesReasoningEvents
		}
		return `event: response.completed
data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":5,"output_tokens":1}}}

`
	}}
	var streamed strings.Builder
	o := &OpenAIResponses{BaseURL: s.start(t), APIKey: "sk", Model: "gpt-4.1", OnText: func(x string) { streamed.WriteString(x) }}
	if err := SetReasoningEffort(o, "openai/gpt-4.1", "high"); err != nil {
		t.Fatal(err)
	}
	msgs := []Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "hi"}}
	msg, cost, err := o.Query(context.Background(), msgs, testTools)
	if err != nil {
		t.Fatal(err)
	}
	if streamed.String() != "Looking" || msg.Content != "Looking" {
		t.Errorf("streamed %q, content %q", streamed.String(), msg.Content)
	}
	if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].ID != "call_1" || msg.ToolCalls[0].Function.Name != "read_file" {
		t.Fatalf("tool calls = %+v", msg.ToolCalls)
	}
	// 200 uncached + 800 cached at the cache-read price, 100 output.
	p := builtinPrices["gpt-4.1"]
	read := p[2]
	if read == 0 {
		read = cacheReadMultiplier
	}
	wantUSD := ((200+800*read)*p[0] + 100*p[1]) / 1e6
	if math.Abs(cost.USD-wantUSD) > 1e-12 || cost.InputTokens != 1000 || cost.OutputTokens != 100 {
		t.Errorf("cost = %+v, want USD %v", cost, wantUSD)
	}
	if cost.CacheReadTokens != 800 {
		t.Errorf("cache read tokens = %d", cost.CacheReadTokens)
	}

	body := s.bodies[0]
	if s.paths[0] != "/v1/responses" || s.auth[0] != "Bearer sk" {
		t.Errorf("request %s auth %q", s.paths[0], s.auth[0])
	}
	if body["store"] != false || body["stream"] != true || body["instructions"] != "sys" || body["tool_choice"] != "auto" || body["prompt_cache_key"] == "" {
		t.Errorf("body = %v", body)
	}
	if got := mustJSON(t, body["reasoning"]); got != `{"effort":"high"}` {
		t.Errorf("reasoning = %s", got)
	}
	if _, ok := body["reasoning_effort"]; ok {
		t.Error("body has top-level reasoning_effort")
	}
	tools, _ := body["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools = %v", body["tools"])
	}
	if tool := tools[0].(map[string]any); tool["type"] != "function" || tool["name"] != "read_file" || tool["strict"] != false {
		t.Errorf("tool = %v", tool)
	}

	// The next request replays the encrypted reasoning item, unchanged,
	// before the function_call, and drops the one without encrypted_content.
	msg = roundTrip(t, msg)
	msgs = append(msgs, msg, Message{Role: "tool", ToolCallID: "call_1", Content: "data"})
	if _, _, err := o.Query(context.Background(), msgs, testTools); err != nil {
		t.Fatal(err)
	}
	input := s.bodies[1]["input"].([]any)
	var types []string
	for _, it := range input {
		types = append(types, it.(map[string]any)["type"].(string))
	}
	if got := strings.Join(types, ","); got != "message,reasoning,message,function_call,function_call_output" {
		t.Fatalf("input types = %s", got)
	}
	if got := mustJSON(t, input[1]); got != `{"encrypted_content":"ENC1","id":"rs_1","summary":[],"type":"reasoning"}` {
		t.Errorf("replayed reasoning = %s", got)
	}
	if fc := input[3].(map[string]any); fc["call_id"] != "call_1" || fc["name"] != "read_file" {
		t.Errorf("function_call = %v", fc)
	}
	if out := input[4].(map[string]any); out["call_id"] != "call_1" || out["output"] != "data" {
		t.Errorf("function_call_output = %v", out)
	}
	if s.bodies[1]["prompt_cache_key"] != body["prompt_cache_key"] {
		t.Error("prompt_cache_key changed within the run")
	}
}

// TestOpenAIResponsesNoStream covers --no-stream (AC8): a non-streaming
// request whose output is parsed, reasoning kept.
func TestOpenAIResponsesNoStream(t *testing.T) {
	s := &responsesServer{reply: func(int) string { return responsesFinalJSON }}
	o := &OpenAIResponses{BaseURL: s.start(t), Model: "gpt-4.1"}
	msg, _, err := o.Query(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Content != "Done." || len(msg.ToolCalls) != 0 {
		t.Errorf("msg = %+v", msg)
	}
	if _, ok := s.bodies[0]["stream"]; ok {
		t.Errorf("non-streaming body has stream: %v", s.bodies[0])
	}
	if _, ok := s.bodies[0]["instructions"]; ok {
		t.Error("empty instructions sent")
	}
	if _, ok := s.bodies[0]["tools"]; ok {
		t.Error("tools sent with none given")
	}
	if s.auth[0] != "" {
		t.Errorf("auth sent without a key: %q", s.auth[0])
	}
	if ReasoningChars(msg) == 0 {
		t.Error("reasoning item not kept")
	}
}

func TestOpenAIResponsesIncompleteAndFailed(t *testing.T) {
	s := &responsesServer{reply: func(int) string {
		return `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"message","content":[{"type":"output_text","text":"par"}]}],"usage":{"input_tokens":1,"output_tokens":1}}`
	}}
	o := &OpenAIResponses{BaseURL: s.start(t), Model: "gpt-4.1"}
	msg, _, err := o.Query(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil || msg.Content != "par" || msg.Extra["status"] != "incomplete: max_output_tokens" {
		t.Errorf("msg = %+v, err = %v", msg, err)
	}

	s = &responsesServer{reply: func(int) string {
		return `event: response.failed
data: {"type":"response.failed","response":{"status":"failed","error":{"code":"server_error","message":"boom"}}}

`
	}}
	o = &OpenAIResponses{BaseURL: s.start(t), Model: "gpt-4.1", OnText: func(string) {}}
	if _, _, err := o.Query(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v", err)
	}
}

// TestOpenAIResponsesRetryAndOverflow covers retries and the context-too-long
// error reaching compaction (AC10).
func TestOpenAIResponsesRetryAndOverflow(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			w.WriteHeader(http.StatusTooManyRequests)
		case 2:
			io.WriteString(w, responsesFinalJSON)
		default:
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":{"message":"Your input exceeds the context window of this model.","type":"invalid_request_error","code":"context_length_exceeded"}}`)
		}
	}))
	defer srv.Close()
	o := &OpenAIResponses{BaseURL: srv.URL, Model: "gpt-4.1", Runtime: fastRetryRuntime()}
	msgs := []Message{{Role: "user", Content: "hi"}}
	if msg, _, err := o.Query(context.Background(), msgs, nil); err != nil || msg.Content != "Done." {
		t.Fatalf("msg = %+v, err = %v", msg, err)
	}
	if calls.Load() != 2 {
		t.Errorf("calls = %d, want a retry", calls.Load())
	}
	_, _, err := o.Query(context.Background(), msgs, nil)
	if err == nil || !IsContextOverflow(err) {
		t.Errorf("overflow err = %v", err)
	}
}

func TestHintResponsesAPI(t *testing.T) {
	for _, msg := range []string{
		`HTTP 400: {"error":{"message":"Function tools with reasoning_effort are not supported for gpt-6 in /v1/chat/completions. Please use /v1/responses instead."}}`,
		`HTTP 400: {"error":{"message":"This model is only supported in the Responses API."}}`,
	} {
		if h := Hint("openai/gpt-6", msg); !strings.Contains(h, "openai_api: responses") {
			t.Errorf("hint for %q = %q", msg, h)
		}
	}
	if h := Hint("openai/gpt-4o", `HTTP 400: {"error":{"message":"bad request"}}`); h != "" {
		t.Errorf("unrelated 400 hint = %q", h)
	}
}
