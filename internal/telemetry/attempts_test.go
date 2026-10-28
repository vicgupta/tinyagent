package telemetry

import (
	"testing"
	"time"
)

func rootAttr(e *Exporter, key string) any {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, a := range e.root.attrs {
		if a.key == key {
			return a.val
		}
	}
	return nil
}

func offlineSettings() Settings {
	return Settings{Endpoint: "http://127.0.0.1:1/v1/traces", Protocol: "http/json", Timeout: 50 * time.Millisecond,
		ServiceName: "tinyagent", MaxAttributeBytes: DefaultMaxAttributeBytes}
}

func TestAttemptsParentSpan(t *testing.T) {
	e, _ := NewAttempts(Options{Settings: offlineSettings()}, "grp", 3)
	defer e.Close()
	trace, parent, ok := ParseTraceParent(e.TraceParent())
	if !ok || hexs(trace[:]) != e.TraceID() || parent != e.root.id {
		t.Fatalf("TraceParent %q does not name the root span", e.TraceParent())
	}
	if e.root.name != "tinyagent attempts" || rootAttr(e, attrAttemptsID) != "grp" {
		t.Fatalf("root %q attrs %v", e.root.name, e.root.attrs)
	}
	e.FinishAttempts(nil, 3, "exhausted", 1.5, 10)
	if e.root.status != statusError || !e.rootEnded || rootAttr(e, attrAttemptsStop) != "exhausted" {
		t.Fatalf("status %d ended %v", e.root.status, e.rootEnded)
	}
}

func TestChildAttemptAttrs(t *testing.T) {
	e, _ := New(Options{Settings: offlineSettings()})
	defer e.Close()
	e.Emit("run.started", map[string]any{"attempt": map[string]any{"attempts_id": "grp", "index": 2, "count": 3}})
	if rootAttr(e, attrAttemptsID) != "grp" || rootAttr(e, attrAttempt) == nil || rootAttr(e, attrAttempts) == nil {
		t.Fatalf("attrs %v", e.root.attrs)
	}
}

func hexs(b []byte) string {
	const d = "0123456789abcdef"
	out := make([]byte, 0, 2*len(b))
	for _, c := range b {
		out = append(out, d[c>>4], d[c&15])
	}
	return string(out)
}
