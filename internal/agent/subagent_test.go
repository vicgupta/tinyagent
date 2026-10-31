package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/hooks/hookstest"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/tools"
)

const submitMarker = "\nCOMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT"

func delegateCall(id, task string) model.ToolCall {
	b, _ := json.Marshal(map[string]string{"task": task})
	return call(id, "delegate", string(b))
}

func assistantCalls(calls ...model.ToolCall) model.Message {
	return model.Message{Role: "assistant", ToolCalls: calls}
}

// subagentAgent returns a parent with subagents on, its scripted model, and
// the children's scripted model.
func subagentAgent(t *testing.T, parent, child []model.Message) (*Agent, *specModel, *specModel) {
	t.Helper()
	pm := &specModel{scripted: scripted{replies: parent}}
	cm := &specModel{scripted: scripted{replies: child}}
	a := newAgent(t, pm)
	a.SkipAgentsMD = true
	a.Tools.Delegate = true
	a.Subagents = &Subagents{Model: cm, ModelName: "child-model"}
	return a, pm, cm
}

func TestDelegateOffByDefault(t *testing.T) {
	m := &specModel{scripted: scripted{replies: []model.Message{
		assistantCalls(delegateCall("1", "look around")),
		{Role: "assistant", Content: "done"},
	}}}
	a := newAgent(t, m)
	var kinds []string
	a.OnEvent = func(kind string, _ map[string]any) { kinds = append(kinds, kind) }
	tr := a.Run(context.Background(), "x")
	if slices.Contains(toolNames(m.specs[0]), "delegate") {
		t.Errorf("delegate advertised by default: %v", toolNames(m.specs[0]))
	}
	res := toolResults(tr.Messages)
	if len(res) != 1 || !strings.HasPrefix(res[0], "format error: unknown tool") {
		t.Errorf("results = %q", res)
	}
	if tr.SubagentsEnabled || tr.Subagents != nil || tr.SubagentSteps != 0 {
		t.Errorf("trajectory has subagent fields: %+v", tr)
	}
	b, _ := json.Marshal(tr)
	if strings.Contains(string(b), "subagent") {
		t.Errorf("trajectory JSON mentions subagents: %s", b)
	}
	for _, k := range kinds {
		if strings.HasPrefix(k, "subagent.") {
			t.Errorf("event %s emitted", k)
		}
	}
}

func TestDelegateAdvertisedInNormalAndReadOnlyRuns(t *testing.T) {
	for _, ro := range []bool{false, true} {
		e := &tools.Executor{Root: t.TempDir(), Delegate: true, ReadOnly: ro}
		if !slices.Contains(toolNames(e.Catalog()), "delegate") {
			t.Errorf("read-only=%v: catalog %v lacks delegate", ro, toolNames(e.Catalog()))
		}
	}
	a, pm, _ := subagentAgent(t, []model.Message{{Role: "assistant", Content: "done"}}, nil)
	a.Tools.ReadOnly = true
	tr := a.Run(context.Background(), "x")
	if !slices.Contains(toolNames(pm.specs[0]), "delegate") || !tr.SubagentsEnabled {
		t.Errorf("specs %v, enabled %v", toolNames(pm.specs[0]), tr.SubagentsEnabled)
	}
}

func TestDelegateIsolationResultAndAccounting(t *testing.T) {
	a, pm, cm := subagentAgent(t,
		[]model.Message{
			{Role: "assistant", Content: "parent thinking", ToolCalls: []model.ToolCall{delegateCall("p1", "Find where Timeout is read.")}},
			{Role: "assistant", Content: "fixed" + submitMarker},
		},
		[]model.Message{
			assistantCalls(call("c1", "grep", `{"pattern":"Timeout"}`)),
			{Role: "assistant", Content: "Timeout is read in a.go:1." + submitMarker},
		})
	os.WriteFile(filepath.Join(a.Tools.Root, "a.go"), []byte("cfg.Timeout\n"), 0o644)
	tr := a.Run(context.Background(), "fix the bug")
	if tr.ExitStatus != StatusSubmitted {
		t.Fatalf("status = %s", tr.ExitStatus)
	}
	// The child's first request is its own system prompt plus the task only.
	first := cm.seen[0]
	if len(first) != 2 || first[0].Role != "system" || !strings.Contains(first[0].Content, subagentNotice) ||
		first[1].Role != "user" || first[1].Content != "Find where Timeout is read." {
		t.Fatalf("child first request = %+v", first)
	}
	if strings.Contains(first[0].Content, "parent thinking") || strings.Contains(tr.SystemPrompt, subagentNotice) {
		t.Error("parent context leaked into the child or the notice into the parent")
	}
	// The child gets exactly the read-only tools.
	if got := toolNames(cm.specs[0]); !slices.Equal(got, []string{"read_file", "list_files", "grep"}) {
		t.Errorf("child tools = %v", got)
	}
	// The parent's next request has the delegate call and result, nothing else.
	next := pm.seen[1]
	if len(next) != 4 || next[3].Role != "tool" || next[3].ToolCallID != "p1" {
		t.Fatalf("parent second request = %+v", next)
	}
	for _, m := range next {
		if m.ToolCallID == "c1" || strings.Contains(m.Content, "cfg.Timeout") {
			t.Errorf("child message leaked into parent: %+v", m)
		}
	}
	want := "Timeout is read in a.go:1.\n\n[delegate d1: Submitted, 2 steps, 200 in / 40 out tokens, $0.0200]"
	if next[3].Content != want {
		t.Errorf("result = %q, want %q", next[3].Content, want)
	}
	// Accounting: 2 parent + 2 child calls, but only the parent's count as steps.
	if tr.Steps != 2 || tr.SubagentSteps != 2 || tr.InputTokens != 400 || tr.OutputTokens != 80 || tr.Cost < 0.0399 || tr.Cost > 0.0401 {
		t.Errorf("steps %d, subagent steps %d, tokens %d/%d, cost %v", tr.Steps, tr.SubagentSteps, tr.InputTokens, tr.OutputTokens, tr.Cost)
	}
	if len(tr.Subagents) != 1 {
		t.Fatalf("subagents = %+v", tr.Subagents)
	}
	s := tr.Subagents[0]
	if s.ID != "d1" || s.ToolCallID != "p1" || s.Status != StatusSubmitted || s.Steps != 2 || s.Model != "child-model" ||
		s.Report != "Timeout is read in a.go:1." || len(s.Messages) != 5 || s.ParentStep != 1 {
		t.Errorf("record = %+v", s)
	}
}

func TestDelegateChildIsReadOnly(t *testing.T) {
	a, _, cm := subagentAgent(t,
		[]model.Message{assistantCalls(delegateCall("p1", "try to write")), {Role: "assistant", Content: "done"}},
		[]model.Message{
			assistantCalls(
				call("w", "write_file", `{"path":"w.txt","content":"x"}`),
				call("e", "edit_file", `{"path":"keep.txt","old":"keep","new":"gone"}`),
				call("b", "bash", `{"command":"touch b.txt"}`),
				call("s", "start_process", `{"command":"touch s.txt"}`),
				call("p", "apply_patch", `{"patch":"*** Begin Patch\n*** Add File: p.txt\n+x\n*** End Patch"}`),
				call("m", "docs__search", `{"q":"x"}`),
				call("d", "delegate", `{"task":"nested"}`),
			),
			{Role: "assistant", Content: "could not write"},
		})
	a.Tools.ApplyPatch = true
	a.Tools.ExternalSpecs = []model.ToolSpec{{Type: "function", Function: model.FunctionSpec{Name: "docs__search"}}}
	a.Tools.ExternalCall = func(context.Context, string, string) (string, bool, bool) {
		t.Error("child reached an MCP tool")
		return "", false, true
	}
	os.WriteFile(filepath.Join(a.Tools.Root, "keep.txt"), []byte("keep"), 0o644)
	tr := a.Run(context.Background(), "x")
	if len(cm.seen) != 2 || len(tr.Subagents) != 1 {
		t.Fatalf("child requests %d, subagents %d", len(cm.seen), len(tr.Subagents))
	}
	res := toolResults(tr.Subagents[0].Messages)
	if len(res) != 7 {
		t.Fatalf("child results = %q", res)
	}
	for i, r := range res {
		if !strings.Contains(r, "read-only mode") && !strings.HasPrefix(r, "format error: unknown tool") {
			t.Errorf("child call %d not denied: %q", i, r)
		}
	}
	for _, f := range []string{"w.txt", "b.txt", "s.txt", "p.txt"} {
		if _, err := os.Stat(filepath.Join(a.Tools.Root, f)); err == nil {
			t.Errorf("%s was created", f)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(a.Tools.Root, "keep.txt")); string(b) != "keep" {
		t.Errorf("keep.txt = %q", b)
	}
}

func TestDelegateChildPathRulesMatchParent(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "outside.txt")
	os.WriteFile(outside, []byte("s3cret"), 0o644)
	calls := []model.ToolCall{
		call("c1", "read_file", `{"path":"../outside"}`),
		call("c2", "read_file", `{"path":`+string(must(json.Marshal(outside)))+`}`),
	}
	a, _, _ := subagentAgent(t,
		[]model.Message{assistantCalls(delegateCall("p1", "read outside")), {Role: "assistant", Content: "done"}},
		[]model.Message{assistantCalls(calls...), {Role: "assistant", Content: "no access"}})
	tr := a.Run(context.Background(), "x")
	res := toolResults(tr.Subagents[0].Messages)
	parent := &tools.Executor{Root: a.Tools.Root}
	for i, c := range calls {
		want := parent.RunResult(context.Background(), c)
		if i >= len(res) || res[i] != want.Text || !strings.HasPrefix(want.Text, "error: ") || strings.Contains(res[i], "s3cret") {
			t.Errorf("call %s: child %q, parent %q", c.ID, res, want.Text)
		}
	}
}

func must(b []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return b
}

func TestDelegateReportHonorsMaxOutputChars(t *testing.T) {
	long := strings.Repeat("r", 5000)
	a, pm, _ := subagentAgent(t,
		[]model.Message{assistantCalls(delegateCall("p1", "report a lot")), {Role: "assistant", Content: "done"}},
		[]model.Message{{Role: "assistant", Content: long}})
	a.Tools.MaxOutputChars = 500
	a.Run(context.Background(), "x")
	got := pm.seen[1][3].Content
	if len(got) > 800 || !strings.Contains(got, "characters elided") || !strings.HasSuffix(got, "$0.0100]") {
		t.Errorf("result (%d chars) = %q", len(got), got)
	}
}

func TestDelegateStepCapIsAToolError(t *testing.T) {
	loop := assistantCalls(call("c", "list_files", `{}`))
	a, pm, cm := subagentAgent(t,
		[]model.Message{assistantCalls(delegateCall("p1", "never finish")), {Role: "assistant", Content: "done" + submitMarker}},
		[]model.Message{loop, loop, loop, loop})
	a.Subagents.MaxSteps = 2
	tr := a.Run(context.Background(), "x")
	if len(cm.seen) != 2 {
		t.Errorf("child made %d calls, want 2", len(cm.seen))
	}
	if tr.ExitStatus != StatusSubmitted || len(pm.seen) != 2 {
		t.Fatalf("parent status %s after %d requests", tr.ExitStatus, len(pm.seen))
	}
	res := pm.seen[1][3].Content
	if !strings.HasPrefix(res, "error: delegate d1 did not finish: StepLimitExceeded") || tr.Subagents[0].Status != StatusStepLimit {
		t.Errorf("result = %q", res)
	}
	if tr.Messages[3].Role != "tool" || !strings.Contains(tr.Messages[3].Content, "StepLimitExceeded") {
		t.Errorf("parent messages = %+v", tr.Messages)
	}
}

func TestDelegateDefaultStepCap(t *testing.T) {
	if (&Subagents{}).maxSteps() != config.DefaultSubagentMaxSteps || (&Subagents{MaxSteps: 3}).maxSteps() != 3 {
		t.Error("maxSteps default")
	}
}

func TestDelegateCostLimitEndsParent(t *testing.T) {
	loop := assistantCalls(call("c", "list_files", `{}`))
	a, pm, cm := subagentAgent(t,
		[]model.Message{assistantCalls(delegateCall("p1", "spend")), {Role: "assistant", Content: "done"}},
		[]model.Message{loop, loop, loop, loop})
	a.Config.CostLimit = 0.025
	tr := a.Run(context.Background(), "x")
	if tr.ExitStatus != StatusCostLimit {
		t.Fatalf("status = %s", tr.ExitStatus)
	}
	// Parent 0.01, child 0.01 (0.02 < 0.025), child 0.01 (0.03): then stop.
	if len(cm.seen) != 2 || len(pm.seen) != 1 {
		t.Errorf("child requests %d, parent requests %d", len(cm.seen), len(pm.seen))
	}
	if tr.Cost < 0.0299 || tr.Cost > 0.0301 || tr.Subagents[0].Status != StatusCostLimit {
		t.Errorf("cost %v, record %+v", tr.Cost, tr.Subagents[0])
	}
}

func TestDelegateNotStartedWhenBudgetSpent(t *testing.T) {
	a, _, cm := subagentAgent(t,
		[]model.Message{assistantCalls(delegateCall("p1", "spend")), {Role: "assistant", Content: "done"}}, nil)
	a.Config.CostLimit = 0.01
	tr := a.Run(context.Background(), "x")
	if tr.ExitStatus != StatusCostLimit || len(cm.seen) != 0 {
		t.Errorf("status %s, child requests %d", tr.ExitStatus, len(cm.seen))
	}
}

// cancelModel cancels the run during its first request.
type cancelModel struct {
	scripted
	cancel func()
}

func (c *cancelModel) Query(ctx context.Context, msgs []model.Message, specs []model.ToolSpec) (model.Message, model.Cost, error) {
	c.cancel()
	return c.scripted.Query(ctx, msgs, specs)
}

func TestDelegateInterruptLeavesCallPendingAndResumes(t *testing.T) {
	a, _, _ := subagentAgent(t,
		[]model.Message{assistantCalls(delegateCall("p1", "look"), call("p2", "list_files", `{}`))}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	a.Subagents.Model = &cancelModel{scripted: scripted{replies: []model.Message{assistantCalls(call("c", "list_files", `{}`))}}, cancel: cancel}
	tr := a.Run(ctx, "x")
	if tr.ExitStatus != StatusInterrupted {
		t.Fatalf("status = %s", tr.ExitStatus)
	}
	if len(tr.Subagents) != 1 || tr.Subagents[0].Status != StatusInterrupted {
		t.Fatalf("subagents = %+v", tr.Subagents)
	}
	if res := toolResults(tr.Messages); len(res) != 0 {
		t.Fatalf("delegate result recorded before interruption: %q", res)
	}

	rm := &scripted{replies: []model.Message{{Role: "assistant", Content: "resumed" + submitMarker}}}
	r := &Agent{Model: rm, ModelName: "fake", Tools: &tools.Executor{Root: a.Tools.Root, Delegate: true}, Config: a.Config,
		SkipAgentsMD: true, Resume: tr, Subagents: &Subagents{Model: &scripted{}, ModelName: "child-model"}}
	got := r.Run(context.Background(), "")
	if got.ExitStatus != StatusSubmitted {
		t.Fatalf("resume status = %s", got.ExitStatus)
	}
	res := toolResults(got.Messages)
	if len(res) < 1 || !strings.Contains(strings.ToLower(res[0]), "interrupted") {
		t.Errorf("results after resume = %q", res)
	}
}

// blockModel waits for the run's deadline.
type blockModel struct{ calls int }

func (b *blockModel) Query(ctx context.Context, _ []model.Message, _ []model.ToolSpec) (model.Message, model.Cost, error) {
	b.calls++
	<-ctx.Done()
	return model.Message{}, model.Cost{}, ctx.Err()
}

func TestDelegateWallTimeEndsParent(t *testing.T) {
	a, _, _ := subagentAgent(t, []model.Message{assistantCalls(delegateCall("p1", "slow"))}, nil)
	bm := &blockModel{}
	a.Subagents.Model = bm
	a.Config.WallTimeSec = 1
	tr := a.Run(context.Background(), "x")
	if tr.ExitStatus != StatusTimeLimit || bm.calls != 1 || len(tr.Subagents) != 1 || tr.Subagents[0].Status != StatusTimeLimit {
		t.Fatalf("status %s, child calls %d, subagents %+v", tr.ExitStatus, bm.calls, tr.Subagents)
	}
}

func TestDelegateEmptyTaskSendsNoRequest(t *testing.T) {
	a, _, cm := subagentAgent(t,
		[]model.Message{assistantCalls(call("p1", "delegate", `{"task":"  "}`), call("p2", "delegate", `{}`)), {Role: "assistant", Content: "done"}}, nil)
	tr := a.Run(context.Background(), "x")
	res := toolResults(tr.Messages)
	if len(cm.seen) != 0 || len(res) != 2 || !strings.Contains(res[0], "non-empty task") || !strings.Contains(res[1], `missing required field "task"`) {
		t.Errorf("child requests %d, results %q", len(cm.seen), res)
	}
	if len(tr.Subagents) != 0 {
		t.Errorf("subagents = %+v", tr.Subagents)
	}
}

func TestDelegateMultipleCallsRunInOrder(t *testing.T) {
	a, pm, cm := subagentAgent(t,
		[]model.Message{assistantCalls(delegateCall("p1", "task one"), delegateCall("p2", "task two")), {Role: "assistant", Content: "done"}},
		[]model.Message{{Role: "assistant", Content: "report one"}, {Role: "assistant", Content: "report two"}})
	tr := a.Run(context.Background(), "x")
	if len(cm.seen) != 2 || len(cm.seen[1]) != 2 || cm.seen[0][1].Content != "task one" || cm.seen[1][1].Content != "task two" {
		t.Fatalf("child requests = %+v", cm.seen)
	}
	next := pm.seen[1]
	if len(next) != 5 || !strings.HasPrefix(next[3].Content, "report one\n\n[delegate d1:") || !strings.HasPrefix(next[4].Content, "report two\n\n[delegate d2:") {
		t.Errorf("parent request = %+v", next)
	}
	if len(tr.Subagents) != 2 || tr.Subagents[1].ID != "d2" || tr.Subagents[1].ToolCallID != "p2" {
		t.Errorf("subagents = %+v", tr.Subagents)
	}
}

func TestDelegateHooksCoverChildCalls(t *testing.T) {
	a, _, _ := subagentAgent(t,
		[]model.Message{assistantCalls(delegateCall("p1", "read secret")), {Role: "assistant", Content: "done"}},
		[]model.Message{assistantCalls(call("c1", "read_file", `{"path":"secret.txt"}`)), {Role: "assistant", Content: "denied"}})
	os.WriteFile(filepath.Join(a.Tools.Root, "secret.txt"), []byte("s3cret"), 0o644)
	dir := t.TempDir()
	log := filepath.Join(t.TempDir(), "log")
	withHooks(t, a, config.Hooks{
		PreTool:  []config.Hook{hookstest.Hook(t, "^read_file$", "gate", dir, "99", "no secrets"), hookstest.Hook(t, "^delegate$", "log", log, "pre-delegate")},
		PostTool: []config.Hook{hookstest.Hook(t, "^delegate$", "log", log, "post-delegate")},
		Stop:     []config.Hook{hookstest.Hook(t, "", "log", log, "stop")},
	})
	tr := a.Run(context.Background(), "x")
	res := toolResults(tr.Subagents[0].Messages)
	if len(res) != 1 || !strings.Contains(res[0], "denied by pre_tool hook") || strings.Contains(res[0], "s3cret") {
		t.Fatalf("child results = %q", res)
	}
	b, err := os.ReadFile(filepath.Join(dir, "0.json"))
	if err != nil {
		t.Fatal(err)
	}
	var in map[string]any
	json.Unmarshal(b, &in)
	if in["subagent"] != "d1" || in["tool_name"] != "read_file" {
		t.Errorf("hook input = %s", b)
	}
	// Hooks run for the delegate call itself; stop hooks only for the parent.
	if b, _ := os.ReadFile(log); string(b) != "pre-delegate\npost-delegate\nstop\n" {
		t.Errorf("hook log = %q", b)
	}
}

func TestDelegateEvents(t *testing.T) {
	a, _, _ := subagentAgent(t,
		[]model.Message{assistantCalls(delegateCall("p1", "look")), {Role: "assistant", Content: "done"}},
		[]model.Message{assistantCalls(call("c1", "list_files", `{}`)), {Role: "assistant", Content: "report"}})
	type ev struct {
		kind   string
		fields map[string]any
	}
	var got []ev
	a.OnEvent = func(kind string, fields map[string]any) { got = append(got, ev{kind, fields}) }
	var errBuf bytes.Buffer
	a.Err, a.Verbose, a.Out = &errBuf, true, &bytes.Buffer{}
	a.Run(context.Background(), "x")
	var seq []string
	for _, e := range got {
		k := e.kind
		if e.fields["subagent"] == "d1" {
			k = "child:" + k
		}
		seq = append(seq, k)
	}
	want := "model.request,tool.call,subagent.started,child:model.request,child:tool.call,child:tool.result,child:model.request,child:assistant.message,subagent.finished,tool.result,model.request,assistant.message"
	if strings.Join(seq, ",") != want {
		t.Errorf("events = %s", strings.Join(seq, ","))
	}
	for _, e := range got {
		switch e.kind {
		case "subagent.started":
			if e.fields["id"] != "d1" || e.fields["model"] != "child-model" || e.fields["parent_step"] != 1 || e.fields["tool_call_id"] != "p1" {
				t.Errorf("started = %v", e.fields)
			}
		case "subagent.finished":
			if e.fields["status"] != StatusCompleted || e.fields["steps"] != 2 || e.fields["input_tokens"] != 200 {
				t.Errorf("finished = %v", e.fields)
			}
		}
	}
	if !strings.Contains(errBuf.String(), "delegate d1 started (child-model)") || !strings.Contains(errBuf.String(), "delegate d1 finished: delegate d1: Completed") {
		t.Errorf("stderr = %q", errBuf.String())
	}
}

func TestDelegateWithOutputSchema(t *testing.T) {
	a, pm := schemaAgent(t,
		assistantCalls(delegateCall("p1", "look")),
		submitCall("s", `{"verdict":"pass"}`))
	a.Tools.Delegate = true
	csm := &specModel{scripted: scripted{replies: []model.Message{{Role: "assistant", Content: "report"}}}}
	a.Subagents = &Subagents{Model: csm, ModelName: "child"}
	tr := a.Run(context.Background(), "x")
	if tr.ExitStatus != StatusSubmitted || tr.StructuredOutput == nil {
		t.Fatalf("status %s, output %s", tr.ExitStatus, tr.StructuredOutput)
	}
	if slices.Contains(toolNames(csm.specs[0]), SubmitToolName) || !slices.Contains(toolNames(pm.specs[0]), SubmitToolName) {
		t.Errorf("child tools %v, parent tools %v", toolNames(csm.specs[0]), toolNames(pm.specs[0]))
	}
}

func TestResumeRefusesChangedSubagentsSetting(t *testing.T) {
	a, _, _ := subagentAgent(t, []model.Message{assistantCalls(call("1", "list_files", `{}`))}, nil)
	a.Config.StepLimit = 1
	tr := a.Run(context.Background(), "x")
	if !tr.SubagentsEnabled || tr.ExitStatus != StatusStepLimit {
		t.Fatalf("trajectory = %+v", tr)
	}
	if err := ValidateResume(tr, a.Config, a.Tools.Root); err != nil {
		t.Errorf("ValidateResume same setting: %v", err)
	}
	off := &Agent{Model: &scripted{}, ModelName: "fake", Tools: &tools.Executor{Root: a.Tools.Root}, Config: a.Config, Resume: tr}
	got := off.Run(context.Background(), "")
	if got.ExitStatus != StatusError || !strings.Contains(got.Submission, "subagents setting differs") {
		t.Errorf("resume = %s %q", got.ExitStatus, got.Submission)
	}
	if (tools.Selection{Delegate: true}).Signature() == (tools.Selection{}).Signature() {
		t.Error("signature ignores the subagents setting")
	}
}
