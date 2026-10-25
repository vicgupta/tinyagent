package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/hooks/hookstest"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/tools"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

func withOutputFiles(t *testing.T, a *Agent, max int) {
	t.Helper()
	o, err := tools.NewOutputFiles(a.Tools.Root, "", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	a.Tools.OutputFiles = o
	a.Tools.MaxOutputChars = max
	a.Tools.BashTimeout = 30 * time.Second
}

func TestToolOutputFileEventsAndTrajectory(t *testing.T) { // AC11
	m := &scripted{replies: []model.Message{
		toolStep(call("big", "bash", `{"command":"seq 1 3000"}`), call("small", "bash", `{"command":"echo hi"}`)),
		{Role: "assistant", Content: "done"},
	}}
	a := newAgent(t, m)
	withOutputFiles(t, a, 800)
	var results []map[string]any
	a.OnEvent = func(kind string, f map[string]any) {
		if kind == "tool.result" {
			results = append(results, f)
		}
	}
	out := filepath.Join(t.TempDir(), "run.json")
	a.OnStep = func(tr *trajectory.Trajectory) error { return tr.Save(out) }
	tr := a.Run(context.Background(), "task")
	if len(results) != 2 {
		t.Fatalf("results = %v", results)
	}
	file, _ := results[0]["output_file"].(string)
	if file == "" || results[0]["output_bytes"] == nil {
		t.Fatalf("big result event = %v", results[0])
	}
	if _, ok := results[1]["output_file"]; ok {
		t.Errorf("small result event has output_file: %v", results[1])
	}
	data, err := os.ReadFile(filepath.Join(a.Tools.Root, file))
	if err != nil || results[0]["output_bytes"] != len(data) {
		t.Errorf("output_bytes %v, file %d bytes (%v)", results[0]["output_bytes"], len(data), err)
	}
	want := []trajectory.ToolOutputFile{{ToolCallID: "big", OutputFile: file, OutputBytes: len(data)}}
	if len(tr.ToolOutputFiles) != 1 || tr.ToolOutputFiles[0] != want[0] {
		t.Errorf("trajectory = %+v, want %+v", tr.ToolOutputFiles, want)
	}
	saved, err := trajectory.Load(out)
	if err != nil || len(saved.ToolOutputFiles) != 1 || saved.ToolOutputFiles[0] != want[0] {
		t.Errorf("saved trajectory = %+v, %v", saved.ToolOutputFiles, err)
	}
	if res := toolResults(tr.Messages); len(res[0]) > 800 || !strings.Contains(res[0], "saved to "+file) {
		t.Errorf("model result = %q", res[0])
	}
}

func TestToolOutputFilesOffKeepsEvents(t *testing.T) { // AC1
	m := &scripted{replies: []model.Message{toolStep(call("big", "bash", `{"command":"seq 1 3000"}`)), {Role: "assistant", Content: "done"}}}
	a := newAgent(t, m)
	a.Tools.MaxOutputChars = 800
	var fields map[string]any
	a.OnEvent = func(kind string, f map[string]any) {
		if kind == "tool.result" {
			fields = f
		}
	}
	tr := a.Run(context.Background(), "task")
	keys := []string{}
	for k := range fields {
		keys = append(keys, k)
	}
	if len(fields) != 4 || tr.ToolOutputFiles != nil || strings.Contains(toolResults(tr.Messages)[0], "[full output") {
		t.Errorf("event keys %v, trajectory %v", keys, tr.ToolOutputFiles)
	}
	if b, _ := json.Marshal(tr); strings.Contains(string(b), "tool_output_files") {
		t.Error("trajectory JSON has tool_output_files with the feature off")
	}
	if _, err := os.Stat(filepath.Join(a.Tools.Root, ".tinyagent")); !os.IsNotExist(err) {
		t.Errorf(".tinyagent exists: %v", err)
	}
}

func TestToolOutputFileHookInputAndFeedback(t *testing.T) {
	m := &scripted{replies: []model.Message{toolStep(call("big", "bash", `{"command":"seq 1 3000"}`)), {Role: "assistant", Content: "done"}}}
	a := newAgent(t, m)
	withOutputFiles(t, a, 800)
	stdin := filepath.Join(t.TempDir(), "in.json")
	withHooks(t, a, config.Hooks{PostTool: []config.Hook{
		hookstest.Hook(t, "bash", "stdin", stdin),
		hookstest.Hook(t, "bash", "stdout", `{"feedback":"\nHOOK FEEDBACK"}`),
	}})
	tr := a.Run(context.Background(), "task")
	var in map[string]any
	b, _ := os.ReadFile(stdin)
	if err := json.Unmarshal(b, &in); err != nil {
		t.Fatalf("hook stdin %q: %v", b, err)
	}
	if len(tr.ToolOutputFiles) != 1 || in["output_file"] != tr.ToolOutputFiles[0].OutputFile {
		t.Errorf("hook output_file = %v, trajectory %+v", in["output_file"], tr.ToolOutputFiles)
	}
	res := toolResults(tr.Messages)[0]
	if len(res) > 800 || !strings.Contains(res, "HOOK FEEDBACK") || !strings.HasSuffix(res, "on that path]") {
		t.Errorf("result after feedback (%d bytes):\n%s", len(res), res)
	}
}
