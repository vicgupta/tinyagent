package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

func writeCall(id, path string) model.Message {
	return model.Message{Role: "assistant", ToolCalls: []model.ToolCall{call(id, "write_file", `{"path":"`+path+`","content":"x"}`)}}
}

// With plan.require_first, a workspace-changing call before any plan is
// refused; the plan is recorded and shown, the steps run in order with
// statuses updated, and a revision (failed step, new step) is kept.
func TestPlanFirstGatesAndTracksSteps(t *testing.T) {
	m := &scripted{replies: []model.Message{
		writeCall("w0", "early.txt"),
		planCall("p1", `{"items":[{"id":"a","text":"write a.txt","status":"in_progress"},{"id":"b","text":"write b.txt","status":"pending"}]}`),
		writeCall("w1", "a.txt"),
		planCall("p2", `{"items":[{"id":"a","text":"write a.txt","status":"done"},{"id":"b","text":"write b.txt","status":"in_progress"}]}`),
		planCall("p3", `{"items":[{"id":"a","text":"write a.txt","status":"done"},{"id":"b","text":"write b.txt","status":"failed"},{"id":"c","text":"write c.txt instead","status":"in_progress"}],"note":"b is not allowed"}`),
		writeCall("w2", "c.txt"),
		planCall("p4", `{"items":[{"id":"a","text":"write a.txt","status":"done"},{"id":"b","text":"write b.txt","status":"failed"},{"id":"c","text":"write c.txt instead","status":"done"}]}`),
		{Role: "assistant", Content: "done\nCOMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT"},
	}}
	a := newAgent(t, m)
	a.Tools.Plan = true
	a.Config.Plan.RequireFirst = true
	var out bytes.Buffer
	a.Out = &out
	var kinds []string
	var firstPlanEvent, firstWrite = -1, -1
	a.OnEvent = func(kind string, f map[string]any) {
		kinds = append(kinds, kind)
		if kind == "plan.updated" && firstPlanEvent < 0 {
			firstPlanEvent = len(kinds)
		}
		if kind == "tool.result" && f["name"] == "write_file" && f["is_error"] == false && firstWrite < 0 {
			firstWrite = len(kinds)
		}
	}
	tr := a.Run(context.Background(), "write the files")
	if tr.ExitStatus != StatusSubmitted {
		t.Fatalf("exit %s", tr.ExitStatus)
	}
	if _, err := os.Stat(filepath.Join(a.Tools.Root, "early.txt")); err == nil {
		t.Fatal("write before the plan ran")
	}
	if !strings.Contains(tr.Messages[3].Content, "plan required") {
		t.Fatalf("gate result = %q", tr.Messages[3].Content)
	}
	if !strings.Contains(m.seen[0][1].Content, "call update_plan with the ordered steps") {
		t.Error("first request lacks the plan-first hint")
	}
	if firstPlanEvent < 0 || firstWrite < firstPlanEvent {
		t.Fatalf("plan event %d, first successful write %d", firstPlanEvent, firstWrite)
	}
	for _, f := range []string{"a.txt", "c.txt"} {
		if _, err := os.Stat(filepath.Join(a.Tools.Root, f)); err != nil {
			t.Errorf("%s not written", f)
		}
	}
	if tr.Plan == nil || tr.Plan.Version != 4 || len(tr.PlanHistory) != 4 {
		t.Fatalf("plan = %+v history %d", tr.Plan, len(tr.PlanHistory))
	}
	got := map[string]string{}
	for _, it := range tr.Plan.Items {
		got[it.ID] = it.Status
	}
	if got["a"] != "done" || got["b"] != "failed" || got["c"] != "done" {
		t.Errorf("statuses = %v", got)
	}
	if !strings.Contains(tr.PlanHistory[2].Diff, "+ c") || !strings.Contains(tr.PlanHistory[2].Diff, "in_progress→failed") {
		t.Errorf("revision diff = %q", tr.PlanHistory[2].Diff)
	}
	if !strings.Contains(out.String(), "plan updated:") || !strings.Contains(out.String(), "[-] b: write b.txt (failed)") {
		t.Errorf("plan not shown: %q", out.String())
	}
	data, _ := json.Marshal(tr)
	var back trajectory.Trajectory
	if err := json.Unmarshal(data, &back); err != nil || back.Plan == nil || back.Plan.Version != 4 {
		t.Fatalf("plan not recorded in trajectory: %v", err)
	}
	if !strings.Contains(strings.Join(kinds, ","), "plan.required") {
		t.Error("no plan.required event")
	}
}

// Without require_first, and for read-only calls or a direct answer, nothing
// waits for a plan.
func TestPlanFirstDoesNotBurdenTrivialTasks(t *testing.T) {
	m := &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{call("r", "list_files", `{}`)}},
		{Role: "assistant", Content: "42\nCOMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT"},
	}}
	a := newAgent(t, m)
	a.Tools.Plan = true
	a.Config.Plan.RequireFirst = true
	tr := a.Run(context.Background(), "what is here?")
	if tr.ExitStatus != StatusSubmitted || strings.Contains(tr.Messages[3].Content, "plan required") || tr.Plan != nil {
		t.Fatalf("read-only task gated: %+v", tr.Messages[3])
	}
	m = &scripted{replies: []model.Message{writeCall("w", "f.txt"), {Role: "assistant", Content: "ok\nCOMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT"}}}
	a = newAgent(t, m)
	a.Tools.Plan = true
	if tr := a.Run(context.Background(), "x"); tr.ExitStatus != StatusSubmitted || strings.Contains(tr.Messages[3].Content, "plan required") {
		t.Fatalf("plan without require_first gated: %+v", tr.Messages[3])
	}
}
