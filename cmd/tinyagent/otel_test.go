package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/vicgupta/tinyagent/internal/agent"
)

// otlpReceiver records OTLP/HTTP JSON exports.
type otlpReceiver struct {
	mu     sync.Mutex
	url    string
	bodies [][]byte
	auth   []string
}

func newOTLPReceiver(t *testing.T) *otlpReceiver {
	r := &otlpReceiver{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		defer r.mu.Unlock()
		if req.URL.Path != "/v1/traces" || req.Header.Get("Content-Type") != "application/json" {
			t.Errorf("export to %s as %s", req.URL.Path, req.Header.Get("Content-Type"))
		}
		r.bodies = append(r.bodies, b)
		r.auth = append(r.auth, req.Header.Get("Authorization"))
	}))
	t.Cleanup(srv.Close)
	r.url = srv.URL
	return r
}

type otlpSpan struct {
	TraceID, SpanID, ParentSpanID, Name string
	Attrs                               map[string]any
}

func (r *otlpReceiver) spans(t *testing.T) []otlpSpan {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []otlpSpan
	for _, b := range r.bodies {
		var req struct {
			ResourceSpans []struct {
				ScopeSpans []struct {
					Spans []struct {
						TraceID      string `json:"traceId"`
						SpanID       string `json:"spanId"`
						ParentSpanID string `json:"parentSpanId"`
						Name         string `json:"name"`
						Attributes   []struct {
							Key   string                     `json:"key"`
							Value map[string]json.RawMessage `json:"value"`
						} `json:"attributes"`
					} `json:"spans"`
				} `json:"scopeSpans"`
			} `json:"resourceSpans"`
		}
		if err := json.Unmarshal(b, &req); err != nil {
			t.Fatalf("export body: %v", err)
		}
		for _, rs := range req.ResourceSpans {
			for _, ss := range rs.ScopeSpans {
				for _, s := range ss.Spans {
					sp := otlpSpan{s.TraceID, s.SpanID, s.ParentSpanID, s.Name, map[string]any{}}
					for _, a := range s.Attributes {
						for k, raw := range a.Value {
							var v any
							json.Unmarshal(raw, &v)
							if k == "intValue" {
								n, _ := strconv.ParseFloat(v.(string), 64)
								v = n
							}
							sp.Attrs[a.Key] = v
						}
					}
					out = append(out, sp)
				}
			}
		}
	}
	return out
}

// newReadFileOpenAI fakes a chat provider that asks for one read_file call
// and then submits; it counts requests.
func newReadFileOpenAI(t *testing.T) *int {
	t.Helper()
	var mu sync.Mutex
	n := new(int)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.ReadAll(r.Body)
		mu.Lock()
		*n++
		first := *n%2 == 1
		mu.Unlock()
		msg := map[string]any{"role": "assistant", "content": "done\n" + agent.SubmitMarker}
		if first {
			msg = map[string]any{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{
				"id": "call_read_1", "type": "function", "function": map[string]any{"name": "read_file", "arguments": `{"path":"notes.txt"}`}}}}
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": msg, "finish_reason": "stop"}},
			"usage": map[string]any{"prompt_tokens": 7, "completion_tokens": 3}})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	t.Setenv("OPENAI_API_KEY", "sk-test-key-0123456789abcdef")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	return n
}

func otelWorkdir(t *testing.T) string {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func clearOTelEnv(t *testing.T) {
	for _, k := range []string{"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "OTEL_EXPORTER_OTLP_PROTOCOL", "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL",
		"OTEL_EXPORTER_OTLP_HEADERS", "OTEL_EXPORTER_OTLP_TRACES_HEADERS", "OTEL_EXPORTER_OTLP_TIMEOUT", "OTEL_EXPORTER_OTLP_TRACES_TIMEOUT",
		"OTEL_SERVICE_NAME", "OTEL_RESOURCE_ATTRIBUTES", "TRACEPARENT", "TRACESTATE"} {
		t.Setenv(k, "")
	}
}

func otelEvents(t *testing.T, stdout string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("not JSONL: %q", line)
		}
		out = append(out, ev)
	}
	return out
}

func TestOTelOffByDefault(t *testing.T) {
	clearOTelEnv(t)
	r := newOTLPReceiver(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", r.url)
	newReadFileOpenAI(t)
	code, _, stderr := runCLI(t, "-t", "read notes", "-m", "openai/gpt-4o", "--no-stream", "--cwd", otelWorkdir(t), "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if len(r.bodies) != 0 {
		t.Errorf("%d exports without --otel", len(r.bodies))
	}
}

func TestOTelCLITrace(t *testing.T) {
	clearOTelEnv(t)
	r := newOTLPReceiver(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", r.url)
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "http/json")
	t.Setenv("OTEL_EXPORTER_OTLP_HEADERS", "authorization=Bearer abc123")
	t.Setenv("TRACEPARENT", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	newReadFileOpenAI(t)
	dir := otelWorkdir(t)
	traj := filepath.Join(t.TempDir(), "run.json")
	db := filepath.Join(t.TempDir(), "log.db")
	code, stdout, stderr := runCLI(t, "-t", "read notes", "-m", "openai/gpt-4o", "--no-stream", "--cwd", dir, "--otel", "--json", "-v", "-o", traj, "--log-db="+db)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stderr, "otel: exporting traces to "+strings.TrimPrefix(r.url, "http://")+" (http/json)") || strings.Contains(stderr, "warning") {
		t.Errorf("stderr:\n%s", stderr)
	}
	if len(r.auth) == 0 || r.auth[0] != "Bearer abc123" {
		t.Errorf("authorization headers %q", r.auth)
	}
	b, _ := os.ReadFile(traj)
	d, _ := os.ReadFile(db)
	for name, s := range map[string]string{"stdout": stdout, "stderr": stderr, "trajectory": string(b), "log db": string(d)} {
		if strings.Contains(s, "abc123") {
			t.Errorf("header value in %s", name)
		}
	}

	events := otelEvents(t, stdout)
	var callID string
	var result, started map[string]any
	for _, ev := range events {
		switch ev["type"] {
		case "tool.call":
			callID = ev["id"].(string)
		case "result":
			result = ev
		case "run.started":
			started = ev
		}
	}
	if o, _ := started["otel"].(map[string]any); o["enabled"] != true || o["protocol"] != "http/json" || o["trace_id"] != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Errorf("run.started otel = %v", started["otel"])
	}

	spans := r.spans(t)
	var root otlpSpan
	chats, reads := 0, 0
	for _, s := range spans {
		if s.Name == "invoke_agent tinyagent" {
			root = s
		}
	}
	if root.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" || root.ParentSpanID != "00f067aa0ba902b7" {
		t.Fatalf("root = %+v", root)
	}
	for _, s := range spans {
		switch {
		case s.Name == "chat gpt-4o" && s.ParentSpanID == root.SpanID:
			chats++
		case s.Name == "execute_tool read_file" && s.ParentSpanID == root.SpanID:
			reads++
			if s.Attrs["gen_ai.tool.name"] != "read_file" || s.Attrs["gen_ai.tool.call.id"] != callID || callID != "call_read_1" {
				t.Errorf("read_file span %v, json call id %q", s.Attrs, callID)
			}
		}
		if s.TraceID != root.TraceID {
			t.Errorf("span %s in another trace", s.Name)
		}
	}
	if chats != 2 || reads != 1 || len(spans) != 4 {
		t.Errorf("%d spans, %d chat, %d read_file", len(spans), chats, reads)
	}
	for attr, field := range map[string]string{"tinyagent.status": "status", "tinyagent.steps": "steps", "gen_ai.usage.input_tokens": "input_tokens",
		"gen_ai.usage.output_tokens": "output_tokens", "tinyagent.cost_usd": "cost_usd", "tinyagent.duration_ms": "duration_ms"} {
		if root.Attrs[attr] != result[field] {
			t.Errorf("root %s = %v, result %s = %v", attr, root.Attrs[attr], field, result[field])
		}
	}
	// Content is off by default.
	for _, body := range r.bodies {
		for _, s := range []string{"read notes", "notes.txt", "hello"} {
			if strings.Contains(string(body), s) {
				t.Errorf("export contains %q", s)
			}
		}
	}
}

func TestOTelFailureIsHarmless(t *testing.T) {
	clearOTelEnv(t)
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	t.Setenv("OTEL_EXPORTER_OTLP_TIMEOUT", "300")
	cfg := writeConfig(t, "otel:\n  endpoint: "+dead.URL+"\n")
	dir := otelWorkdir(t)
	newReadFileOpenAI(t)
	code0, out0, _ := runCLI(t, "-t", "read notes", "-m", "openai/gpt-4o", "--no-stream", "--cwd", dir, "--json", "-c", cfg)
	code, out, stderr := runCLI(t, "-t", "read notes", "-m", "openai/gpt-4o", "--no-stream", "--cwd", dir, "--json", "-c", cfg, "--otel")
	if code != code0 || code != 0 {
		t.Fatalf("exit %d, without --otel %d: %s", code, code0, stderr)
	}
	if strings.Count(stderr, "warning: otel: exporting traces to") != 1 {
		t.Errorf("stderr:\n%s", stderr)
	}
	kinds := func(events []map[string]any) (ks []string, warnings int, result map[string]any) {
		for _, ev := range events {
			if ev["type"] == "warning" {
				warnings++
				continue
			}
			ks = append(ks, ev["type"].(string))
			if ev["type"] == "result" {
				result = ev
				delete(result, "duration_ms")
			}
		}
		return
	}
	k0, w0, r0 := kinds(otelEvents(t, out0))
	k1, w1, r1 := kinds(otelEvents(t, out))
	if strings.Join(k0, ",") != strings.Join(k1, ",") || w0 != 0 || w1 != 1 {
		t.Errorf("events %v (%d warnings) vs %v (%d warnings)", k0, w0, k1, w1)
	}
	b0, _ := json.Marshal(r0)
	b1, _ := json.Marshal(r1)
	if string(b0) != string(b1) {
		t.Errorf("result %s vs %s", b0, b1)
	}
}

func TestOTelConfigErrors(t *testing.T) {
	clearOTelEnv(t)
	dir := otelWorkdir(t)
	requests := newReadFileOpenAI(t)
	cases := []struct {
		name, config, env, want string
	}{
		{"grpc env", "", "grpc", "http/protobuf or http/json"},
		{"grpc config", "otel:\n  protocol: grpc\n", "", "http/protobuf or http/json"},
		{"unknown key", "otel:\n  bogus: 1\n", "", "bogus"},
		{"bad timeout", "otel:\n  timeout: soon\n", "", "otel.timeout"},
	}
	for _, c := range cases {
		t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", c.env)
		args := []string{"-t", "x", "-m", "openai/gpt-4o", "--no-stream", "--cwd", dir, "--otel"}
		if c.config != "" {
			args = append(args, "-c", writeConfig(t, c.config))
		}
		code, _, stderr := runCLI(t, args...)
		if code != 2 || !strings.Contains(stderr, c.want) {
			t.Errorf("%s: exit %d, stderr %s", c.name, code, stderr)
		}
	}
	if *requests != 0 {
		t.Errorf("%d provider requests", *requests)
	}
	// Without --otel the environment is never consulted.
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "grpc")
	if code, _, stderr := runCLI(t, "-t", "x", "-m", "openai/gpt-4o", "--no-stream", "--cwd", dir); code != 0 {
		t.Errorf("grpc env without --otel: exit %d: %s", code, stderr)
	}
}
