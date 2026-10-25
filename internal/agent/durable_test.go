package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/tools"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// snapshots saves every checkpoint the agent writes to its own file.
func snapshots(t *testing.T, a *Agent) *[]string {
	dir := t.TempDir()
	var paths []string
	a.OnStep = func(tr *trajectory.Trajectory) error {
		p := filepath.Join(dir, fmt.Sprintf("save-%02d.json", len(paths)))
		if err := tr.Save(p); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, p)
		return nil
	}
	return &paths
}

func loadCheckpoint(t *testing.T, p string) *trajectory.Trajectory {
	t.Helper()
	tr, err := trajectory.Load(p)
	if err != nil {
		t.Fatalf("load %s: %v", filepath.Base(p), err)
	}
	return tr
}

// Every save is a resumable crash point: before the tools (both calls
// pending), between them, and after them. Resume closes pending calls with an
// interrupted error result, never re-runs them, and keeps steps and cost.
func TestCrashPointsBeforeDuringAndAfterTools(t *testing.T) {
	m := &scripted{replies: []model.Message{{Role: "assistant", Content: "working", ToolCalls: []model.ToolCall{
		call("w", "write_file", `{"path":"f.txt","content":"one"}`),
		call("b", "bash", `{"command":"echo two >> f.txt"}`),
	}}}}
	a := newAgent(t, m)
	a.Config.StepLimit = 1
	saves := snapshots(t, a)
	a.Run(context.Background(), "task")
	if len(*saves) != 4 { // write-ahead, after w, after b, termination
		t.Fatalf("saves = %d, want 4", len(*saves))
	}
	for k, wantPending := range []int{2, 1, 0} {
		t.Run(fmt.Sprintf("save%d", k), func(t *testing.T) {
			tr := loadCheckpoint(t, (*saves)[k])
			if len(tr.PendingCalls) != wantPending || tr.Steps != 1 || tr.Cost != 0.01 || tr.ExitStatus != "" {
				t.Fatalf("checkpoint pending=%d steps=%d cost=%v status=%q", len(tr.PendingCalls), tr.Steps, tr.Cost, tr.ExitStatus)
			}
			if err := ValidateResume(tr, a.Config, a.Tools.Root); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(a.Tools.Root, "f.txt"), []byte("marker"), 0o644); err != nil {
				t.Fatal(err)
			}
			rm := &scripted{replies: []model.Message{{Role: "assistant", Content: "done"}}}
			var events []string
			r := &Agent{Model: rm, ModelName: "fake", Tools: &tools.Executor{Root: a.Tools.Root}, Config: a.Config, Resume: tr,
				OnEvent: func(kind string, f map[string]any) {
					if kind == "resume.interrupted_call" {
						events = append(events, f["id"].(string))
					}
				}}
			r.Config.StepLimit = 0
			got := r.Run(context.Background(), "")
			if got.ExitStatus != StatusCompleted {
				t.Fatalf("status %s: %s", got.ExitStatus, got.Submission)
			}
			sent := rm.seen[0]
			if sent[2].Content != "working" || len(sent[2].ToolCalls) != 2 {
				t.Fatal("assistant response lost")
			}
			interrupted := 0
			for _, msg := range sent[3:5] {
				if msg.Role != "tool" {
					t.Fatalf("missing result: %+v", sent)
				}
				if strings.Contains(msg.Content, "[interrupted:") {
					interrupted++
				}
			}
			if interrupted != wantPending || len(events) != wantPending {
				t.Fatalf("interrupted results %d events %v, want %d", interrupted, events, wantPending)
			}
			if !strings.HasPrefix(sent[5].Content, trajectory.ContinuationNotice) {
				t.Fatal("missing continuation notice")
			}
			if b, _ := os.ReadFile(filepath.Join(a.Tools.Root, "f.txt")); string(b) != "marker" {
				t.Fatal("interrupted call was re-run")
			}
			if got.Steps != 2 || fmt.Sprintf("%.2f", got.Cost) != "0.02" || len(got.PendingCalls) != 0 {
				t.Fatalf("accounting %d steps %v cost pending %v", got.Steps, got.Cost, got.PendingCalls)
			}
		})
	}
}

// A compacted run resumes with exactly the model view it would have sent
// next, plus the continuation notice.
func TestResumeCompactedRun(t *testing.T) {
	script := func() []reply {
		return []reply{{msg: reads(1, 1)}, {msg: reads(2, 3)}, {msg: reads(3, 1), input: 23000}, {msg: reads(4, 1)}, {msg: submit}}
	}
	full := &fakeModel{summary: "## Task\nread big.txt", replies: script()}
	a, _ := compactAgent(t, full, 30000)
	if tr := a.Run(context.Background(), "the task"); tr.ExitStatus != StatusSubmitted || len(tr.Compactions) == 0 {
		t.Fatalf("setup: %s, %d compactions", tr.ExitStatus, len(tr.Compactions))
	}
	want := full.agentRequests()[4].msgs

	cut := &fakeModel{summary: full.summary, replies: script()}
	b, _ := compactAgent(t, cut, 30000)
	b.Tools.Root = a.Tools.Root
	b.Config.StepLimit = 4
	saves := snapshots(t, b)
	if tr := b.Run(context.Background(), "the task"); tr.ExitStatus != StatusStepLimit {
		t.Fatalf("setup: %s", tr.ExitStatus)
	}
	tr := loadCheckpoint(t, (*saves)[len(*saves)-1])
	if len(tr.Compactions) == 0 {
		t.Fatal("checkpoint not compacted")
	}
	resumed := &fakeModel{summary: full.summary, replies: []reply{{msg: submit}}}
	r, _ := compactAgent(t, resumed, 30000)
	r.Tools.Root, r.Resume = a.Tools.Root, tr
	if got := r.Run(context.Background(), ""); got.ExitStatus != StatusSubmitted {
		t.Fatalf("resume: %s %s", got.ExitStatus, got.Submission)
	}
	sent := resumed.agentRequests()[0].msgs
	if len(sent) != len(want)+1 || !reflect.DeepEqual(sent[:len(want)], want) || sent[len(want)].Content != trajectory.ContinuationNotice {
		t.Fatalf("resumed view differs:\n got %d msgs\nwant %d msgs + notice", len(sent), len(want))
	}
	if resumed.summaryRequests() != 0 {
		t.Fatal("resume re-summarized")
	}
}

func TestResumeReportsLostProcesses(t *testing.T) {
	m := &scripted{replies: []model.Message{{Role: "assistant", ToolCalls: []model.ToolCall{call("s", "start_process", `{"command":"sleep 30"}`)}}}}
	a := newAgent(t, m)
	a.Config.StepLimit = 1
	saves := snapshots(t, a)
	a.Run(context.Background(), "task")
	tr := loadCheckpoint(t, (*saves)[len(*saves)-1])
	if len(tr.Processes) != 1 || tr.Processes[0].ID != "p1" || tr.Processes[0].Command != "sleep 30" || tr.Processes[0].PID <= 0 {
		t.Fatalf("processes = %+v", tr.Processes)
	}
	rm := &scripted{replies: []model.Message{{Role: "assistant", Content: "done"}}}
	var lost []map[string]any
	r := &Agent{Model: rm, ModelName: "fake", Tools: &tools.Executor{Root: a.Tools.Root}, Config: a.Config, Resume: tr,
		OnEvent: func(kind string, f map[string]any) {
			if kind == "resume.process_lost" {
				lost = append(lost, f)
			}
		}}
	r.Config.StepLimit = 0
	got := r.Run(context.Background(), "")
	notice := rm.seen[0][len(rm.seen[0])-1].Content
	if !strings.Contains(notice, "p1") || !strings.Contains(notice, "sleep 30") || !strings.Contains(notice, "gone") {
		t.Fatalf("notice = %q", notice)
	}
	if len(lost) != 1 || lost[0]["cmd"] != "sleep 30" || len(got.Processes) != 0 {
		t.Fatalf("events %v, processes %v", lost, got.Processes)
	}
}

func planCall(id, args string) model.Message {
	return model.Message{Role: "assistant", ToolCalls: []model.ToolCall{call(id, "update_plan", args)}}
}

func TestPlanDisabledByDefault(t *testing.T) {
	f := &fakeModel{replies: []reply{{msg: reads(1, 1)}, {msg: submit}}}
	a, _ := compactAgent(t, f, 0)
	out := filepath.Join(t.TempDir(), "t.json")
	a.OnStep = func(tr *trajectory.Trajectory) error { return tr.Save(out) }
	tr := a.Run(context.Background(), "task")
	for _, r := range f.agentRequests() {
		for _, s := range r.tools {
			if s.Function.Name == "update_plan" {
				t.Fatal("update_plan advertised by default")
			}
		}
		for _, m := range r.msgs {
			if strings.Contains(m.Content, planHeader) {
				t.Fatal("plan block sent by default")
			}
		}
	}
	b, _ := os.ReadFile(out)
	if strings.Contains(string(b), `"plan`) || tr.ToolSignature != ToolSignature() {
		t.Fatalf("default checkpoint changed: %s", tr.ToolSignature)
	}
}

func TestPlanToolPersistsAcrossCompactionAndResume(t *testing.T) {
	plan := `{"items":[{"id":"a","text":"read big.txt","status":"in_progress"},{"id":"b","text":"report","status":"pending"}]}`
	f := &fakeModel{summary: "## Task\nread", replies: []reply{
		{msg: planCall("p", plan)}, {msg: reads(1, 1)}, {msg: reads(2, 3)}, {msg: reads(3, 1), input: 23000}, {msg: reads(4, 1)},
	}}
	a, _ := compactAgent(t, f, 30000)
	a.Tools.Plan = true
	a.Config.StepLimit = 5
	var updates int
	a.OnEvent = func(kind string, _ map[string]any) {
		if kind == "plan.updated" {
			updates++
		}
	}
	saves := snapshots(t, a)
	tr := a.Run(context.Background(), "the task")
	if tr.ExitStatus != StatusStepLimit || len(tr.Compactions) == 0 || tr.Plan == nil || tr.Plan.Version != 1 || updates != 1 || !tr.PlanEnabled {
		t.Fatalf("run: %s compactions=%d plan=%+v updates=%d", tr.ExitStatus, len(tr.Compactions), tr.Plan, updates)
	}
	if tr.Compactions[len(tr.Compactions)-1].PlanVersion != 1 {
		t.Error("compaction did not record plan version")
	}
	reqs := f.agentRequests()
	for i, r := range reqs {
		has := false
		for _, m := range r.msgs {
			has = has || strings.Contains(m.Content, planHeader)
		}
		if !has {
			t.Errorf("request %d has no plan block", i)
		}
		if i > 0 && !strings.Contains(fmt.Sprint(r.msgs), "read big.txt (in_progress)") {
			t.Errorf("request %d lacks the current plan", i)
		}
	}
	for _, r := range f.reqs {
		if r.tools == nil && !strings.Contains(r.msgs[1].Content, "read big.txt") {
			t.Error("summarizer not given the plan")
		}
	}
	for _, m := range tr.Messages {
		if m.Role != "tool" && strings.Contains(m.Content, planHeader) {
			t.Fatal("plan block leaked into the stored history")
		}
	}

	loaded := loadCheckpoint(t, (*saves)[len(*saves)-1])
	if loaded.Plan == nil || len(loaded.PlanHistory) != 1 {
		t.Fatal("plan not in checkpoint")
	}
	// The plan setting must match on resume.
	off := &Agent{Model: &fakeModel{}, ModelName: "fake", Tools: &tools.Executor{Root: a.Tools.Root}, Config: a.Config, Resume: loadCheckpoint(t, (*saves)[len(*saves)-1])}
	if got := off.Run(context.Background(), ""); got.ExitStatus != StatusError {
		t.Fatalf("resume without plan: %s", got.ExitStatus)
	}
	rm := &fakeModel{replies: []reply{{msg: submit}}}
	r, _ := compactAgent(t, rm, 30000)
	r.Tools.Root, r.Tools.Plan, r.Resume = a.Tools.Root, true, loaded
	if got := r.Run(context.Background(), ""); got.ExitStatus != StatusSubmitted || got.Plan.Version != 1 {
		t.Fatalf("resume: %s %s", got.ExitStatus, got.Submission)
	}
	if !strings.Contains(fmt.Sprint(rm.agentRequests()[0].msgs), "read big.txt (in_progress)") {
		t.Fatal("plan missing after resume")
	}
}

func TestUpdatePlanValidation(t *testing.T) {
	a := newAgent(t, &scripted{})
	tr := &trajectory.Trajectory{Steps: 3}
	cases := map[string]string{
		`{"items":[{"id":"a","text":"x","status":"in_progress"},{"id":"b","text":"y","status":"in_progress"}]}`: "at most one",
		`{"items":[{"id":"a","text":"x","status":"later"}]}`:                                                    "invalid status",
		`{"items":[{"id":"a","text":"x","status":"done"},{"id":"a","text":"y","status":"done"}]}`:               "duplicate",
		`{"items":[{"id":"a","text":"` + strings.Repeat("x", 501) + `","status":"done"}]}`:                      "text",
		`{"note":"no items"}`: "requires items",
		`{"items":[],"x":1}`:  "invalid",
	}
	for args, want := range cases {
		out, isErr := a.updatePlan(tr, call("1", "update_plan", args))
		if !isErr || !strings.Contains(out, want) {
			t.Errorf("%s: %q, want %q", args, out, want)
		}
	}
	var items []string
	for i := range 51 {
		items = append(items, fmt.Sprintf(`{"id":"%d","text":"x","status":"pending"}`, i))
	}
	if out, isErr := a.updatePlan(tr, call("1", "update_plan", `{"items":[`+strings.Join(items, ",")+`]}`)); !isErr || !strings.Contains(out, "limit") {
		t.Errorf("51 items: %q", out)
	}
	if tr.Plan != nil {
		t.Fatal("invalid update stored")
	}
	out, isErr := a.updatePlan(tr, call("1", "update_plan", `{"items":[{"id":"a","text":"do it","status":"done"}],"note":"n"}`))
	if isErr || !strings.Contains(out, "[x] a: do it") || tr.Plan.Version != 1 || tr.Plan.UpdatedStep != 3 {
		t.Fatalf("valid update: %q %+v", out, tr.Plan)
	}
	a.updatePlan(tr, call("2", "update_plan", `{"items":[{"id":"b","text":"next","status":"pending"}]}`))
	if tr.Plan.Version != 2 || len(tr.PlanHistory) != 2 || !strings.Contains(tr.PlanHistory[1].Diff, "+ b") || !strings.Contains(tr.PlanHistory[1].Diff, "- a") {
		t.Fatalf("history = %+v", tr.PlanHistory)
	}
}

// update_plan changes no files, so it is offered in read-only mode too.
func TestPlanAllowedReadOnly(t *testing.T) {
	m := &scripted{replies: []model.Message{planCall("p", `{"items":[{"id":"a","text":"look","status":"pending"}]}`), {Role: "assistant", Content: "done"}}}
	a := newAgent(t, m)
	a.Tools.ReadOnly, a.Tools.Plan = true, true
	tr := a.Run(context.Background(), "task")
	if tr.Plan == nil || strings.HasPrefix(tr.Messages[3].Content, "error") {
		t.Fatalf("read-only plan: %+v", tr.Messages[3])
	}
	data, _ := json.Marshal(tr)
	if !strings.Contains(string(data), `"plan_enabled":true`) {
		t.Fatal("plan_enabled not recorded")
	}
}
