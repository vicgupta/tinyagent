package telemetry

import (
	"strings"
	"testing"
)

func TestLockdownTaintAttributes(t *testing.T) {
	r := newReceiver(t)
	e, warn := New(Options{Settings: testSettings(r, "http/json"), Model: "openai/gpt-4o", Task: "task"})
	if warn != "" {
		t.Fatal(warn)
	}
	e.Emit("run.started", map[string]any{"schema_version": 1, "model": "openai/gpt-4o",
		"lockdown": map[string]any{"enabled": true, "sources": []string{"web_fetch"}, "web_fetch_allowed_domains": []string{}, "tainted": false}})
	e.Emit("model.request", map[string]any{"step": 1, "model": "openai/gpt-4o", "duration_ms": int64(1), "input_tokens": 1, "output_tokens": 1})
	e.Emit("tool.call", map[string]any{"id": "c1", "name": "web_fetch", "arguments": map[string]any{"url": "https://a.example/?q=secret-query"}})
	e.Emit("taint", map[string]any{"source": "web_fetch", "step": 1, "tool_call_id": "c1", "tool": "web_fetch", "host": "a.example"})
	e.Emit("tool.result", map[string]any{"id": "c1", "name": "web_fetch", "output": "page", "is_error": false})
	e.Emit("model.request", map[string]any{"step": 2, "model": "openai/gpt-4o", "duration_ms": int64(1), "input_tokens": 1, "output_tokens": 1})
	e.Emit("tool.call", map[string]any{"id": "c2", "name": "bash", "arguments": map[string]any{"command": "true"}})
	e.Emit("lockdown.denied", map[string]any{"channel": "command", "tool": "bash", "tool_call_id": "c2", "step": 2})
	e.Emit("tool.result", map[string]any{"id": "c2", "name": "bash", "output": "error: lockdown", "is_error": true})
	e.Emit("result", map[string]any{"status": "Submitted", "steps": 2, "tainted": true, "lockdown_denials": 1})
	if w := e.Close(); w != "" {
		t.Fatal(w)
	}
	var root, fetch, bash tspan
	for _, s := range r.spans(t) {
		switch {
		case s.name == "invoke_agent tinyagent":
			root = s
		case s.attrs["gen_ai.tool.call.id"] == "c1":
			fetch = s
		case s.attrs["gen_ai.tool.call.id"] == "c2":
			bash = s
		}
	}
	if root.attrs[attrLockdownEnabled] != true || root.attrs[attrLockdownTainted] != true {
		t.Errorf("root attrs = %v", root.attrs)
	}
	if _, ok := fetch.attrs[attrLockdownTainted]; ok {
		t.Errorf("span started before taint marked tainted: %v", fetch.attrs)
	}
	if bash.attrs[attrLockdownTainted] != true {
		t.Errorf("span after taint attrs = %v", bash.attrs)
	}
	var events []string
	for _, s := range []tspan{root, fetch, bash} {
		for _, ev := range s.events {
			events = append(events, ev.name)
		}
	}
	if j := strings.Join(events, ","); !strings.Contains(j, "taint") || !strings.Contains(j, "lockdown.denied") {
		t.Errorf("events = %v", events)
	}
}

func TestLockdownOffNoAttributes(t *testing.T) {
	r := newReceiver(t)
	e, _ := New(Options{Settings: testSettings(r, "http/json"), Model: "openai/gpt-4o", Task: "task"})
	runEvents(e)
	if w := e.Close(); w != "" {
		t.Fatal(w)
	}
	for _, s := range r.spans(t) {
		if _, ok := s.attrs[attrLockdownEnabled]; ok {
			t.Errorf("%s has %s", s.name, attrLockdownEnabled)
		}
		if _, ok := s.attrs[attrLockdownTainted]; ok {
			t.Errorf("%s has %s", s.name, attrLockdownTainted)
		}
	}
}
