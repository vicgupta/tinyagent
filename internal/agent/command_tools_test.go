package agent

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/hooks/hookstest"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/tools"
)

// withRunTests gives a's executor a run_tests command tool that creates
// "ran" in the root.
func withRunTests(t *testing.T, a *Agent) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses touch")
	}
	cts, err := tools.NewCommandTools([]config.CommandTool{{Name: "run_tests", Description: "Run the tests.", Argv: []string{"touch", "ran"}}}, a.Tools.Root, 10)
	if err != nil {
		t.Fatal(err)
	}
	a.Tools.CommandTools = cts
}

func TestCommandToolRecordedAndHookDenies(t *testing.T) {
	m := &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{call("1", "run_tests", `{}`)}},
		{Role: "assistant", Content: "ok"},
	}}
	a := newAgent(t, m)
	withRunTests(t, a)
	withHooks(t, a, config.Hooks{PreTool: []config.Hook{hookstest.Hook(t, "run_tests", "exit2", "not now")}})
	var called string
	a.OnEvent = func(kind string, fields map[string]any) {
		if kind == "tool.call" {
			called, _ = fields["name"].(string)
		}
	}
	tr := a.Run(context.Background(), "task")
	if _, err := os.Stat(filepath.Join(a.Tools.Root, "ran")); err == nil {
		t.Error("denied command tool ran")
	}
	if res := toolResults(tr.Messages); len(res) != 1 || !strings.HasPrefix(res[0], "error: denied by pre_tool hook") {
		t.Errorf("results = %q", res)
	}
	if called != "run_tests" {
		t.Errorf("tool.call name = %q", called)
	}
	if len(tr.CommandTools) != 1 || tr.CommandTools[0].Name != "run_tests" || strings.Join(tr.CommandTools[0].Argv, " ") != "touch ran" ||
		!strings.HasPrefix(tr.CommandTools[0].SchemaDigest, "sha256:") {
		t.Errorf("command_tools = %+v", tr.CommandTools)
	}
	for _, name := range tr.Tools {
		if name == "run_tests" {
			t.Error("command tool in the recorded tool set")
		}
	}
}

func TestCommandToolsResumeChanged(t *testing.T) {
	m := &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{call("1", "run_tests", `{}`)}},
	}}
	a := newAgent(t, m)
	withRunTests(t, a)
	a.Config.StepLimit = 1
	tr := a.Run(context.Background(), "task")
	if tr.ExitStatus != StatusStepLimit {
		t.Fatalf("setup: %s %s", tr.ExitStatus, tr.Submission)
	}
	if _, err := os.Stat(filepath.Join(a.Tools.Root, "ran")); err != nil {
		t.Fatal("command tool did not run")
	}
	os.Remove(filepath.Join(a.Tools.Root, "ran"))

	// Resumed without the tool: stderr and the model are told, and a call
	// to it fails without running anything.
	m = &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{call("2", "run_tests", `{}`)}},
		{Role: "assistant", Content: "done"},
	}}
	var errBuf bytes.Buffer
	a.Model, a.Resume, a.Config.StepLimit, a.Err = m, tr, 10, &errBuf
	a.Tools.CommandTools = nil
	got := a.Run(context.Background(), "")
	if got.ExitStatus != StatusCompleted {
		t.Fatalf("status %s: %s", got.ExitStatus, got.Submission)
	}
	if !strings.Contains(errBuf.String(), "command tools differ") {
		t.Errorf("stderr = %q", errBuf.String())
	}
	notice := m.seen[0][len(m.seen[0])-1].Content
	if !strings.Contains(notice, "command tools have changed") {
		t.Errorf("notice = %q", notice)
	}
	res := toolResults(got.Messages)
	if last := res[len(res)-1]; !strings.Contains(last, `unknown tool "run_tests"`) {
		t.Errorf("result = %q", last)
	}
	if _, err := os.Stat(filepath.Join(a.Tools.Root, "ran")); err == nil {
		t.Error("removed command tool ran")
	}
	if got.CommandTools != nil {
		t.Errorf("command_tools = %+v", got.CommandTools)
	}
}
