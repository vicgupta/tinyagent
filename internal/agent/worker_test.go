package agent

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/hooks/hookstest"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/tools"
)

func workerCallMsg(id, task string) model.ToolCall {
	b, _ := json.Marshal(map[string]string{"task": task, "label": "fix"})
	return call(id, tools.WorkerToolName, string(b))
}

func applyCallMsg(id, worker string) model.ToolCall {
	b, _ := json.Marshal(map[string]string{"worker": worker})
	return call(id, tools.ApplyWorkerToolName, string(b))
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// workerRepo returns a Git repository with one commit holding a.txt.
func workerRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "init", "-q")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644)
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "init")
	return dir
}

// workerAgent returns a parent with workers on in a fresh repository, its
// scripted model, and the workers' scripted model.
func workerAgent(t *testing.T, parent, child []model.Message) (*Agent, *specModel, *specModel) {
	t.Helper()
	pm := &specModel{scripted: scripted{replies: parent}}
	cm := &specModel{scripted: scripted{replies: child}}
	a := &Agent{Model: pm, ModelName: "fake", Tools: &tools.Executor{Root: workerRepo(t), Workers: true},
		Config: config.Default(), SkipAgentsMD: true}
	a.Workers = &Workers{Model: cm, ModelName: "worker-model", RunName: "test"}
	return a, pm, cm
}

func TestWorkersOffByDefault(t *testing.T) {
	e := &tools.Executor{Root: t.TempDir()}
	names := toolNames(e.Catalog())
	if slices.Contains(names, "worker") || slices.Contains(names, "apply_worker") {
		t.Errorf("worker tools advertised by default: %v", names)
	}
	e = &tools.Executor{Root: t.TempDir(), Workers: true, ReadOnly: true}
	if slices.Contains(toolNames(e.Catalog()), "worker") {
		t.Error("worker advertised in read-only mode")
	}
	e = &tools.Executor{Root: t.TempDir(), Workers: true}
	if names := toolNames(e.Catalog()); !slices.Contains(names, "worker") || !slices.Contains(names, "apply_worker") {
		t.Errorf("catalog with workers lacks them: %v", names)
	}
}

func TestOnlyRestrictsCatalogAndDispatch(t *testing.T) {
	e := &tools.Executor{Root: t.TempDir(), Only: []string{"read_file", "write_file"}}
	if names := toolNames(e.Catalog()); !slices.Equal(names, []string{"read_file", "write_file"}) {
		t.Errorf("catalog = %v", names)
	}
	res := e.RunResult(context.Background(), call("1", "bash", `{"command":"echo hi"}`))
	if !res.IsError || !strings.Contains(res.Text, "not available to this worker") {
		t.Errorf("bash result = %+v", res)
	}
	if e.Permits("bash") {
		t.Error("Permits(bash) with Only excluding it")
	}
}

func TestWorkerCommitsAndApplies(t *testing.T) {
	a, pm, cm := workerAgent(t,
		[]model.Message{
			assistantCalls(workerCallMsg("p1", "append a line to a.txt and add b.txt")),
			assistantCalls(applyCallMsg("p2", "w1")),
			{Role: "assistant", Content: "applied" + submitMarker},
		},
		[]model.Message{
			assistantCalls(call("c1", "write_file", `{"path":"a.txt","content":"one\ntwo\n"}`)),
			assistantCalls(call("c2", "write_file", `{"path":"b.txt","content":"new\n"}`)),
			{Role: "assistant", Content: "Appended two; added b.txt." + submitMarker},
		})
	root := a.Tools.Root
	os.WriteFile(filepath.Join(root, "dirty.txt"), []byte("uncommitted\n"), 0o644)
	hookDir := t.TempDir()
	withHooks(t, a, config.Hooks{PreTool: []config.Hook{hookstest.Hook(t, "^write_file$", "gate", hookDir, "0", "")}})
	var events []string
	var finished map[string]any
	a.OnEvent = func(kind string, f map[string]any) {
		events = append(events, kind)
		if kind == "subagent.finished" {
			finished = f
		}
	}
	tr := a.Run(context.Background(), "do it")
	if tr.ExitStatus != StatusSubmitted {
		t.Fatalf("exit = %s %s", tr.ExitStatus, tr.Submission)
	}
	if !tr.WorkersEnabled {
		t.Error("WorkersEnabled not recorded")
	}
	if names := toolNames(cm.specs[0]); slices.Contains(names, "worker") || slices.Contains(names, "delegate") || slices.Contains(names, "ask_user") {
		t.Errorf("worker tools = %v", names)
	} else if !slices.Contains(names, "bash") || !slices.Contains(names, "write_file") {
		t.Errorf("worker lacks default tools: %v", names)
	}
	if sys := cm.seen[0][0].Content; !strings.Contains(sys, "worker subagent") {
		t.Errorf("worker system prompt lacks the worker notice")
	}
	_ = pm
	res := toolResults(tr.Messages)
	if len(res) != 2 {
		t.Fatalf("results = %q", res)
	}
	if !strings.Contains(res[0], "Appended two") || !strings.Contains(res[0], "[worker w1: Submitted · branch tinyagent/test-w1 · commit ") ||
		!strings.Contains(res[0], "2 files changed") || !strings.Contains(res[0], "b.txt") {
		t.Errorf("worker result = %q", res[0])
	}
	if !strings.Contains(res[1], "applied worker w1") {
		t.Errorf("apply result = %q", res[1])
	}
	if b, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(b) != "one\ntwo\n" {
		t.Errorf("a.txt = %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "b.txt")); string(b) != "new\n" {
		t.Errorf("b.txt = %q", b)
	}
	if got := gitIn(t, root, "diff", "--cached", "--name-only"); got != "" {
		t.Errorf("index changed: %q", got)
	}
	if len(tr.Subagents) != 1 {
		t.Fatalf("subagents = %+v", tr.Subagents)
	}
	w := tr.Subagents[0]
	if w.Kind != "worker" || w.ID != "w1" || w.Branch != "tinyagent/test-w1" || w.Commit == "" || w.BaseCommit == "" || !w.Applied {
		t.Errorf("record = %+v", w)
	}
	// The worker started from the snapshot, which had the dirty file.
	if got := gitIn(t, root, "show", w.BaseCommit+":dirty.txt"); got != "uncommitted" {
		t.Errorf("base dirty.txt = %q", got)
	}
	if !strings.Contains(gitIn(t, root, "log", "-1", "--format=%s", w.Commit), "tinyagent worker w1: fix [Submitted]") {
		t.Errorf("commit subject = %q", gitIn(t, root, "log", "-1", "--format=%s", w.Commit))
	}
	if _, err := os.Stat(filepath.Join(root, ".tinyagent/worktrees/test-w1")); err != nil {
		t.Errorf("worktree not kept: %v", err)
	}
	if finished["kind"] != "worker" || finished["branch"] != "tinyagent/test-w1" || finished["files_changed"] != 2 {
		t.Errorf("finished event = %v", finished)
	}
	if !slices.Contains(events, "worker.applied") {
		t.Errorf("events = %v", events)
	}
	b, err := os.ReadFile(filepath.Join(hookDir, "0.json"))
	if err != nil {
		t.Fatal(err)
	}
	var in map[string]any
	json.Unmarshal(b, &in)
	if in["subagent"] != "w1" || in["worker"] != true || in["root"] == root {
		t.Errorf("hook input = %v", in)
	}
	// Re-applying is refused.
	r2 := a.applyWorker(tr, applyCallMsg("x", "w1"))
	if !r2.IsError || !strings.Contains(r2.Text, "already applied") {
		t.Errorf("second apply = %+v", r2)
	}
}

func TestWorkerNoChangesRemovesWorktree(t *testing.T) {
	a, _, _ := workerAgent(t,
		[]model.Message{
			assistantCalls(workerCallMsg("p1", "look only")),
			assistantCalls(applyCallMsg("p2", "w1")),
			{Role: "assistant", Content: "ok" + submitMarker},
		},
		[]model.Message{{Role: "assistant", Content: "nothing to do" + submitMarker}})
	tr := a.Run(context.Background(), "x")
	res := toolResults(tr.Messages)
	if len(res) != 2 || !strings.Contains(res[0], "no changes (worktree and branch removed)") {
		t.Fatalf("results = %q", res)
	}
	if !strings.Contains(res[1], "made no changes") {
		t.Errorf("apply result = %q", res[1])
	}
	if _, err := os.Stat(filepath.Join(a.Tools.Root, ".tinyagent/worktrees/test-w1")); err == nil {
		t.Error("worktree kept")
	}
	if strings.Contains(gitIn(t, a.Tools.Root, "branch", "--list"), "tinyagent/") {
		t.Error("branch kept")
	}
}

func TestApplyWorkerConflictChangesNothing(t *testing.T) {
	a, _, _ := workerAgent(t,
		[]model.Message{
			assistantCalls(workerCallMsg("p1", "rewrite a.txt")),
			assistantCalls(call("p2", "write_file", `{"path":"a.txt","content":"parent\n"}`)),
			assistantCalls(applyCallMsg("p3", "w1")),
			{Role: "assistant", Content: "ok" + submitMarker},
		},
		[]model.Message{
			assistantCalls(call("c1", "write_file", `{"path":"a.txt","content":"worker\n"}`)),
			{Role: "assistant", Content: "rewrote" + submitMarker},
		})
	tr := a.Run(context.Background(), "x")
	res := toolResults(tr.Messages)
	if len(res) != 3 || !strings.Contains(res[2], "does not apply cleanly") || !strings.Contains(res[2], "a.txt") || !strings.Contains(res[2], "tinyagent/test-w1") {
		t.Fatalf("results = %q", res)
	}
	if b, _ := os.ReadFile(filepath.Join(a.Tools.Root, "a.txt")); string(b) != "parent\n" {
		t.Errorf("a.txt = %q", b)
	}
}

func TestApplyWorkerRespectsDenyWrite(t *testing.T) {
	a, _, _ := workerAgent(t,
		[]model.Message{
			assistantCalls(workerCallMsg("p1", "edit")),
			assistantCalls(applyCallMsg("p2", "w1")),
			{Role: "assistant", Content: "ok" + submitMarker},
		},
		[]model.Message{
			assistantCalls(call("c1", "bash", `{"command":"echo x > secret.txt"}`)),
			{Role: "assistant", Content: "done" + submitMarker},
		})
	fa, err := tools.NewFileAccess(a.Tools.Root, nil, []string{"secret.txt"})
	if err != nil {
		t.Fatal(err)
	}
	a.Tools.FileAccess = fa
	tr := a.Run(context.Background(), "x")
	res := toolResults(tr.Messages)
	if len(res) != 2 || !strings.Contains(res[1], "nothing was applied") {
		t.Fatalf("results = %q", res)
	}
	if _, err := os.Stat(filepath.Join(a.Tools.Root, "secret.txt")); err == nil {
		t.Error("denied file was applied")
	}
}

func TestWorkerToolsIntersectParent(t *testing.T) {
	a, _, cm := workerAgent(t,
		[]model.Message{assistantCalls(workerCallMsg("p1", "x")), {Role: "assistant", Content: "ok" + submitMarker}},
		[]model.Message{{Role: "assistant", Content: "r" + submitMarker}})
	a.Workers.Tools = []string{"read_file", "web_fetch", "edit_file"}
	a.Run(context.Background(), "x")
	if names := toolNames(cm.specs[0]); !slices.Equal(names, []string{"read_file", "edit_file"}) {
		t.Errorf("worker tools = %v", names)
	}
}

func TestWorkerStepLimitKeepsPartialWork(t *testing.T) {
	loop := assistantCalls(call("c", "write_file", `{"path":"part.txt","content":"partial\n"}`))
	a, _, _ := workerAgent(t,
		[]model.Message{assistantCalls(workerCallMsg("p1", "never finish")), {Role: "assistant", Content: "ok" + submitMarker}},
		[]model.Message{loop, loop, loop})
	a.Workers.MaxSteps = 2
	tr := a.Run(context.Background(), "x")
	res := toolResults(tr.Messages)
	if len(res) != 1 || !strings.Contains(res[0], "did not finish") || !strings.Contains(res[0], "commit ") {
		t.Fatalf("results = %q", res)
	}
	w := tr.Subagents[0]
	if w.Commit == "" || !strings.Contains(gitIn(t, a.Tools.Root, "show", w.Commit+":part.txt"), "partial") {
		t.Errorf("partial work not committed: %+v", w)
	}
}

func TestWorkerResumeSettingMustMatch(t *testing.T) {
	a, _, _ := workerAgent(t, []model.Message{{Role: "assistant", Content: "done" + submitMarker}}, nil)
	tr := a.Run(context.Background(), "x")
	r := &Agent{Model: &scripted{}, ModelName: "fake", Tools: &tools.Executor{Root: a.Tools.Root}, Config: a.Config,
		SkipAgentsMD: true, Resume: tr, FollowUp: "more"}
	got := r.Run(context.Background(), "")
	if got.ExitStatus != StatusError || !strings.Contains(got.Submission, "workers setting") {
		t.Errorf("resume without workers = %s %q", got.ExitStatus, got.Submission)
	}
}

// A workers checkpoint offers worker and apply_worker; its recorded tool
// list must validate against the same selection (issue #306).
func TestWorkerCheckpointValidates(t *testing.T) {
	a, _, _ := workerAgent(t, []model.Message{{Role: "assistant", Content: "done" + submitMarker}}, nil)
	tr := a.Run(context.Background(), "x")
	if !tr.WorkersEnabled || !slices.Contains(tr.Tools, tools.WorkerToolName) {
		t.Fatalf("checkpoint did not record workers: %v", tr.Tools)
	}
	if err := ValidateResume(tr, a.Config, a.Tools.Root); err != nil {
		t.Errorf("ValidateResume = %v", err)
	}
}
