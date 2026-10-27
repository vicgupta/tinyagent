package telemetry

import (
	"strings"
	"testing"
)

// TestWorkerSpans checks two concurrent workers get their own
// "invoke_agent worker" spans under their worker tool spans, with branch
// and commit attributes, and that worker.applied lands on apply_worker.
func TestWorkerSpans(t *testing.T) {
	r := newReceiver(t)
	e, warn := New(Options{Settings: testSettings(r, "http/json"), Model: "openai/gpt-4o", Task: "t", Version: "v1"})
	if warn != "" {
		t.Fatal(warn)
	}
	e.Emit("run.started", map[string]any{"schema_version": 1, "model": "openai/gpt-4o"})
	e.Emit("model.request", map[string]any{"step": 1, "model": "openai/gpt-4o", "duration_ms": int64(1), "input_tokens": 1, "output_tokens": 1})
	e.Emit("tool.call", map[string]any{"id": "c1", "name": "worker", "arguments": map[string]any{"task": "a"}})
	e.Emit("tool.call", map[string]any{"id": "c2", "name": "worker", "arguments": map[string]any{"task": "b"}})
	e.Emit("subagent.started", map[string]any{"id": "w1", "kind": "worker", "tool_call_id": "c1", "model": "openai/gpt-4o-mini", "parent_step": 1,
		"branch": "tinyagent/worker-w1", "worktree": "/home/someone/wt1", "base_commit": "base111"})
	e.Emit("subagent.started", map[string]any{"id": "w2", "kind": "worker", "tool_call_id": "c2", "model": "openai/gpt-4o-mini", "parent_step": 1,
		"branch": "tinyagent/worker-w2", "worktree": "/home/someone/wt2", "base_commit": "base111"})
	e.Emit("tool.call", map[string]any{"id": "c_w2", "name": "bash", "arguments": map[string]any{"command": "x"}, "subagent": "w2"})
	e.Emit("tool.call", map[string]any{"id": "c_w1", "name": "bash", "arguments": map[string]any{"command": "y"}, "subagent": "w1"})
	e.Emit("tool.result", map[string]any{"id": "c_w2", "name": "bash", "output": "ok", "is_error": false, "subagent": "w2"})
	e.Emit("tool.result", map[string]any{"id": "c_w1", "name": "bash", "output": "ok", "is_error": false, "subagent": "w1"})
	e.Emit("subagent.finished", map[string]any{"id": "w2", "kind": "worker", "status": "Interrupted", "steps": 1, "input_tokens": 2, "output_tokens": 1, "cost_usd": 0.0, "files_changed": 0})
	e.Emit("subagent.finished", map[string]any{"id": "w1", "kind": "worker", "status": "Submitted", "steps": 2, "input_tokens": 3, "output_tokens": 1, "cost_usd": 0.0,
		"files_changed": 2, "branch": "tinyagent/worker-w1", "commit": "abc123"})
	e.Emit("tool.result", map[string]any{"id": "c1", "name": "worker", "output": "report", "is_error": false})
	e.Emit("tool.result", map[string]any{"id": "c2", "name": "worker", "output": "report", "is_error": false})
	e.Emit("tool.call", map[string]any{"id": "c3", "name": "apply_worker", "arguments": map[string]any{"id": "w1"}})
	e.Emit("worker.applied", map[string]any{"id": "w1", "branch": "tinyagent/worker-w1", "commit": "abc123", "files_changed": 2})
	e.Emit("tool.result", map[string]any{"id": "c3", "name": "apply_worker", "output": "applied", "is_error": false})
	e.Emit("result", map[string]any{"status": "Submitted", "steps": 2})
	if w := e.Close(); w != "" {
		t.Fatal(w)
	}
	spans := r.spans(t)
	toolSpan := map[string]tspan{}
	workers := map[string]tspan{}
	for _, s := range spans {
		switch {
		case s.name == "execute_tool worker" || s.name == "execute_tool apply_worker":
			toolSpan[s.attrs["gen_ai.tool.call.id"].(string)] = s
		case s.name == "invoke_agent worker":
			workers[s.attrs["tinyagent.subagent.id"].(string)] = s
		case s.name == "invoke_agent delegate":
			t.Errorf("worker exported as a delegate span: %+v", s)
		}
	}
	if len(workers) != 2 {
		t.Fatalf("worker spans %v", workers)
	}
	w1, w2 := workers["w1"], workers["w2"]
	if w1.parent != toolSpan["c1"].id || w2.parent != toolSpan["c2"].id {
		t.Errorf("worker parents %s %s, want %s %s", w1.parent, w2.parent, toolSpan["c1"].id, toolSpan["c2"].id)
	}
	want := map[string]any{"gen_ai.agent.name": "worker", "tinyagent.subagent.kind": "worker", "tinyagent.worker.branch": "tinyagent/worker-w1",
		"tinyagent.worker.base_commit": "base111", "tinyagent.worker.commit": "abc123", "tinyagent.worker.files_changed": int64(2),
		"tinyagent.status": "Submitted", "gen_ai.request.model": "gpt-4o-mini"}
	for k, v := range want {
		if w1.attrs[k] != v {
			t.Errorf("w1 %s = %#v, want %#v", k, w1.attrs[k], v)
		}
	}
	if w1.status == statusError {
		t.Errorf("w1 status error")
	}
	if _, ok := w2.attrs["tinyagent.worker.commit"]; ok || w2.attrs["tinyagent.worker.files_changed"] != int64(0) || w2.status != statusError {
		t.Errorf("w2 span %+v", w2)
	}
	for _, s := range spans {
		if s.name == "execute_tool bash" {
			wantParent := map[string]string{"c_w1": w1.id, "c_w2": w2.id}[s.attrs["gen_ai.tool.call.id"].(string)]
			if s.parent != wantParent {
				t.Errorf("bash %v parent %s, want %s", s.attrs["gen_ai.tool.call.id"], s.parent, wantParent)
			}
		}
		for k, v := range s.attrs {
			if sv, ok := v.(string); ok && strings.Contains(sv, "/home/someone") {
				t.Errorf("span %s attr %s exports a worktree path", s.name, k)
			}
		}
	}
	apply := toolSpan["c3"]
	if len(apply.events) != 1 || apply.events[0].name != "worker.applied" || apply.events[0].attrs["commit"] != "abc123" {
		t.Errorf("apply_worker events %+v", apply.events)
	}
}
