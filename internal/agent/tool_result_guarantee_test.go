package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/hooks/hookstest"
	"github.com/vicgupta/tinyagent/internal/lockdown"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/snapshot"
	"github.com/vicgupta/tinyagent/internal/tools"
)

// toolResultEvents collects tool.result events during a run.
func toolResultEvents(a *Agent) *[]map[string]any {
	var events []map[string]any
	prev := a.OnEvent
	a.OnEvent = func(kind string, fields map[string]any) {
		if prev != nil {
			prev(kind, fields)
		}
		if kind == "tool.result" {
			events = append(events, fields)
		}
	}
	return &events
}

// assertToolResult pins that a call left both a tool.result event and a
// matching tool message in history (issue #379).
func assertToolResult(t *testing.T, msgs []model.Message, events *[]map[string]any, callID, name string, wantError bool, outputSubstrings ...string) {
	t.Helper()
	var msgOut string
	for _, m := range msgs {
		if m.Role == "tool" && m.ToolCallID == callID {
			msgOut = m.Content
			break
		}
	}
	if msgOut == "" {
		t.Fatalf("no tool message for call %s in history", callID)
	}
	for _, s := range outputSubstrings {
		if !strings.Contains(msgOut, s) {
			t.Fatalf("tool message %q missing %q", msgOut, s)
		}
	}
	found := false
	for _, e := range *events {
		if e["id"] != callID || e["name"] != name {
			continue
		}
		if e["is_error"] != wantError {
			t.Fatalf("tool.result is_error = %v, want %v (%v)", e["is_error"], wantError, e)
		}
		out, _ := e["output"].(string)
		if out != msgOut {
			t.Fatalf("tool.result output = %q, want matching history %q", out, msgOut)
		}
		found = true
		break
	}
	if !found {
		t.Fatalf("no tool.result for %s/%s in %v", callID, name, *events)
	}
}

func TestPermissionDenyEmitsToolResult(t *testing.T) {
	m := &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{call("1", "bash", `{"command":"touch ran"}`)}},
		{Role: "assistant", Content: "ok"},
	}}
	a := newAgent(t, m)
	withPolicy(t, a, config.Permissions{Rules: []config.PermissionRule{
		{Tool: "bash", Command: "touch *", Decision: "deny", Message: "no writes"},
	}})
	events := toolResultEvents(a)

	tr := a.Run(context.Background(), "task")
	if tr.ExitStatus != StatusCompleted {
		t.Fatalf("status = %s", tr.ExitStatus)
	}
	assertToolResult(t, tr.Messages, events, "1", "bash", true, "denied by permission rule 0", "no writes")
}

func TestHookDenyEmitsToolResult(t *testing.T) {
	m := &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{call("1", "bash", `{"command":"echo hi"}`)}},
		{Role: "assistant", Content: "ok"},
	}}
	a := newAgent(t, m)
	withHooks(t, a, config.Hooks{PreTool: []config.Hook{hookstest.Hook(t, "bash", "exit2", "no network commands")}})
	events := toolResultEvents(a)

	tr := a.Run(context.Background(), "task")
	if tr.ExitStatus != StatusCompleted {
		t.Fatalf("status = %s", tr.ExitStatus)
	}
	assertToolResult(t, tr.Messages, events, "1", "bash", true, "denied by pre_tool hook", "no network commands")
}

func TestLockdownRefuseEmitsToolResult(t *testing.T) {
	m := &scripted{replies: []model.Message{
		assistantCalls(call("1", "bash", `{"command":"touch ran"}`)),
		{Role: "assistant", Content: "ok"},
	}}
	a := newAgent(t, m)
	a.Lockdown = newLockdown(t)
	a.Lockdown.Taint(lockdown.Source{Source: lockdown.SourceUntrustedTask})
	events := toolResultEvents(a)

	tr := a.Run(context.Background(), "task")
	if tr.ExitStatus != StatusCompleted {
		t.Fatalf("status = %s", tr.ExitStatus)
	}
	assertToolResult(t, tr.Messages, events, "1", "bash", true, "lockdown:", "refused")
}

func TestBashTimeoutEmitsToolResult(t *testing.T) {
	m := &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{call("1", "bash", `{"command":"sleep 5","timeout":1}`)}},
		{Role: "assistant", Content: "ok"},
	}}
	a := newAgent(t, m)
	a.Tools.BashTimeout = 30 * time.Second
	events := toolResultEvents(a)

	tr := a.Run(context.Background(), "task")
	if tr.ExitStatus != StatusCompleted {
		t.Fatalf("status = %s", tr.ExitStatus)
	}
	assertToolResult(t, tr.Messages, events, "1", "bash", false, "timed out")
}

func TestAskUserAnswerEmitsToolResult(t *testing.T) {
	a, tr := askedRun(t)
	tr = roundTrip(t, tr)
	m := &scripted{replies: []model.Message{{Role: "assistant", Content: "done\n" + SubmitMarker}}}
	a.Model, a.Resume, a.FollowUp = m, tr, "per key"
	events := toolResultEvents(a)

	got := a.Run(context.Background(), "")
	if got.ExitStatus != StatusSubmitted {
		t.Fatalf("status = %s (%q)", got.ExitStatus, got.Submission)
	}
	assertToolResult(t, got.Messages, events, "q", tools.AskUserToolName, false, "per key")
}

func TestResumeInterruptedEmitsToolResult(t *testing.T) {
	m := &scripted{replies: []model.Message{{Role: "assistant", Content: "working", ToolCalls: []model.ToolCall{
		call("w", "write_file", `{"path":"f.txt","content":"one"}`),
	}}}}
	a := newAgent(t, m)
	a.Config.StepLimit = 1
	saves := snapshots(t, a)
	a.Run(context.Background(), "task")
	if len(*saves) < 1 {
		t.Fatal("no checkpoints")
	}
	tr := loadCheckpoint(t, (*saves)[0]) // write-ahead: call pending
	if len(tr.PendingCalls) != 1 {
		t.Fatalf("pending = %d", len(tr.PendingCalls))
	}
	rm := &scripted{replies: []model.Message{{Role: "assistant", Content: "done"}}}
	var results []map[string]any
	var interrupted []string
	r := &Agent{Model: rm, ModelName: "fake", Tools: &tools.Executor{Root: a.Tools.Root}, Config: a.Config, Resume: tr,
		OnEvent: func(kind string, f map[string]any) {
			switch kind {
			case "resume.interrupted_call":
				interrupted = append(interrupted, f["id"].(string))
			case "tool.result":
				results = append(results, f)
			}
		}}
	r.Config.StepLimit = 0
	got := r.Run(context.Background(), "")
	if got.ExitStatus != StatusCompleted {
		t.Fatalf("status %s", got.ExitStatus)
	}
	if len(interrupted) != 1 || interrupted[0] != "w" {
		t.Fatalf("interrupted events = %v", interrupted)
	}
	if len(results) != 1 || results[0]["id"] != "w" || results[0]["is_error"] != true {
		t.Fatalf("tool.result events = %v", results)
	}
	out, _ := results[0]["output"].(string)
	if !strings.Contains(out, "[interrupted:") {
		t.Fatalf("tool.result output = %q", out)
	}
	res := toolResults(got.Messages)
	if len(res) != 1 || res[0] != out {
		t.Fatalf("history = %q, event = %q", res, out)
	}
}

func TestSnapshotFailureEmitsToolResult(t *testing.T) {
	m := &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{call("1", "write_file", `{"path":"f.txt","content":"hi"}`)}},
	}}
	a := newAgent(t, m)
	s, err := snapshot.Open(a.Tools.Root, snapshot.Options{})
	if err != nil {
		t.Fatal(err)
	}
	a.Snapshots = &failingSnaps{inner: s, failAt: 2}
	events := toolResultEvents(a)

	tr := a.Run(context.Background(), "write")
	if tr.ExitStatus != StatusError {
		t.Fatalf("status = %s", tr.ExitStatus)
	}
	if _, err := os.Stat(filepath.Join(a.Tools.Root, "f.txt")); err == nil {
		t.Error("tool ran after its snapshot failed")
	}
	assertToolResult(t, tr.Messages, events, "1", "write_file", true, "not run:", "snapshot")
}
