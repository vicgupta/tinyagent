package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

func toolStep(calls ...model.ToolCall) model.Message {
	return model.Message{Role: "assistant", ToolCalls: calls}
}

var badStep = toolStep(call("x", "read_file", `{"path":`))

func runSteps(t *testing.T, cfg func(*Agent), steps ...model.Message) (*trajectory.Trajectory, *scripted) {
	t.Helper()
	m := &scripted{replies: append(steps, model.Message{Role: "assistant", Content: "done"})}
	a := newAgent(t, m)
	if cfg != nil {
		cfg(a)
	}
	return a.Run(context.Background(), "task"), m
}

func TestFormatErrorInvalidJSON(t *testing.T) { // AC1
	tr, _ := runSteps(t, nil, badStep)
	res := toolResults(tr.Messages)
	if len(res) != 1 || !strings.HasPrefix(res[0], "format error:") || !strings.Contains(res[0], "invalid arguments JSON") {
		t.Fatalf("results = %q", res)
	}
	if len(res[0]) > 500 {
		t.Errorf("correction is %d chars, want <= 500", len(res[0]))
	}
	if tr.ExitStatus != StatusCompleted || tr.FormatErrors != 1 || tr.ConsecutiveFormatErrors != 1 {
		t.Errorf("status %s, format_errors %d, consecutive %d", tr.ExitStatus, tr.FormatErrors, tr.ConsecutiveFormatErrors)
	}
}

func TestFormatErrorUnknownTool(t *testing.T) { // AC2
	tr, _ := runSteps(t, nil, toolStep(call("1", "no_such_tool", `{}`)))
	res := toolResults(tr.Messages)
	if len(res) != 1 || !strings.HasPrefix(res[0], `format error: unknown tool "no_such_tool"`) || tr.ExitStatus != StatusCompleted {
		t.Fatalf("results = %q, status %s", res, tr.ExitStatus)
	}
}

func TestFormatErrorMissingRequired(t *testing.T) { // AC3
	var root string
	tr, _ := runSteps(t, func(a *Agent) {
		root = a.Tools.Root
		os.WriteFile(filepath.Join(root, "f"), []byte("a"), 0o644)
	}, toolStep(call("1", "edit_file", `{"old_str":"a","new_str":"b"}`)))
	res := toolResults(tr.Messages)
	if len(res) != 1 || !strings.HasPrefix(res[0], "format error:") || !strings.Contains(res[0], `"path"`) {
		t.Fatalf("results = %q", res)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "f")); string(b) != "a" {
		t.Errorf("tool ran: f = %q", b)
	}
}

func TestFormatErrorWrongType(t *testing.T) {
	tr, _ := runSteps(t, nil, toolStep(call("1", "bash", `{"command":["ls"]}`)))
	if res := toolResults(tr.Messages); len(res) != 1 || !strings.Contains(res[0], `field "command" must be of type string`) {
		t.Fatalf("results = %q", res)
	}
	// Numeric strings stay accepted for integers, as the tools always allowed.
	tr, _ = runSteps(t, nil, toolStep(call("1", "bash", `{"command":"echo hi","timeout":"5"}`)))
	if tr.FormatErrors != 0 {
		t.Errorf("numeric-string timeout counted as a format error: %q", toolResults(tr.Messages))
	}
}

func TestToolFailureIsNotFormatError(t *testing.T) { // AC4
	var events []map[string]any
	tr, _ := runSteps(t, func(a *Agent) {
		a.OnEvent = func(k string, f map[string]any) {
			if k == "tool.result" {
				events = append(events, f)
			}
		}
	}, toolStep(call("1", "bash", `{"command":"exit 1"}`)))
	res := toolResults(tr.Messages)
	if len(res) != 1 || !strings.HasPrefix(res[0], "<returncode>1</returncode>") || tr.FormatErrors != 0 || tr.ConsecutiveFormatErrors != 0 {
		t.Fatalf("results = %q, format errors %d", res, tr.FormatErrors)
	}
	if _, ok := events[0]["format_error"]; ok {
		t.Errorf("tool.result = %v, want no format_error", events[0])
	}
}

func TestRepeatedFormatErrorStops(t *testing.T) { // AC5, AC10 events
	var results []map[string]any
	tr, m := runSteps(t, func(a *Agent) {
		a.OnEvent = func(k string, f map[string]any) {
			if k == "tool.result" {
				results = append(results, f)
			}
		}
	}, badStep, badStep, badStep, badStep)
	if tr.ExitStatus != StatusFormatError || tr.Steps != 3 || len(m.seen) != 3 || tr.Submission != "" {
		t.Fatalf("status %s after %d steps (%d queries), submission %q", tr.ExitStatus, tr.Steps, len(m.seen), tr.Submission)
	}
	if !strings.Contains(tr.LastFormatError, "invalid arguments JSON") {
		t.Errorf("last format error = %q", tr.LastFormatError)
	}
	if len(results) != 3 || results[0]["format_error"] != true || results[0]["is_error"] != true {
		t.Errorf("tool.result events = %v", results)
	}
}

func TestFormatErrorCounterResets(t *testing.T) { // AC6 and mixed steps
	good := toolStep(call("g", "list_files", `{}`))
	mixed := toolStep(call("b", "no_such_tool", `{}`), call("g", "list_files", `{}`))
	tr, _ := runSteps(t, nil, badStep, badStep, good, badStep, badStep, mixed, badStep, badStep)
	if tr.ExitStatus != StatusCompleted || tr.FormatErrors != 7 || tr.ConsecutiveFormatErrors != 2 {
		t.Fatalf("status %s, format errors %d, consecutive %d", tr.ExitStatus, tr.FormatErrors, tr.ConsecutiveFormatErrors)
	}
}

func TestMaxFormatErrorsConfigurable(t *testing.T) { // AC7
	tr, _ := runSteps(t, func(a *Agent) { a.Config.MaxFormatErrors = 1 }, badStep, badStep)
	if tr.ExitStatus != StatusFormatError || tr.Steps != 1 {
		t.Errorf("N=1: %s after %d steps", tr.ExitStatus, tr.Steps)
	}
	tr, _ = runSteps(t, func(a *Agent) { a.Config.MaxFormatErrors = 5 }, badStep, badStep, badStep, badStep)
	if tr.ExitStatus != StatusCompleted {
		t.Errorf("N=5, 4 bad steps: %s", tr.ExitStatus)
	}
	tr, _ = runSteps(t, func(a *Agent) { a.Config.MaxFormatErrors = 5 }, badStep, badStep, badStep, badStep, badStep, badStep)
	if tr.ExitStatus != StatusFormatError || tr.Steps != 5 {
		t.Errorf("N=5: %s after %d steps", tr.ExitStatus, tr.Steps)
	}
}

func TestStepLimitBeforeFormatErrorCap(t *testing.T) { // AC9
	tr, _ := runSteps(t, func(a *Agent) { a.Config.StepLimit = 2 }, badStep, badStep, badStep)
	if tr.ExitStatus != StatusStepLimit || tr.Steps != 2 {
		t.Fatalf("%s after %d steps", tr.ExitStatus, tr.Steps)
	}
}

func TestFormatErrorTrajectoryAndResume(t *testing.T) { // AC11
	out := filepath.Join(t.TempDir(), "traj.json")
	bad := func(id string) model.Message { return toolStep(call(id, "read_file", `{"path":`)) }
	m := &scripted{replies: []model.Message{bad("a"), bad("b")}}
	a := newAgent(t, m)
	a.Config.StepLimit = 2
	a.OnStep = func(tr *trajectory.Trajectory) error { return tr.Save(out) }
	a.Run(context.Background(), "task")
	saved, err := trajectory.Load(out)
	if err != nil {
		t.Fatal(err)
	}
	if saved.FormatErrors != 2 || saved.ConsecutiveFormatErrors != 2 || len(saved.FormatErrorMessages) != 2 {
		t.Fatalf("saved counters: %d %d %v", saved.FormatErrors, saved.ConsecutiveFormatErrors, saved.FormatErrorMessages)
	}
	for _, i := range saved.FormatErrorMessages {
		if m := saved.Messages[i]; m.Role != "tool" || !strings.HasPrefix(m.Content, "format error:") {
			t.Errorf("marker %d points at %+v", i, m)
		}
	}
	if saved.Messages[2].ToolCalls[0].Function.Arguments != `{"path":` {
		t.Errorf("original arguments not kept: %q", saved.Messages[2].ToolCalls[0].Function.Arguments)
	}
	stripped := *saved
	stripped.FormatErrors, stripped.FormatErrorMessages = 0, nil
	if err := stripped.Validate(); err == nil {
		t.Error("checkpoint with an unmarked malformed call validated")
	}
	m2 := &scripted{replies: []model.Message{bad("c"), bad("d")}}
	a.Model, a.Resume, a.Config.StepLimit = m2, saved, 10
	tr := a.Run(context.Background(), "")
	if tr.ExitStatus != StatusFormatError || len(m2.seen) != 1 {
		t.Fatalf("resumed: %s after %d queries", tr.ExitStatus, len(m2.seen))
	}
}
