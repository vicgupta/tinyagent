package model

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func bodyServer(t *testing.T, body *map[string]any, reply string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(body)
		io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAnthropicTruncation(t *testing.T) {
	for _, reason := range []string{"max_tokens", "model_context_window_exceeded"} {
		var body map[string]any
		srv := bodyServer(t, &body, `{"stop_reason":"`+reason+`","content":[{"type":"text","text":"half"}],"usage":{"input_tokens":1,"output_tokens":1}}`)
		m := &Anthropic{BaseURL: srv.URL, APIKey: "k", Model: "c"}
		msg, _, err := m.Query(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if cut, r := Truncated(msg); !cut || r != "stop_reason: "+reason {
			t.Errorf("%s: truncated = %v %q", reason, cut, r)
		}
		if body["max_tokens"] != float64(DefaultAnthropicMaxTokens) {
			t.Errorf("max_tokens = %v", body["max_tokens"])
		}
	}
	var body map[string]any
	srv := bodyServer(t, &body, `{"stop_reason":"end_turn","content":[{"type":"text","text":"ok"}],"usage":{}}`)
	m := &Anthropic{BaseURL: srv.URL, APIKey: "k", Model: "c"}
	if err := SetMaxOutputTokens(m, "anthropic/c", 1234); err != nil {
		t.Fatal(err)
	}
	msg, _, _ := m.Query(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if cut, _ := Truncated(msg); cut {
		t.Error("end_turn marked truncated")
	}
	if body["max_tokens"] != float64(1234) {
		t.Errorf("max_tokens = %v, want 1234", body["max_tokens"])
	}
}

func TestOpenAITruncationAndBudget(t *testing.T) {
	reply := `{"choices":[{"finish_reason":"length","message":{"role":"assistant","content":"par"}}],"usage":{}}`
	var body map[string]any
	srv := bodyServer(t, &body, reply)
	m := &OpenAI{BaseURL: srv.URL, APIKey: "k", Model: "gpt"}
	msg, _, err := m.Query(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cut, r := Truncated(msg); !cut || r != "finish_reason: length" {
		t.Errorf("truncated = %v %q", cut, r)
	}
	for _, k := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens"} {
		if _, ok := body[k]; ok {
			t.Errorf("unset budget sent %s", k)
		}
	}
	for spec, key := range map[string]string{"openai/gpt": "max_completion_tokens", "openrouter/x": "max_completion_tokens", "ollama/llama": "max_tokens"} {
		body = nil
		m := &OpenAI{BaseURL: srv.URL, APIKey: "k", Model: "gpt"}
		if err := SetMaxOutputTokens(m, spec, 500); err != nil {
			t.Fatal(err)
		}
		m.Query(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
		if body[key] != float64(500) {
			t.Errorf("%s: body[%s] = %v", spec, key, body[key])
		}
	}
}

func TestResponsesTruncation(t *testing.T) {
	var body map[string]any
	srv := bodyServer(t, &body, `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[],"usage":{}}`)
	m := &OpenAIResponses{BaseURL: srv.URL, APIKey: "k", Model: "gpt"}
	if err := SetMaxOutputTokens(m, "openai/gpt", 777); err != nil {
		t.Fatal(err)
	}
	msg, _, err := m.Query(context.Background(), []Message{{Role: "user", Content: "hi"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if cut, _ := Truncated(msg); !cut {
		t.Errorf("not truncated: %+v", msg.Extra)
	}
	if body["max_output_tokens"] != float64(777) {
		t.Errorf("max_output_tokens = %v", body["max_output_tokens"])
	}
	other, _, _ := parseResponsesObject(responsesObject{Status: "incomplete", IncompleteDetails: &struct {
		Reason string `json:"reason"`
	}{"content_filter"}}, "x")
	if cut, _ := Truncated(other); cut {
		t.Error("content_filter marked truncated")
	}
}

func TestMaxOutputTokensSupport(t *testing.T) {
	if err := MaxOutputTokensSupported("codex/gpt-5"); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Errorf("codex err = %v", err)
	}
	if err := SetMaxOutputTokens(&OpenAI{}, "openai/x", -1); err == nil {
		t.Error("negative accepted")
	}
	if err := SetMaxOutputTokens(&OpenAI{}, "codex/x", 0); err != nil {
		t.Errorf("0 should be a no-op: %v", err)
	}
}

func TestReplayArgumentsSanitized(t *testing.T) {
	tc := ToolCall{Function: FunctionCall{Name: "f", Arguments: `{"a":`}}
	m := Message{Role: "assistant", ToolCalls: []ToolCall{tc}}
	if got := replayArguments(m, tc); got != `{"a":` {
		t.Errorf("untruncated replay changed: %q", got)
	}
	markTruncated(&m, "length")
	if got := replayArguments(m, tc); got != "{}" {
		t.Errorf("truncated replay = %q, want {}", got)
	}
}
