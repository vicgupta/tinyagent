package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vicgupta/tinyagent/internal/agent"
	"github.com/vicgupta/tinyagent/internal/model"
)

// signalWriter collects JSONL output and closes seen the first time an
// assistant.delta line is written.
type signalWriter struct {
	mu   sync.Mutex
	buf  strings.Builder
	seen chan struct{}
	once sync.Once
}

func (w *signalWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf.Write(p)
	if strings.Contains(string(p), `"type":"assistant.delta"`) {
		w.once.Do(func() { close(w.seen) })
	}
	return len(p), nil
}

// TestJSONStreamsAssistantDeltas: with --json, streamed text reaches stdout
// as assistant.delta events while the model is still generating. The server
// holds back the rest of the reply until the first delta event has been
// written, so a buffered implementation would time out.
func TestJSONStreamsAssistantDeltas(t *testing.T) {
	out := &signalWriter{seen: make(chan struct{})}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"all \"}}]}\n\n")
		w.(http.Flusher).Flush()
		select {
		case <-out.seen:
		case <-r.Context().Done():
			return
		case <-time.After(5 * time.Second):
			return
		}
		io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"good\\n"+agent.SubmitMarker+"\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer srv.Close()

	deltas := &textDeltas{}
	a := newTestAgent(t, &model.OpenAI{BaseURL: srv.URL, Model: "gpt-4o", OnText: deltas.write})
	a.Streamed = true
	events := startEvents(out, true, a, false, func() { t.Error("stop called on a healthy stream") }, nil, nil)
	deltas.events = events
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	tr := a.Run(ctx, "check")
	if err := events.finish(tr, time.Second); err != nil {
		t.Fatal(err)
	}
	if tr.ExitStatus != agent.StatusSubmitted {
		t.Fatalf("status %q (a buffered stream would time out): %s", tr.ExitStatus, out.buf.String())
	}
	var kinds, texts []string
	var message string
	for _, line := range strings.Split(strings.TrimSpace(out.buf.String()), "\n") {
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("not JSON: %q", line)
		}
		kind := ev["type"].(string)
		kinds = append(kinds, kind)
		switch kind {
		case "assistant.delta":
			texts = append(texts, ev["text"].(string))
		case "assistant.message":
			message = ev["text"].(string)
		}
	}
	want := "run.started assistant.delta assistant.delta model.request assistant.message result"
	if strings.Join(kinds, " ") != want {
		t.Errorf("events = %v, want %s", kinds, want)
	}
	if len(texts) != 2 || texts[0] != "all " || strings.Join(texts, "") != message {
		t.Errorf("deltas %q, message %q", texts, message)
	}
}

// TestTextDeltasBeforeEvents: deltas before the event stream exists, or
// with --json off, are dropped rather than panicking.
func TestTextDeltasBeforeEvents(t *testing.T) {
	(&textDeltas{}).write("x")
	(&textDeltas{events: &runEvents{}}).write("x")
}
