package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/hooks/hookstest"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// gateHook returns a stop hook that rejects the first n results with text
// and records each payload it received in the returned directory.
func gateHook(t *testing.T, n, text string) (config.Hook, string) {
	t.Helper()
	dir := t.TempDir()
	return hookstest.Hook(t, "", "gate", dir, n, text), dir
}

func payload(t *testing.T, dir string, run int) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, strconv.Itoa(run)+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var p map[string]any
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatalf("payload %q: %v", b, err)
	}
	return p
}

func TestStopHookSendsModelBackThenAccepts(t *testing.T) {
	m := &scripted{replies: []model.Message{
		{Role: "assistant", Content: "first try\n" + SubmitMarker},
		{Role: "assistant", Content: "fixed\n" + SubmitMarker},
	}}
	a := newAgent(t, m)
	h, dir := gateHook(t, "1", "go test ./... failed: TestFoo")
	withHooks(t, a, config.Hooks{Stop: []config.Hook{h}})
	var events []map[string]any
	a.OnEvent = func(kind string, f map[string]any) {
		if kind == "hook" {
			events = append(events, f)
		}
	}
	tr := a.Run(context.Background(), "task")
	if tr.ExitStatus != StatusSubmitted || tr.Submission != "fixed" || tr.Steps != 2 || tr.StopContinuations != 1 || tr.StopReason != "" {
		t.Fatalf("trajectory = %+v", tr)
	}
	second := m.seen[1]
	last := second[len(second)-1]
	if last.Role != "user" || !strings.HasPrefix(last.Content, stopNotice) || !strings.Contains(last.Content, "]\ngo test ./... failed: TestFoo") {
		t.Errorf("feedback message = %+v", last)
	}
	p0, p1 := payload(t, dir, 0), payload(t, dir, 1)
	if p0["event"] != "stop" || p0["status"] != StatusSubmitted || p0["submission"] != "first try" || p0["stop_hook_active"] != false ||
		p0["continuations"] != float64(0) || p0["step"] != float64(1) || p0["root"] != a.Tools.Root || p0["read_only"] != false {
		t.Errorf("first payload = %v", p0)
	}
	if _, ok := p0["structured_output"]; ok {
		t.Errorf("payload has structured_output: %v", p0)
	}
	if p1["stop_hook_active"] != true || p1["continuations"] != float64(1) || p1["submission"] != "fixed" {
		t.Errorf("second payload = %v", p1)
	}
	if len(events) != 2 || events[0]["event"] != "stop" || events[0]["decision"] != "continue" || events[1]["decision"] != "allow" {
		t.Errorf("hook events = %v", events)
	}
	if _, ok := events[0]["tool_name"]; ok {
		t.Errorf("stop hook event has tool fields: %v", events[0])
	}
}

func TestStopHookGatesCompletedRuns(t *testing.T) {
	m := &scripted{replies: []model.Message{
		{Role: "assistant", Content: "done"},
		{Role: "assistant", Content: "really done"},
	}}
	a := newAgent(t, m)
	h, dir := gateHook(t, "1", "not yet")
	withHooks(t, a, config.Hooks{Stop: []config.Hook{h}})
	tr := a.Run(context.Background(), "task")
	if tr.ExitStatus != StatusCompleted || tr.Submission != "really done" || tr.StopContinuations != 1 {
		t.Fatalf("trajectory = %+v", tr)
	}
	if p := payload(t, dir, 0); p["status"] != StatusCompleted || p["submission"] != "done" {
		t.Errorf("payload = %v", p)
	}
}

func TestStopHookCapEndsWithVerificationFailed(t *testing.T) {
	var replies []model.Message
	for i := 0; i < 5; i++ {
		replies = append(replies, model.Message{Role: "assistant", Content: "done " + SubmitMarker})
	}
	m := &scripted{replies: replies}
	a := newAgent(t, m)
	h, dir := gateHook(t, "100", "tests still fail")
	max := 2
	withHooks(t, a, config.Hooks{Stop: []config.Hook{h}, MaxStopContinuations: &max})
	tr := a.Run(context.Background(), "task")
	if tr.ExitStatus != StatusVerificationFailed || tr.StopContinuations != 2 || tr.Steps != 3 || tr.Submission != "done" {
		t.Fatalf("trajectory = %+v", tr)
	}
	if !strings.Contains(tr.StopReason, "tests still fail") || tr.StructuredOutput != nil {
		t.Errorf("stop reason = %q", tr.StopReason)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 3 {
		t.Errorf("stop hook ran %d times, want 3", len(entries))
	}
}

func TestStopHooksRunInOrderAndCombineFeedback(t *testing.T) {
	m := &scripted{replies: []model.Message{
		{Role: "assistant", Content: SubmitMarker},
		{Role: "assistant", Content: SubmitMarker},
	}}
	a := newAgent(t, m)
	log := filepath.Join(t.TempDir(), "log")
	max := 1
	withHooks(t, a, config.Hooks{Stop: []config.Hook{
		hookstest.Hook(t, "", "log", log, "first"),
		hookstest.Hook(t, "", "exit2", "lint failed"),
		hookstest.Hook(t, "", "stdout", `{"decision":"continue","reason":"coverage dropped"}`),
		hookstest.Hook(t, "", "log", log, "last"),
	}, MaxStopContinuations: &max})
	tr := a.Run(context.Background(), "task")
	if tr.ExitStatus != StatusVerificationFailed {
		t.Fatalf("trajectory = %+v", tr)
	}
	got, _ := os.ReadFile(log)
	if string(got) != "first\nlast\nfirst\nlast\n" {
		t.Errorf("hook order log = %q", got)
	}
	fb := m.seen[1][len(m.seen[1])-1].Content
	if i, j := strings.Index(fb, "lint failed"), strings.Index(fb, "coverage dropped"); i < 0 || j < i {
		t.Errorf("feedback = %q", fb)
	}
	if !strings.Contains(tr.StopReason, "lint failed") || !strings.Contains(tr.StopReason, "coverage dropped") {
		t.Errorf("stop reason = %q", tr.StopReason)
	}
}

func TestStopHookErrorsFailOpen(t *testing.T) {
	for _, h := range []config.Hook{
		hookstest.Hook(t, "", "exit", "3"),
		hookstest.Hook(t, "", "stdout", "not json"),
		hookstest.Hook(t, "", "stdout", `{"decision":"maybe"}`),
	} {
		m := &scripted{replies: []model.Message{{Role: "assistant", Content: "ok " + SubmitMarker}}}
		a := newAgent(t, m)
		withHooks(t, a, config.Hooks{Stop: []config.Hook{h}})
		var errBuf bytes.Buffer
		a.Err = &errBuf
		var warned bool
		a.OnEvent = func(kind string, f map[string]any) {
			if kind == "warning" {
				warned = true
			}
		}
		tr := a.Run(context.Background(), "task")
		if tr.ExitStatus != StatusSubmitted || tr.Submission != "ok" || tr.StopContinuations != 0 {
			t.Errorf("%v: trajectory = %+v", h.Args, tr)
		}
		if !strings.Contains(errBuf.String(), "warning: stop hook ") || !strings.Contains(errBuf.String(), "treated as accept") || !warned {
			t.Errorf("%v: stderr = %q", h.Args, errBuf.String())
		}
	}
}

func TestStopHookSkipsCallsInSubmittingMessage(t *testing.T) {
	m := &scripted{replies: []model.Message{
		{Role: "assistant", Content: "done " + SubmitMarker, ToolCalls: []model.ToolCall{call("1", "bash", `{"command":"touch ran"}`)}},
		{Role: "assistant", Content: "done " + SubmitMarker},
	}}
	a := newAgent(t, m)
	h, _ := gateHook(t, "1", "check again")
	withHooks(t, a, config.Hooks{Stop: []config.Hook{h}})
	tr := a.Run(context.Background(), "task")
	if tr.ExitStatus != StatusSubmitted {
		t.Fatalf("trajectory = %+v", tr)
	}
	if _, err := os.Stat(filepath.Join(a.Tools.Root, "ran")); err == nil {
		t.Error("tool call in the submitting message ran")
	}
	res := toolResults(m.seen[1])
	if len(res) != 1 || res[0] != notRunResult {
		t.Errorf("tool results = %q", res)
	}
}

func TestStopHookLimitsWin(t *testing.T) {
	m := &scripted{replies: []model.Message{
		{Role: "assistant", Content: SubmitMarker},
		{Role: "assistant", Content: SubmitMarker},
	}}
	a := newAgent(t, m)
	a.Config.StepLimit = 1
	h, _ := gateHook(t, "100", "no")
	withHooks(t, a, config.Hooks{Stop: []config.Hook{h}})
	tr := a.Run(context.Background(), "task")
	if tr.ExitStatus != StatusStepLimit || tr.StopContinuations != 1 {
		t.Fatalf("trajectory = %+v", tr)
	}
}

func TestStopHookTimeoutIsNeverSubmitted(t *testing.T) {
	m := &scripted{replies: []model.Message{{Role: "assistant", Content: SubmitMarker}}}
	a := newAgent(t, m)
	a.Config.WallTimeSec = 1
	withHooks(t, a, config.Hooks{Stop: []config.Hook{hookstest.Hook(t, "", "sleep")}})
	tr := a.Run(context.Background(), "task")
	if tr.ExitStatus != StatusTimeLimit {
		t.Fatalf("trajectory = %+v", tr)
	}
}

func TestStopHookStructuredOutput(t *testing.T) {
	a, m := schemaAgent(t,
		submitCall("1", `{"verdict":"pass"}`),
		submitCall("2", `{"verdict":"fail"}`),
	)
	h, dir := gateHook(t, "1", "verdict is wrong")
	withHooks(t, a, config.Hooks{Stop: []config.Hook{h}})
	tr := a.Run(context.Background(), "task")
	if tr.ExitStatus != StatusSubmitted || string(tr.StructuredOutput) != `{"verdict":"fail"}` || tr.StopContinuations != 1 {
		t.Fatalf("trajectory = %+v", tr)
	}
	if p := payload(t, dir, 0); p["structured_output"] == nil || p["structured_output"].(map[string]any)["verdict"] != "pass" {
		t.Errorf("payload = %v", p)
	}
	_ = m
}

func TestStopHookSkippedForInvalidStructuredOutput(t *testing.T) {
	a, _ := schemaAgent(t,
		submitCall("1", `{"verdict":"maybe"}`),
		submitCall("2", `{"verdict":"pass"}`),
	)
	h, dir := gateHook(t, "0", "")
	withHooks(t, a, config.Hooks{Stop: []config.Hook{h}})
	tr := a.Run(context.Background(), "task")
	if tr.ExitStatus != StatusSubmitted {
		t.Fatalf("trajectory = %+v", tr)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("stop hook ran %d times, want once (only for the valid result)", len(entries))
	}
}

func TestNoStopHooksKeepsTrajectoryUnchanged(t *testing.T) {
	m := &scripted{replies: []model.Message{{Role: "assistant", Content: "x " + SubmitMarker}}}
	a := newAgent(t, m)
	withHooks(t, a, config.Hooks{PreTool: []config.Hook{hookstest.Hook(t, "", "stdout", "")}})
	tr := a.Run(context.Background(), "task")
	b, _ := json.Marshal(tr)
	if strings.Contains(string(b), "stop_continuations") || strings.Contains(string(b), "stop_reason") || tr.ExitStatus != StatusSubmitted {
		t.Errorf("trajectory = %s", b)
	}
}

func TestStopContinuedCheckpointsAreNotResumable(t *testing.T) {
	m := &scripted{replies: []model.Message{{Role: "assistant", Content: SubmitMarker}}}
	a := newAgent(t, m)
	a.Config.StepLimit = 1
	h, _ := gateHook(t, "100", "no")
	withHooks(t, a, config.Hooks{Stop: []config.Hook{h}})
	tr := a.Run(context.Background(), "task")
	if err := ValidateResume(tr, a.Config, a.Tools.Root); err == nil || !strings.Contains(err.Error(), "stop hook") {
		t.Errorf("resume error = %v", err)
	}
	failed := &trajectory.Trajectory{}
	*failed = *tr
	failed.ExitStatus = StatusVerificationFailed
	if err := ValidateResume(failed, a.Config, a.Tools.Root); err == nil || !strings.Contains(err.Error(), "unsupported exit status") {
		t.Errorf("resume error = %v", err)
	}
}
