package model

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// recorder serves replies in order and records each decoded request body.
type recorder struct {
	bodies  []map[string]any
	replies []string
}

func (r *recorder) server(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var b map[string]any
		json.NewDecoder(req.Body).Decode(&b)
		r.bodies = append(r.bodies, b)
		io.WriteString(w, r.replies[min(len(r.bodies)-1, len(r.replies)-1)])
	}))
	t.Cleanup(srv.Close)
	return srv
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// roundTrip passes a message through trajectory-style JSON, as --resume does.
func roundTrip(t *testing.T, m Message) Message {
	t.Helper()
	var out Message
	if err := json.Unmarshal([]byte(mustJSON(t, m)), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

const anthropicThinkingReply = `{"stop_reason":"tool_use","content":[` +
	`{"type":"thinking","thinking":"Let me list\nthe files.","signature":"s1"},` +
	`{"type":"redacted_thinking","data":"xyz"},` +
	`{"type":"tool_use","id":"t1","name":"list_files","input":{"path":"."}}],` +
	`"usage":{"input_tokens":1,"output_tokens":1}}`

const anthropicThinkingStream = `event: message_start
data: {"type":"message_start","message":{"usage":{"input_tokens":1,"output_tokens":0}}}

data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}

data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me list\n"}}

data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"the files."}}

data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"s1"}}

data: {"type":"content_block_stop","index":0}

data: {"type":"content_block_start","index":1,"content_block":{"type":"redacted_thinking","data":"xyz"}}

data: {"type":"content_block_stop","index":1}

data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"t1","name":"list_files","input":{}}}

data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}

data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"\".\"}"}}

data: {"type":"content_block_stop","index":2}

data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}

data: {"type":"message_stop"}

`

// TestAnthropicThinkingReplayed covers thinking and redacted_thinking blocks
// sent back unchanged, in order, before the tool_result, for streamed and
// non-streamed responses and after a trajectory round trip.
func TestAnthropicThinkingReplayed(t *testing.T) {
	// Keys are sorted because the test decodes the body into maps.
	want := `[{"signature":"s1","thinking":"Let me list\nthe files.","type":"thinking"},` +
		`{"data":"xyz","type":"redacted_thinking"},` +
		`{"id":"t1","input":{"path":"."},"name":"list_files","type":"tool_use"}]`
	for _, c := range []struct {
		name, reply string
		stream      bool
	}{{"non-streaming", anthropicThinkingReply, false}, {"streaming", anthropicThinkingStream, true}} {
		t.Run(c.name, func(t *testing.T) {
			rec := &recorder{replies: []string{c.reply}}
			a := &Anthropic{BaseURL: rec.server(t).URL, APIKey: "k", Model: "claude-sonnet-5-5"}
			if c.stream {
				a.OnText = func(string) {}
			}
			history := []Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "list"}}
			msg, _, err := a.Query(context.Background(), history, testTools)
			if err != nil {
				t.Fatal(err)
			}
			if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].ID != "t1" || msg.Content != "" {
				t.Fatalf("msg = %+v", msg)
			}
			for _, m := range []Message{msg, roundTrip(t, msg)} {
				hist := append(append([]Message{}, history...), m, Message{Role: "tool", ToolCallID: "t1", Content: "a.go"})
				if _, _, err := a.Query(context.Background(), hist, testTools); err != nil {
					t.Fatal(err)
				}
				msgs := rec.bodies[len(rec.bodies)-1]["messages"].([]any)
				if len(msgs) != 3 {
					t.Fatalf("messages = %v", msgs)
				}
				asst := msgs[1].(map[string]any)
				if asst["role"] != "assistant" || mustJSON(t, asst["content"]) != want {
					t.Errorf("assistant content = %s\nwant %s", mustJSON(t, asst["content"]), want)
				}
				res := msgs[2].(map[string]any)["content"].([]any)[0].(map[string]any)
				if res["type"] != "tool_result" || res["tool_use_id"] != "t1" {
					t.Errorf("tool result = %v", res)
				}
			}
		})
	}
}

// TestAnthropicReplayFallsBackOnMismatch keeps the plain rebuild when the
// stored blocks no longer match the message's tool calls.
func TestAnthropicReplayFallsBackOnMismatch(t *testing.T) {
	m := Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "other", Type: "function", Function: FunctionCall{Name: "x", Arguments: "{}"}}},
		Extra: map[string]any{ExtraAnthropicContent: []any{map[string]any{"type": "thinking", "thinking": "t", "signature": "s"}, map[string]any{"type": "tool_use", "id": "t1"}}}}
	_, msgs := toAnthropic([]Message{{Role: "user", Content: "u"}, m})
	if got := mustJSON(t, msgs[1].Content); strings.Contains(got, "thinking") || !strings.Contains(got, `"id":"other"`) {
		t.Errorf("content = %s", got)
	}
}

const codexReasoningEvents = `data: {"type":"response.output_item.done","item":{"type":"reasoning","id":"r1","summary":[],"encrypted_content":"e1"}}

data: {"type":"response.output_item.done","item":{"type":"reasoning","id":"r2","summary":[]}}

data: {"type":"response.output_item.done","item":{"type":"function_call","call_id":"c1","name":"list_files","arguments":"{}"}}

data: {"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":3,"output_tokens":2}}}

`

// TestCodexReasoningReplayed covers encrypted reasoning items sent back right
// before the function_call of the same response.
func TestCodexReasoningReplayed(t *testing.T) {
	rec := &recorder{replies: []string{codexReasoningEvents}}
	access := fakeJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()})
	c := &Codex{BaseURL: rec.server(t).URL, AuthFile: writeCodexAuth(t, access), Model: "gpt-5.5"}
	history := []Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "list"}}
	msg, _, err := c.Query(context.Background(), history, testTools)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []Message{msg, roundTrip(t, msg)} {
		hist := append(append([]Message{}, history...), m, Message{Role: "tool", ToolCallID: "c1", Content: "a.go"})
		if _, _, err := c.Query(context.Background(), hist, testTools); err != nil {
			t.Fatal(err)
		}
		input := rec.bodies[len(rec.bodies)-1]["input"].([]any)
		var types []string
		for _, it := range input {
			types = append(types, it.(map[string]any)["type"].(string))
		}
		// r2 has no encrypted_content and cannot be replayed with store=false.
		if strings.Join(types, ",") != "message,reasoning,function_call,function_call_output" {
			t.Fatalf("input types = %v", types)
		}
		if got := mustJSON(t, input[1]); got != `{"encrypted_content":"e1","id":"r1","summary":[],"type":"reasoning"}` {
			t.Errorf("reasoning item = %s", got)
		}
		if input[2].(map[string]any)["call_id"] != "c1" || input[3].(map[string]any)["call_id"] != "c1" {
			t.Errorf("input = %v", input)
		}
	}
}

// TestReasoningEffortRequests checks each provider's native control when set,
// and that request bodies carry no reasoning keys when unset.
func TestReasoningEffortRequests(t *testing.T) {
	openaiReply := `{"choices":[{"message":{"role":"assistant","content":"x"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`
	anthropicReply := `{"stop_reason":"end_turn","content":[{"type":"text","text":"x"}],"usage":{"input_tokens":1,"output_tokens":1}}`
	msgs := []Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "hi"}}
	reasoningKeys := []string{"reasoning", "reasoning_effort", "output_config", "thinking"}
	for _, c := range []struct {
		spec, reply, key, want string
	}{
		{"openai/gpt-5", openaiReply, "reasoning_effort", `"high"`},
		{"openrouter/openai/gpt-5", openaiReply, "reasoning", `{"effort":"high"}`},
		{"anthropic/claude-sonnet-5-5", anthropicReply, "output_config", `{"effort":"high"}`},
		{"codex/gpt-5.5", codexEvents, "reasoning", `{"effort":"high"}`},
	} {
		for _, effort := range []string{"", "high"} {
			rec := &recorder{replies: []string{c.reply}}
			url := rec.server(t).URL
			t.Setenv("OPENAI_API_KEY", "k")
			t.Setenv("OPENAI_BASE_URL", url) // a custom base URL keeps Chat Completions
			t.Setenv("ANTHROPIC_API_KEY", "k")
			auth := writeCodexAuth(t, fakeJWT(map[string]any{"exp": time.Now().Add(time.Hour).Unix()}))
			t.Setenv("CODEX_HOME", filepath.Dir(auth))
			m, err := New(c.spec, nil)
			if err != nil {
				t.Fatal(err)
			}
			switch p := m.(type) {
			case *OpenAI:
				p.BaseURL = url
			case *Anthropic:
				p.BaseURL = url
			case *Codex:
				p.BaseURL = url
			}
			if err := SetReasoningEffort(m, c.spec, effort); err != nil {
				t.Fatal(err)
			}
			if _, _, err := m.Query(context.Background(), msgs, testTools); err != nil {
				t.Fatalf("%s: %v", c.spec, err)
			}
			body := rec.bodies[0]
			if effort == "" {
				for _, k := range reasoningKeys {
					if _, ok := body[k]; ok {
						t.Errorf("%s unset: body has %q", c.spec, k)
					}
				}
				continue
			}
			if got := mustJSON(t, body[c.key]); got != c.want {
				t.Errorf("%s: %s = %s, want %s", c.spec, c.key, got, c.want)
			}
			for _, k := range reasoningKeys {
				if _, ok := body[k]; ok && k != c.key {
					t.Errorf("%s: unexpected %q", c.spec, k)
				}
			}
		}
	}
}

func TestSetReasoningEffortErrors(t *testing.T) {
	if err := SetReasoningEffort(NewOllama("llama3.1"), "ollama/llama3.1", "low"); err == nil || !strings.Contains(err.Error(), "ollama") {
		t.Errorf("ollama: %v", err)
	}
	if err := SetReasoningEffort(&OpenAI{}, "openai/gpt-5", "bogus"); err == nil {
		t.Error("bogus effort accepted")
	}
	if err := SetReasoningEffort(NewOllama("x"), "ollama/x", ""); err != nil {
		t.Errorf("unset effort: %v", err)
	}
}
