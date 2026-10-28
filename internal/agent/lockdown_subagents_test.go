package agent

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vicgupta/tinyagent/internal/lockdown"
	"github.com/vicgupta/tinyagent/internal/model"
)

// A worker shares the parent's lockdown state: once the parent is tainted,
// the worker's commands are refused (and recorded in the shared state), while
// its file edits still work.
func TestLockdownWorkerRefusedAfterParentTaint(t *testing.T) {
	a, _, cm := workerAgent(t,
		[]model.Message{
			assistantCalls(call("p1", "docs__search", `{"q":"x"}`)),
			assistantCalls(workerCallMsg("p2", "add b.txt")),
			{Role: "assistant", Content: "done" + submitMarker},
		},
		[]model.Message{
			assistantCalls(call("c1", "bash", `{"command":"touch ran"}`)),
			assistantCalls(call("c2", "write_file", `{"path":"b.txt","content":"new\n"}`)),
			{Role: "assistant", Content: "Added b.txt." + submitMarker},
		})
	var mcpCalls atomic.Int32
	withFakeMCP(a, &mcpCalls)
	a.Lockdown = newLockdown(t)
	var ev eventLog
	a.OnEvent = ev.on

	tr := a.Run(context.Background(), "task")
	if tr.ExitStatus != StatusSubmitted {
		t.Fatalf("status %s %s", tr.ExitStatus, tr.Submission)
	}
	if !a.Lockdown.Tainted() || mcpCalls.Load() != 1 {
		t.Fatalf("tainted %v, mcp calls %d", a.Lockdown.Tainted(), mcpCalls.Load())
	}
	if len(tr.Subagents) != 1 {
		t.Fatalf("subagents = %+v", tr.Subagents)
	}
	w := tr.Subagents[0]
	res := toolResults(w.Messages)
	if len(res) != 2 || !strings.HasPrefix(res[0], "error: lockdown: bash refused because this run has read untrusted content (mcp, step 1)") {
		t.Fatalf("worker results = %q", res)
	}
	if strings.Contains(res[1], "error") {
		t.Errorf("write_file after taint = %q", res[1])
	}
	wt := filepath.Join(a.Tools.Root, ".tinyagent/worktrees/test-w1")
	if _, err := os.Stat(filepath.Join(wt, "ran")); err == nil {
		t.Error("worker bash ran after taint")
	}
	if b, err := os.ReadFile(filepath.Join(wt, "b.txt")); err != nil || string(b) != "new\n" {
		t.Errorf("worker b.txt = %q, %v", b, err)
	}
	d := a.Lockdown.Denials()
	if len(d) != 1 || d[0].Tool != "bash" || d[0].ToolCallID != "c1" || d[0].Channel != lockdown.ChannelCommand {
		t.Errorf("denials = %+v", d)
	}
	denied := ev.of("lockdown.denied")
	if len(denied) != 1 || denied[0]["subagent"] != "w1" || denied[0]["tool"] != "bash" {
		t.Errorf("lockdown.denied events = %v", denied)
	}
	// The worker adds no taint of its own and the run's one notice stays on
	// the parent's MCP result.
	if src := a.Lockdown.Sources(); len(src) != 1 || src[0].Source != lockdown.SourceMCP || src[0].ToolCallID != "p1" {
		t.Errorf("sources = %+v", src)
	}
	if strings.Contains(strings.Join(res, ""), lockdown.Notice) {
		t.Error("notice repeated in the worker")
	}
	if names := toolNames(cm.specs[0]); slices.Contains(names, "docs__search") || slices.Contains(names, "web_fetch") {
		t.Errorf("worker tools = %v", names)
	}
}

// A worker can't bring untrusted content into the run: it gets neither
// web_fetch nor the parent's MCP tools, so a worker started before taint
// runs commands normally and leaves the parent untainted.
func TestLockdownWorkerResultDoesNotTaintParent(t *testing.T) {
	a, _, cm := workerAgent(t,
		[]model.Message{
			assistantCalls(workerCallMsg("p1", "add b.txt")),
			assistantCalls(call("p2", "bash", `{"command":"touch parent-ran"}`)),
			{Role: "assistant", Content: "done" + submitMarker},
		},
		[]model.Message{
			assistantCalls(call("c1", "docs__search", `{"q":"x"}`)),
			assistantCalls(call("c2", "bash", `{"command":"touch ran"}`)),
			{Role: "assistant", Content: "Ran it." + submitMarker},
		})
	var mcpCalls atomic.Int32
	withFakeMCP(a, &mcpCalls)
	a.Lockdown = newLockdown(t)
	var ev eventLog
	a.OnEvent = ev.on

	tr := a.Run(context.Background(), "task")
	if tr.ExitStatus != StatusSubmitted {
		t.Fatalf("status %s %s", tr.ExitStatus, tr.Submission)
	}
	if names := toolNames(cm.specs[0]); slices.Contains(names, "docs__search") || slices.Contains(names, "web_fetch") {
		t.Errorf("worker tools = %v", names)
	}
	if mcpCalls.Load() != 0 {
		t.Errorf("worker reached the MCP server %d times", mcpCalls.Load())
	}
	if a.Lockdown.Tainted() || len(a.Lockdown.Denials()) != 0 || len(ev.of("taint")) != 0 {
		t.Errorf("tainted %v, denials %+v, taint events %v", a.Lockdown.Tainted(), a.Lockdown.Denials(), ev.of("taint"))
	}
	if _, err := os.Stat(filepath.Join(a.Tools.Root, ".tinyagent/worktrees/test-w1/ran")); err != nil {
		t.Errorf("worker bash didn't run before taint: %v", err)
	}
	if _, err := os.Stat(filepath.Join(a.Tools.Root, "parent-ran")); err != nil {
		t.Errorf("parent bash refused after an untainted worker: %v", err)
	}
	if strings.Contains(strings.Join(toolResults(tr.Messages), ""), lockdown.Notice) {
		t.Error("notice on an untainted run")
	}
}

// A delegate child is read-only (no commands, web_fetch or MCP), so a
// tainted parent can still delegate and its reads work unchanged.
func TestLockdownDelegateAfterTaint(t *testing.T) {
	a, _, cm := subagentAgent(t,
		[]model.Message{
			assistantCalls(delegateCall("p1", "read a.txt")),
			{Role: "assistant", Content: "done" + submitMarker},
		},
		[]model.Message{
			assistantCalls(call("c1", "read_file", `{"path":"a.txt"}`)),
			{Role: "assistant", Content: "a.txt says hello" + submitMarker},
		})
	if err := os.WriteFile(filepath.Join(a.Tools.Root, "a.txt"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var mcpCalls atomic.Int32
	withFakeMCP(a, &mcpCalls)
	a.Lockdown = newLockdown(t)
	a.Lockdown.Taint(lockdown.Source{Source: lockdown.SourceUntrustedTask})

	tr := a.Run(context.Background(), "untrusted issue body")
	if tr.ExitStatus != StatusSubmitted {
		t.Fatalf("status %s %s", tr.ExitStatus, tr.Submission)
	}
	res := toolResults(tr.Messages)
	if len(res) != 1 || !strings.Contains(res[0], "a.txt says hello") {
		t.Fatalf("results = %q", res)
	}
	if len(tr.Subagents) != 1 {
		t.Fatalf("subagents = %+v", tr.Subagents)
	}
	if cr := toolResults(tr.Subagents[0].Messages); len(cr) != 1 || !strings.Contains(cr[0], "hello") {
		t.Errorf("delegate results = %q", cr)
	}
	for _, n := range toolNames(cm.specs[0]) {
		if n == "bash" || n == "start_process" || n == "web_fetch" || n == "docs__search" {
			t.Errorf("delegate offered outbound tool %s", n)
		}
	}
	if len(a.Lockdown.Denials()) != 0 || !a.Lockdown.Tainted() {
		t.Errorf("denials %+v, tainted %v", a.Lockdown.Denials(), a.Lockdown.Tainted())
	}
}
