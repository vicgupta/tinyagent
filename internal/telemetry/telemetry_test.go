package telemetry

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/eventlog"
)

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

func TestResolveEndpoint(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.OTel
		env  map[string]string
		want string
	}{
		{"default", config.OTel{}, nil, "http://localhost:4318/v1/traces"},
		{"base env", config.OTel{}, map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://h:4318/base/"}, "http://h:4318/base/v1/traces"},
		{"base env no slash", config.OTel{}, map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://h:4318"}, "http://h:4318/v1/traces"},
		{"signal env as is", config.OTel{}, map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://h:4318/base/", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": "http://h:4318/custom"}, "http://h:4318/custom"},
		{"config wins", config.OTel{Endpoint: "https://c:443"}, map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "http://h:4318", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT": "http://h:4318/custom"}, "https://c:443/v1/traces"},
	}
	for _, c := range cases {
		s, err := Resolve(c.cfg, env(c.env))
		if err != nil || s.Endpoint != c.want {
			t.Errorf("%s: endpoint %q, %v; want %q", c.name, s.Endpoint, err, c.want)
		}
	}
	if _, err := Resolve(config.OTel{}, env(map[string]string{"OTEL_EXPORTER_OTLP_ENDPOINT": "h:4318"})); err == nil {
		t.Error("want an error for an endpoint without a scheme")
	}
}

func TestResolveProtocolTimeoutHeaders(t *testing.T) {
	s, err := Resolve(config.OTel{}, env(nil))
	if err != nil || s.Protocol != "http/protobuf" || s.Timeout != DefaultTimeout || s.ServiceName != "tinyagent" || s.MaxAttributeBytes != DefaultMaxAttributeBytes {
		t.Fatalf("defaults = %+v, %v", s, err)
	}
	s, err = Resolve(config.OTel{}, env(map[string]string{
		"OTEL_EXPORTER_OTLP_PROTOCOL": "http/json", "OTEL_EXPORTER_OTLP_TIMEOUT": "2500",
		"OTEL_EXPORTER_OTLP_HEADERS":        "authorization=Bearer%20abc123, x-team = core",
		"OTEL_EXPORTER_OTLP_TRACES_HEADERS": "x-team=traces",
		"OTEL_SERVICE_NAME":                 "ci", "OTEL_RESOURCE_ATTRIBUTES": "service.name=ignored,deployment.environment=prod",
	}))
	if err != nil || s.Protocol != "http/json" || s.Timeout != 2500*time.Millisecond || s.ServiceName != "ci" {
		t.Fatalf("env = %+v, %v", s, err)
	}
	if s.Headers["authorization"] != "Bearer abc123" || s.Headers["x-team"] != "traces" || s.Resource["deployment.environment"] != "prod" || s.Resource["service.name"] != "" {
		t.Errorf("headers %v, resource %v", s.Headers, s.Resource)
	}
	s, err = Resolve(config.OTel{HeadersEnv: []string{"MY_OTLP_HEADERS"}, Timeout: "3s", Protocol: "http/protobuf"},
		env(map[string]string{"MY_OTLP_HEADERS": "x-api-key=k1", "OTEL_EXPORTER_OTLP_TIMEOUT": "1", "OTEL_EXPORTER_OTLP_PROTOCOL": "http/json"}))
	if err != nil || s.Headers["x-api-key"] != "k1" || s.Timeout != 3*time.Second || s.Protocol != "http/protobuf" {
		t.Errorf("config = %+v, %v", s, err)
	}

	for name, vars := range map[string]map[string]string{
		"grpc":         {"OTEL_EXPORTER_OTLP_TRACES_PROTOCOL": "grpc"},
		"timeout":      {"OTEL_EXPORTER_OTLP_TIMEOUT": "soon"},
		"header entry": {"OTEL_EXPORTER_OTLP_HEADERS": "authorization"},
	} {
		if _, err := Resolve(config.OTel{}, env(vars)); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	_, err = Resolve(config.OTel{}, env(map[string]string{"OTEL_EXPORTER_OTLP_TRACES_PROTOCOL": "grpc"}))
	if !strings.Contains(err.Error(), "http/protobuf or http/json") {
		t.Errorf("grpc error = %v", err)
	}
	_, err = Resolve(config.OTel{}, env(map[string]string{"OTEL_EXPORTER_OTLP_HEADERS": "authorization:secret-value"}))
	if strings.Contains(err.Error(), "secret-value") {
		t.Errorf("header error leaks the value: %v", err)
	}
	if _, err := Resolve(config.OTel{HeadersEnv: []string{"UNSET_HEADERS"}}, env(nil)); err == nil || !strings.Contains(err.Error(), "UNSET_HEADERS") {
		t.Errorf("unset headers_env: %v", err)
	}
}

func TestParseTraceParent(t *testing.T) {
	trace, parent, ok := ParseTraceParent("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	if !ok || hex.EncodeToString(trace[:]) != "4bf92f3577b34da6a3ce929d0e0e4736" || hex.EncodeToString(parent[:]) != "00f067aa0ba902b7" {
		t.Errorf("valid: %x %x %v", trace, parent, ok)
	}
	for _, v := range []string{
		"", "garbage", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7",
		"00-00000000000000000000000000000000-00f067aa0ba902b7-01",
		"00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01",
		"00-4BF92F3577B34DA6A3CE929D0E0E4736-00f067aa0ba902b7-01",
		"ff-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
		"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01-extra",
		"00-zzf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
	} {
		if _, _, ok := ParseTraceParent(v); ok {
			t.Errorf("%q parsed", v)
		}
	}
}

// receiver is a test OTLP/HTTP endpoint.
type receiver struct {
	mu       sync.Mutex
	srv      *httptest.Server
	bodies   [][]byte
	ctypes   []string
	headers  []http.Header
	statuses []int // replies in order; 200 after they run out
	hang     chan struct{}
}

func newReceiver(t *testing.T, statuses ...int) *receiver {
	r := &receiver{statuses: statuses}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.bodies = append(r.bodies, b)
		r.ctypes = append(r.ctypes, req.Header.Get("Content-Type"))
		r.headers = append(r.headers, req.Header.Clone())
		code := 200
		if len(r.statuses) > 0 {
			code, r.statuses = r.statuses[0], r.statuses[1:]
		}
		hang := r.hang
		r.mu.Unlock()
		if hang != nil {
			select {
			case <-hang:
			case <-req.Context().Done():
			}
			return
		}
		if code == 503 {
			w.Header().Set("Retry-After", "0")
		}
		w.WriteHeader(code)
	}))
	t.Cleanup(func() {
		if r.hang != nil {
			close(r.hang)
		}
		r.srv.Close()
	})
	return r
}

func (r *receiver) requests() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.bodies)
}

// spans decodes every received request into test spans.
func (r *receiver) spans(t *testing.T) []tspan {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []tspan
	for i, b := range r.bodies {
		if r.ctypes[i] == "application/json" {
			out = append(out, decodeJSON(t, b)...)
		} else {
			out = append(out, decodeProto(t, b)...)
		}
	}
	return out
}

// tspan is a decoded span.
type tspan struct {
	traceID, id, parent, name, traceState string
	kind, status                          int
	statusMsg                             string
	start, end                            int64
	attrs                                 map[string]any
	events                                []tevent
	resource                              map[string]any
}

type tevent struct {
	name  string
	attrs map[string]any
}

func decodeJSON(t *testing.T, b []byte) []tspan {
	t.Helper()
	var req struct {
		ResourceSpans []struct {
			Resource struct {
				Attributes []jattr `json:"attributes"`
			} `json:"resource"`
			ScopeSpans []struct {
				Scope struct {
					Name    string `json:"name"`
					Version string `json:"version"`
				} `json:"scope"`
				Spans []struct {
					TraceID      string   `json:"traceId"`
					SpanID       string   `json:"spanId"`
					ParentSpanID string   `json:"parentSpanId"`
					TraceState   string   `json:"traceState"`
					Name         string   `json:"name"`
					Kind         int      `json:"kind"`
					Start        string   `json:"startTimeUnixNano"`
					End          string   `json:"endTimeUnixNano"`
					Attributes   []jattr  `json:"attributes"`
					Events       []jevent `json:"events"`
					Status       *struct {
						Code    int    `json:"code"`
						Message string `json:"message"`
					} `json:"status"`
				} `json:"spans"`
			} `json:"scopeSpans"`
		} `json:"resourceSpans"`
	}
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		t.Fatalf("OTLP JSON: %v\n%s", err, b)
	}
	var out []tspan
	for _, rs := range req.ResourceSpans {
		res := jattrs(rs.Resource.Attributes)
		for _, ss := range rs.ScopeSpans {
			for _, s := range ss.Spans {
				ts := tspan{traceID: s.TraceID, id: s.SpanID, parent: s.ParentSpanID, traceState: s.TraceState, name: s.Name, kind: s.Kind, attrs: jattrs(s.Attributes), resource: res}
				ts.start, _ = strconv.ParseInt(s.Start, 10, 64)
				ts.end, _ = strconv.ParseInt(s.End, 10, 64)
				if s.Status != nil {
					ts.status, ts.statusMsg = s.Status.Code, s.Status.Message
				}
				for _, e := range s.Events {
					ts.events = append(ts.events, tevent{e.Name, jattrs(e.Attributes)})
				}
				out = append(out, ts)
			}
		}
	}
	return out
}

type jattr struct {
	Key   string `json:"key"`
	Value struct {
		StringValue *string  `json:"stringValue"`
		BoolValue   *bool    `json:"boolValue"`
		IntValue    *string  `json:"intValue"`
		DoubleValue *float64 `json:"doubleValue"`
	} `json:"value"`
}

type jevent struct {
	Time       string  `json:"timeUnixNano"`
	Name       string  `json:"name"`
	Attributes []jattr `json:"attributes"`
}

func jattrs(as []jattr) map[string]any {
	m := map[string]any{}
	for _, a := range as {
		switch v := a.Value; {
		case v.StringValue != nil:
			m[a.Key] = *v.StringValue
		case v.BoolValue != nil:
			m[a.Key] = *v.BoolValue
		case v.IntValue != nil:
			n, _ := strconv.ParseInt(*v.IntValue, 10, 64)
			m[a.Key] = n
		case v.DoubleValue != nil:
			m[a.Key] = *v.DoubleValue
		}
	}
	return m
}

// pfield is one protobuf wire-format field.
type pfield struct {
	num  int
	wire int
	v    uint64
	b    []byte
}

func parsePB(t *testing.T, b []byte) []pfield {
	t.Helper()
	var out []pfield
	for len(b) > 0 {
		key, n := binary.Uvarint(b)
		if n <= 0 {
			t.Fatalf("bad tag")
		}
		b = b[n:]
		f := pfield{num: int(key >> 3), wire: int(key & 7)}
		switch f.wire {
		case 0:
			f.v, n = binary.Uvarint(b)
			b = b[n:]
		case 1:
			f.v = binary.LittleEndian.Uint64(b)
			b = b[8:]
		case 5:
			f.v = uint64(binary.LittleEndian.Uint32(b))
			b = b[4:]
		case 2:
			l, n := binary.Uvarint(b)
			b = b[n:]
			f.b = b[:l]
			b = b[l:]
		default:
			t.Fatalf("wire type %d", f.wire)
		}
		out = append(out, f)
	}
	return out
}

func decodeProto(t *testing.T, b []byte) []tspan {
	t.Helper()
	var out []tspan
	for _, rs := range parsePB(t, b) { // ExportTraceServiceRequest.resource_spans
		var res map[string]any
		for _, f := range parsePB(t, rs.b) {
			switch f.num {
			case 1: // Resource
				res = map[string]any{}
				for _, a := range parsePB(t, f.b) {
					if a.num == 1 {
						pattr(t, a.b, res)
					}
				}
			case 2: // ScopeSpans
				for _, ss := range parsePB(t, f.b) {
					if ss.num != 2 {
						continue
					}
					s := tspan{attrs: map[string]any{}, resource: res}
					for _, sf := range parsePB(t, ss.b) {
						switch sf.num {
						case 1:
							s.traceID = hex.EncodeToString(sf.b)
						case 2:
							s.id = hex.EncodeToString(sf.b)
						case 3:
							s.traceState = string(sf.b)
						case 4:
							s.parent = hex.EncodeToString(sf.b)
						case 5:
							s.name = string(sf.b)
						case 6:
							s.kind = int(sf.v)
						case 7:
							s.start = int64(sf.v)
						case 8:
							s.end = int64(sf.v)
						case 9:
							pattr(t, sf.b, s.attrs)
						case 11:
							e := tevent{attrs: map[string]any{}}
							for _, ef := range parsePB(t, sf.b) {
								switch ef.num {
								case 2:
									e.name = string(ef.b)
								case 3:
									pattr(t, ef.b, e.attrs)
								}
							}
							s.events = append(s.events, e)
						case 15:
							for _, st := range parsePB(t, sf.b) {
								switch st.num {
								case 2:
									s.statusMsg = string(st.b)
								case 3:
									s.status = int(st.v)
								}
							}
						}
					}
					out = append(out, s)
				}
			}
		}
	}
	return out
}

func pattr(t *testing.T, b []byte, into map[string]any) {
	var key string
	for _, f := range parsePB(t, b) {
		switch f.num {
		case 1:
			key = string(f.b)
		case 2:
			for _, v := range parsePB(t, f.b) {
				switch v.num {
				case 1:
					into[key] = string(v.b)
				case 2:
					into[key] = v.v != 0
				case 3:
					into[key] = int64(v.v)
				case 4:
					into[key] = math.Float64frombits(v.v)
				}
			}
		}
	}
}

func testSettings(r *receiver, protocol string) Settings {
	return Settings{Endpoint: r.srv.URL + "/v1/traces", Protocol: protocol, Timeout: 2 * time.Second,
		ServiceName: "tinyagent", MaxAttributeBytes: DefaultMaxAttributeBytes, Headers: map[string]string{"authorization": "Bearer abc123"}}
}

// runEvents feeds a run with two model requests, a read_file call with a
// pre_tool hook, a retry, a fallback and a delegate call.
func runEvents(e *Exporter) {
	e.Emit("run.started", map[string]any{"schema_version": 1, "model": "openai/gpt-4o"})
	e.Emit("provider.retry", map[string]any{"attempt": 1, "reason": "HTTP 429", "backoff_ms": int64(10)})
	e.Emit("model.fallback", map[string]any{"step": 1, "from": "openai/gpt-4o", "to": "anthropic/claude-x", "reason": "HTTP 503", "attempts": 3})
	e.Emit("model.request", map[string]any{"step": 1, "model": "anthropic/claude-x", "duration_ms": int64(5), "input_tokens": 10, "output_tokens": 2, "cache_read_tokens": 0, "cache_write_tokens": 0})
	e.Emit("assistant.message", map[string]any{"text": "reading the secret-task-text file"})
	e.Emit("tool.call", map[string]any{"id": "call_1", "name": "read_file", "arguments": map[string]any{"path": "x.txt"}})
	e.Emit("hook", map[string]any{"event": "pre_tool", "tool_call_id": "call_1", "tool_name": "read_file", "command": "check", "decision": "allow", "duration_ms": int64(1)})
	e.Emit("tool.result", map[string]any{"id": "call_1", "name": "read_file", "output": "value tok-SECRET-123456", "is_error": false})
	e.Emit("tool.call", map[string]any{"id": "call_2", "name": "delegate", "arguments": map[string]any{"task": "look"}})
	e.Emit("subagent.started", map[string]any{"id": "d1", "tool_call_id": "call_2", "model": "openai/gpt-4o-mini", "parent_step": 1})
	e.Emit("model.request", map[string]any{"step": 1, "model": "openai/gpt-4o-mini", "duration_ms": int64(1), "input_tokens": 3, "output_tokens": 1, "subagent": "d1"})
	e.Emit("tool.call", map[string]any{"id": "c_child", "name": "grep", "arguments": map[string]any{"pattern": "x"}, "subagent": "d1"})
	e.Emit("tool.result", map[string]any{"id": "c_child", "name": "grep", "output": "no matches", "is_error": true, "subagent": "d1"})
	e.Emit("subagent.finished", map[string]any{"id": "d1", "status": "Submitted", "steps": 1, "input_tokens": 3, "output_tokens": 1, "cost_usd": 0.0})
	e.Emit("tool.result", map[string]any{"id": "call_2", "name": "delegate", "output": "report", "is_error": false})
	e.Emit("compaction", map[string]any{"step": 2, "reason": "proactive", "tokens_before": 100, "tokens_after": 50})
	e.Emit("model.request", map[string]any{"step": 2, "model": "anthropic/claude-x", "duration_ms": int64(3), "input_tokens": 20, "output_tokens": 4})
	e.Emit("assistant.message", map[string]any{"text": "all done"})
	e.Emit("result", map[string]any{"status": "Submitted", "submission": "all done", "steps": 2, "input_tokens": 33, "output_tokens": 7,
		"cache_read_tokens": 0, "cache_write_tokens": 0, "cost_usd": 0.0125, "duration_ms": int64(42)})
}

func byName(spans []tspan) map[string]tspan {
	m := map[string]tspan{}
	for _, s := range spans {
		if _, ok := m[s.name]; !ok {
			m[s.name] = s
		}
	}
	return m
}

func TestTraceTree(t *testing.T) {
	for _, protocol := range []string{"http/protobuf", "http/json"} {
		r := newReceiver(t)
		e, warn := New(Options{Settings: testSettings(r, protocol), Model: "openai/gpt-4o", Task: "secret-task-text", Version: "v1.2.3",
			TraceParent: "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", TraceState: "vendor=x"})
		if warn != "" {
			t.Fatal(warn)
		}
		runEvents(e)
		if w := e.Close(); w != "" {
			t.Fatalf("%s: warning %s", protocol, w)
		}
		if r.requests() != 1 {
			t.Fatalf("%s: %d requests", protocol, r.requests())
		}
		wantType := map[string]string{"http/protobuf": "application/x-protobuf", "http/json": "application/json"}[protocol]
		if r.ctypes[0] != wantType || r.headers[0].Get("Authorization") != "Bearer abc123" || !strings.HasPrefix(r.headers[0].Get("User-Agent"), "tinyagent/v1.2.3") {
			t.Errorf("%s: content type %q, headers %v", protocol, r.ctypes[0], r.headers[0])
		}
		spans := r.spans(t)
		if len(spans) != 9 {
			names := []string{}
			for _, s := range spans {
				names = append(names, s.name)
			}
			t.Fatalf("%s: spans %v", protocol, names)
		}
		m := byName(spans)
		root := m["invoke_agent tinyagent"]
		if root.traceID != "4bf92f3577b34da6a3ce929d0e0e4736" || root.parent != "00f067aa0ba902b7" || root.traceState != "vendor=x" || root.kind != kindInternal {
			t.Errorf("%s: root context %+v", protocol, root)
		}
		for _, s := range spans {
			if s.traceID != root.traceID {
				t.Errorf("%s: span %s in trace %s", protocol, s.name, s.traceID)
			}
			if s.end < s.start {
				t.Errorf("%s: span %s ends before it starts", protocol, s.name)
			}
		}
		if root.resource["service.name"] != "tinyagent" || root.resource["service.version"] != "v1.2.3" {
			t.Errorf("%s: resource %v", protocol, root.resource)
		}
		wantRoot := map[string]any{"gen_ai.operation.name": "invoke_agent", "gen_ai.agent.name": "tinyagent", "gen_ai.request.model": "gpt-4o",
			"gen_ai.provider.name": "openai", "tinyagent.status": "Submitted", "tinyagent.steps": int64(2), "gen_ai.usage.input_tokens": int64(33),
			"gen_ai.usage.output_tokens": int64(7), "tinyagent.cost_usd": 0.0125, "tinyagent.resumed": false, "tinyagent.version": "v1.2.3"}
		for k, v := range wantRoot {
			if root.attrs[k] != v {
				t.Errorf("%s: root %s = %#v, want %#v", protocol, k, root.attrs[k], v)
			}
		}
		if root.status != statusUnset {
			t.Errorf("%s: root status %d", protocol, root.status)
		}
		chats := 0
		for _, s := range spans {
			if strings.HasPrefix(s.name, "chat ") && s.parent == root.id {
				chats++
				if s.kind != kindClient || s.attrs["gen_ai.operation.name"] != "chat" {
					t.Errorf("%s: chat span %+v", protocol, s)
				}
			}
		}
		if chats != 2 {
			t.Errorf("%s: %d chat spans under the root", protocol, chats)
		}
		first := m["chat claude-x"]
		if len(first.events) != 2 || first.events[0].name != "provider.retry" || first.events[0].attrs["attempt"] != int64(1) || first.events[0].attrs["reason"] != "HTTP 429" || first.events[1].name != "model.fallback" {
			t.Errorf("%s: chat events %+v", protocol, first.events)
		}
		if first.attrs["gen_ai.usage.input_tokens"] != int64(10) || first.attrs["tinyagent.step"] != int64(1) {
			t.Errorf("%s: chat attrs %v", protocol, first.attrs)
		}
		read := m["execute_tool read_file"]
		if read.parent != root.id || read.attrs["gen_ai.tool.name"] != "read_file" || read.attrs["gen_ai.tool.call.id"] != "call_1" || read.attrs["tinyagent.output_bytes"] != int64(len("value tok-SECRET-123456")) {
			t.Errorf("%s: read_file span %+v", protocol, read)
		}
		hook := m["hook pre_tool"]
		if hook.parent != read.id || hook.attrs["tinyagent.hook.decision"] != "allow" {
			t.Errorf("%s: hook span %+v", protocol, hook)
		}
		del := m["execute_tool delegate"]
		sub := m["invoke_agent delegate"]
		if sub.parent != del.id || sub.attrs["tinyagent.status"] != "Submitted" || sub.attrs["gen_ai.request.model"] != "gpt-4o-mini" {
			t.Errorf("%s: subagent span %+v (delegate %s)", protocol, sub, del.id)
		}
		if c := m["chat gpt-4o-mini"]; c.parent != sub.id {
			t.Errorf("%s: child chat parent %s, want %s", protocol, c.parent, sub.id)
		}
		grep := m["execute_tool grep"]
		if grep.parent != sub.id || grep.status != statusError || grep.attrs["error.type"] != "tool_error" {
			t.Errorf("%s: child tool %+v", protocol, grep)
		}
		if len(root.events) != 1 || root.events[0].name != "compaction" || root.events[0].attrs["tokens_after"] != int64(50) {
			t.Errorf("%s: root events %+v", protocol, root.events)
		}
		// Content is off: no text anywhere.
		for _, b := range r.bodies {
			for _, s := range []string{"secret-task-text", "x.txt", "tok-SECRET", "all done", "no matches", "report"} {
				if strings.Contains(string(b), s) {
					t.Errorf("%s: body contains %q", protocol, s)
				}
			}
			if strings.Contains(string(b), "abc123") {
				t.Errorf("%s: body contains the header value", protocol)
			}
		}
	}
}

func TestContentRedactedAndCapped(t *testing.T) {
	r := newReceiver(t)
	s := testSettings(r, "http/json")
	s.Content, s.MaxAttributeBytes = true, 16
	red := eventlog.NewRedactor([]string{"MY_TOKEN=tok-SECRET-123456"})
	e, _ := New(Options{Settings: s, Model: "openai/gpt-4o", Task: "secret-task-text", Redactor: red})
	e.Emit("model.request", map[string]any{"step": 1, "model": "openai/gpt-4o", "duration_ms": int64(1)})
	e.Emit("assistant.message", map[string]any{"text": "héllo wörld, älong assistant reply"})
	e.Emit("tool.call", map[string]any{"id": "1", "name": "bash", "arguments": map[string]any{"command": "echo"}})
	e.Emit("tool.result", map[string]any{"id": "1", "name": "bash", "output": "tok-SECRET-123456 and more", "is_error": false})
	e.Emit("result", map[string]any{"status": "Interrupted", "steps": 1})
	if w := e.Close(); w != "" {
		t.Fatal(w)
	}
	m := byName(r.spans(t))
	tool := m["execute_tool bash"]
	if got := tool.attrs["gen_ai.tool.call.result"]; got != eventlog.Redacted+" and m" {
		t.Errorf("result = %q", got)
	}
	if got := tool.attrs["gen_ai.tool.call.arguments"]; got != `{"command":"echo` {
		t.Errorf("arguments = %q", got)
	}
	for _, sp := range m {
		for k, v := range sp.attrs {
			if s, ok := v.(string); ok && len(s) > 16 {
				t.Errorf("%s %s is %d bytes", sp.name, k, len(s))
			}
		}
	}
	if got := m["chat gpt-4o"].attrs["tinyagent.assistant.text"]; got != "héllo wörld, " {
		t.Errorf("assistant text = %q", got)
	}
	root := m["invoke_agent tinyagent"]
	if root.attrs["tinyagent.task"] != "secret-task-text" || root.status != statusError || root.statusMsg != "Interrupted" {
		t.Errorf("root %+v", root)
	}
}

func TestExportRetriesTransientErrors(t *testing.T) {
	r := newReceiver(t, 503)
	e, _ := New(Options{Settings: testSettings(r, "http/protobuf"), Model: "m"})
	e.Emit("result", map[string]any{"status": "Submitted"})
	if w := e.Close(); w != "" {
		t.Fatalf("warning after 503 then 200: %s", w)
	}
	if r.requests() != 2 {
		t.Errorf("%d requests, want 2", r.requests())
	}

	r = newReceiver(t, 400, 400)
	e, _ = New(Options{Settings: testSettings(r, "http/protobuf"), Model: "m"})
	e.Emit("result", map[string]any{"status": "Submitted"})
	w := e.Close()
	if r.requests() != 1 || !strings.Contains(w, "HTTP 400") || !strings.Contains(w, "1 of 1 spans not delivered") {
		t.Errorf("400: %d requests, warning %q", r.requests(), w)
	}
	if strings.Contains(w, "abc123") {
		t.Errorf("warning leaks the header: %s", w)
	}
}

func TestExportFailuresAreBounded(t *testing.T) {
	r := newReceiver(t)
	r.hang = make(chan struct{})
	s := testSettings(r, "http/json")
	s.Timeout = 300 * time.Millisecond
	e, _ := New(Options{Settings: s, Model: "m"})
	e.Emit("result", map[string]any{"status": "Submitted"})
	start := time.Now()
	w := e.Close()
	if d := time.Since(start); d > s.Timeout+time.Second {
		t.Errorf("Close took %s", d)
	}
	if !strings.Contains(w, "no response within the 300ms timeout") {
		t.Errorf("hanging receiver warning = %q", w)
	}

	// Unreachable: connection refused is retried until the timeout.
	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL
	dead.Close()
	s = Settings{Endpoint: url + "/v1/traces", Protocol: "http/protobuf", Timeout: 300 * time.Millisecond, ServiceName: "x"}
	e, _ = New(Options{Settings: s, Model: "m"})
	start = time.Now()
	w = e.Close()
	if d := time.Since(start); d > s.Timeout+time.Second || !strings.HasPrefix(w, "otel: exporting traces to ") || strings.Count(w, "otel:") != 1 {
		t.Errorf("unreachable: %s, warning %q", d, w)
	}
	if e.Close() != "" {
		t.Error("second Close warned again")
	}
}

func TestMalformedTraceParentStartsNewTrace(t *testing.T) {
	r := newReceiver(t)
	e, warn := New(Options{Settings: testSettings(r, "http/json"), Model: "m", TraceParent: "00-bad"})
	if !strings.Contains(warn, "TRACEPARENT") {
		t.Errorf("warning = %q", warn)
	}
	e.Close()
	sp := r.spans(t)
	if len(sp) != 1 || sp[0].parent != "" || len(sp[0].traceID) != 32 || sp[0].traceID != e.TraceID() {
		t.Errorf("spans = %+v", sp)
	}
}

func TestBatchesDuringRun(t *testing.T) {
	r := newReceiver(t)
	e, _ := New(Options{Settings: testSettings(r, "http/protobuf"), Model: "m"})
	for i := 0; i < batchSize+10; i++ {
		id := strconv.Itoa(i)
		e.Emit("tool.call", map[string]any{"id": id, "name": "grep"})
		e.Emit("tool.result", map[string]any{"id": id, "name": "grep", "output": "", "is_error": false})
	}
	if w := e.Close(); w != "" {
		t.Fatal(w)
	}
	if n := len(r.spans(t)); n != batchSize+11 || r.requests() != 2 {
		t.Errorf("%d spans in %d requests", n, r.requests())
	}
}
