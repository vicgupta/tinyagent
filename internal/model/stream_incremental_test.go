package model

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// incrementalServer writes first and flushes, then waits until the test
// releases it (after the client has seen first's text) before writing rest.
// A client that buffered the whole response before reporting text would
// never release it, and the request would time out.
func incrementalServer(t *testing.T, first, rest string, release <-chan struct{}) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, first)
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
			return
		case <-time.After(5 * time.Second):
			return
		}
		io.WriteString(w, rest)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// chunkRecorder records each text delta and releases the server once the
// first one arrives, while the server is still holding back the rest.
type chunkRecorder struct {
	release chan struct{}
	once    sync.Once
	mu      sync.Mutex
	chunks  []string
}

func newChunkRecorder() *chunkRecorder { return &chunkRecorder{release: make(chan struct{})} }

func (c *chunkRecorder) onText(s string) {
	c.mu.Lock()
	c.chunks = append(c.chunks, s)
	c.mu.Unlock()
	c.once.Do(func() { close(c.release) })
}

func (c *chunkRecorder) check(t *testing.T, want ...string) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if strings.Join(c.chunks, "|") != strings.Join(want, "|") {
		t.Errorf("chunks = %q, want %q", c.chunks, want)
	}
}

func incrementalCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestOpenAIStreamIncremental(t *testing.T) {
	rec := newChunkRecorder()
	srv := incrementalServer(t,
		"data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"Hel\"}}]}\n\n",
		"data: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\n"+
			"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"type\":\"function\",\"function\":{\"name\":\"read_file\",\"arguments\":\"{\\\"pa\"}}]}}]}\n\n"+
			"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"th\\\":\\\"a\\\"}\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\n"+
			"data: [DONE]\n\n",
		rec.release)
	m := &OpenAI{BaseURL: srv.URL, Model: "gpt-4o", OnText: rec.onText}
	msg, _, err := m.Query(incrementalCtx(t), []Message{{Role: "user", Content: "hi"}}, testTools)
	if err != nil {
		t.Fatalf("query (a buffered stream would time out here): %v", err)
	}
	rec.check(t, "Hel", "lo")
	if msg.Content != "Hello" || len(msg.ToolCalls) != 1 || msg.ToolCalls[0].Function.Arguments != `{"path":"a"}` {
		t.Errorf("message = %+v", msg)
	}
}

func TestAnthropicStreamIncremental(t *testing.T) {
	rec := newChunkRecorder()
	srv := incrementalServer(t,
		"event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n"+
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n"+
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Look\"}}\n\n",
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ing\"}}\n\n"+
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n"+
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"tu1\",\"name\":\"read_file\",\"input\":{}}}\n\n"+
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"path\\\":\"}}\n\n"+
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":1,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"\\\"x\\\"}\"}}\n\n"+
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":1}\n\n"+
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":2}}\n\n"+
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
		rec.release)
	m := &Anthropic{BaseURL: srv.URL, Model: "claude-test", OnText: rec.onText}
	msg, _, err := m.Query(incrementalCtx(t), []Message{{Role: "user", Content: "hi"}}, testTools)
	if err != nil {
		t.Fatalf("query (a buffered stream would time out here): %v", err)
	}
	rec.check(t, "Look", "ing")
	if msg.Content != "Looking" || len(msg.ToolCalls) != 1 || msg.ToolCalls[0].Function.Arguments != `{"path":"x"}` {
		t.Errorf("message = %+v", msg)
	}
}

func TestOpenAIResponsesStreamIncremental(t *testing.T) {
	rec := newChunkRecorder()
	srv := incrementalServer(t,
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"Hel\"}\n\n",
		"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"lo\"}\n\n"+
			"event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"message\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"Hello\"}]}}\n\n"+
			"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\n",
		rec.release)
	m := &OpenAIResponses{BaseURL: srv.URL, APIKey: "sk", Model: "gpt-4.1", OnText: rec.onText}
	msg, _, err := m.Query(incrementalCtx(t), []Message{{Role: "user", Content: "hi"}}, testTools)
	if err != nil {
		t.Fatalf("query (a buffered stream would time out here): %v", err)
	}
	rec.check(t, "Hel", "lo")
	if msg.Content != "Hello" {
		t.Errorf("message = %+v", msg)
	}
}

// TestStreamFallbackToJSON: a server that ignores "stream": true and answers
// with a plain JSON body (with or without a JSON content type) still works.
// The whole text is reported once and tool calls are kept.
func TestStreamFallbackToJSON(t *testing.T) {
	for _, c := range []struct {
		name, body string
		ct         string
		newModel   func(url string, onText func(string)) Model
	}{
		{"openai-chat", `{"choices":[{"message":{"role":"assistant","content":"Hello","tool_calls":[{"id":"c1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"a\"}"}}]},"finish_reason":"tool_calls"}]}`, "application/json",
			func(u string, f func(string)) Model { return &OpenAI{BaseURL: u, Model: "gpt-4o", OnText: f} }},
		{"openai-chat-no-ct", `{"choices":[{"message":{"role":"assistant","content":"Hello","tool_calls":[{"id":"c1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"a\"}"}}]}}]}`, "",
			func(u string, f func(string)) Model { return &OpenAI{BaseURL: u, Model: "gpt-4o", OnText: f} }},
		{"anthropic", `{"content":[{"type":"text","text":"Hello"},{"type":"tool_use","id":"c1","name":"read_file","input":{"path":"a"}}],"stop_reason":"tool_use","usage":{"input_tokens":1,"output_tokens":1}}`, "application/json",
			func(u string, f func(string)) Model { return &Anthropic{BaseURL: u, Model: "claude-test", OnText: f} }},
		{"openai-responses", `{"status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Hello"}]},{"type":"function_call","call_id":"c1","name":"read_file","arguments":"{\"path\":\"a\"}"}],"usage":{"input_tokens":1,"output_tokens":1}}`, "application/json",
			func(u string, f func(string)) Model {
				return &OpenAIResponses{BaseURL: u, APIKey: "sk", Model: "gpt-4.1", OnText: f}
			}},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				if c.ct != "" {
					w.Header().Set("Content-Type", c.ct)
				}
				io.WriteString(w, c.body)
			}))
			defer srv.Close()
			var got []string
			msg, _, err := c.newModel(srv.URL, func(s string) { got = append(got, s) }).Query(incrementalCtx(t), []Message{{Role: "user", Content: "hi"}}, testTools)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(got, "|") != "Hello" || msg.Content != "Hello" {
				t.Errorf("onText %q, content %q", got, msg.Content)
			}
			if len(msg.ToolCalls) != 1 || msg.ToolCalls[0].Function.Name != "read_file" || msg.ToolCalls[0].Function.Arguments != `{"path":"a"}` {
				t.Errorf("tool calls = %+v", msg.ToolCalls)
			}
		})
	}
}
