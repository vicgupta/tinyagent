package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/gitwt"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/tools"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// Workers configures the children the opt-in worker tool runs.
type Workers struct {
	// Model and ModelName are the workers' model (by default the parent's
	// model spec, without streaming).
	Model     model.Model
	ModelName string
	// MaxSteps caps each worker's model calls; 0 means
	// config.DefaultWorkerMaxSteps.
	MaxSteps int
	// Tools are the configured worker tools; nil means
	// config.DefaultWorkerTools. A worker gets only those the parent has.
	Tools []string
	// RunName prefixes worker branch and worktree names
	// (tinyagent/<RunName>-w<n>); "" generates one per process.
	RunName string
}

func (w *Workers) maxSteps() int {
	if w.MaxSteps > 0 {
		return w.MaxSteps
	}
	return config.DefaultWorkerMaxSteps
}

// runName returns RunName, generating it on first use.
func (w *Workers) runName() string {
	if w.RunName == "" {
		b := make([]byte, 3)
		rand.Read(b)
		w.RunName = "run-" + time.Now().Format("20060102-150405") + "-" + hex.EncodeToString(b)
	}
	return w.RunName
}

// workerNotice ends a worker's system prompt.
const workerNotice = "You are a worker subagent working for another agent, in an isolated Git checkout that is a snapshot of its working tree. " +
	"Complete only the described task, verify it where you can, and finish with a concise report of what you changed and how you verified it. " +
	"Do not commit, push or create branches: tinyagent commits your changes for you when you finish. " +
	"Do not ask questions; if something cannot be done, say so in the report."

// workerForbidden are tools a worker never gets, whatever the config says.
var workerForbidden = map[string]bool{
	tools.DelegateToolName: true, tools.WorkerToolName: true, tools.ApplyWorkerToolName: true,
	tools.AskUserToolName: true, tools.PlanToolName: true,
}

// workersOn reports whether this agent offers the worker tools.
func (a *Agent) workersOn() bool {
	return a.Tools != nil && a.Tools.Workers && !a.Tools.ReadOnly
}

// workerCall runs a worker or apply_worker call; ok is false for any other
// call.
func (a *Agent) workerCall(ctx context.Context, t *trajectory.Trajectory, call model.ToolCall) (tools.Result, bool) {
	if !a.workersOn() {
		return tools.Result{}, false
	}
	switch call.Function.Name {
	case tools.WorkerToolName:
		return a.worker(ctx, t, call), true
	case tools.ApplyWorkerToolName:
		return a.applyWorker(t, call), true
	}
	return tools.Result{}, false
}

func toolErr(format string, args ...any) tools.Result {
	return tools.Result{Text: "error: " + fmt.Sprintf(format, args...), IsError: true}
}

// workerTools returns the configured worker tools the parent also has.
func (a *Agent) workerTools() []string {
	want := a.Workers.Tools
	if len(want) == 0 {
		want = config.DefaultWorkerTools
	}
	have := map[string]bool{}
	for _, s := range a.Tools.Catalog() {
		have[s.Function.Name] = true
	}
	var out []string
	for _, n := range want {
		if have[n] && !workerForbidden[n] {
			out = append(out, n)
		}
	}
	return out
}

// workerEnv re-anchors the parent's command environment to the worktree:
// a sandbox's writable parent root becomes the worktree, and a container
// must already mount the worktree. It never widens what commands may do.
func workerEnv(env tools.Environment, parentRoot, root string) (tools.Environment, error) {
	switch e := env.(type) {
	case nil:
		return nil, nil
	case tools.Local:
		if e.Sandbox == nil {
			return e, nil
		}
		sb := *e.Sandbox
		sb.Writable = nil
		for _, w := range e.Sandbox.Writable {
			if filepath.Clean(w) == filepath.Clean(parentRoot) {
				w = root
			}
			sb.Writable = append(sb.Writable, w)
		}
		e.Sandbox = &sb
		return e, nil
	case *tools.Container:
		if !within(e.Mount, root) {
			return nil, fmt.Errorf("the worker's worktree %s is outside the container mount %s", root, e.Mount)
		}
		return e, nil
	}
	return nil, fmt.Errorf("workers do not support this command environment (%T)", env)
}

// within reports whether path is dir or inside it.
func within(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// worker runs one worker call: a child agent with write and command tools
// in its own worktree, whose changes tinyagent commits to the worker's
// branch. Its history is kept in t.Subagents and its usage added to t.
func (a *Agent) worker(ctx context.Context, t *trajectory.Trajectory, call model.ToolCall) tools.Result {
	var args struct {
		Task  string `json:"task"`
		Label string `json:"label"`
	}
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
		return toolErr("invalid worker arguments: %s", err)
	}
	if strings.TrimSpace(args.Task) == "" {
		return toolErr("worker needs a non-empty task: a complete brief of what to change and how to verify it")
	}
	if a.Workers == nil || a.Workers.Model == nil {
		return toolErr("worker is not available in this run")
	}
	label := clipRunes(strings.TrimSpace(args.Label), maxDelegateLabel)
	n := 1
	for _, s := range t.Subagents {
		if s.Kind == "worker" {
			n++
		}
	}
	id := fmt.Sprintf("w%d", n)

	cfg := a.Config
	cfg.StepLimit = a.Workers.maxSteps()
	cfg.WallTimeSec = 0 // the parent's deadline is already in ctx
	if a.Config.CostLimit > 0 {
		left := a.Config.CostLimit - t.Cost
		if left <= 0 {
			a.delegateStop = StatusCostLimit
			return toolErr("worker %s not started: the cost limit is reached", id)
		}
		cfg.CostLimit = left
	}
	only := a.workerTools()
	if len(only) == 0 {
		return toolErr("worker %s not started: none of the configured worker tools are available in this run", id)
	}

	top, err := gitwt.Top(a.Tools.Root)
	if err != nil {
		return toolErr("worker %s not started: %s", id, err)
	}
	rel, err := filepath.Rel(top, a.Tools.Root)
	if err != nil || !within(top, a.Tools.Root) {
		return toolErr("worker %s not started: the working directory is not inside the repository at %s", id, top)
	}
	base, err := gitwt.Snapshot(top)
	if err != nil {
		return toolErr("worker %s not started: %s", id, err)
	}
	name := a.Workers.runName() + "-" + id
	wt, branch, err := gitwt.Create(top, name, base)
	if err != nil {
		return toolErr("worker %s not started: %s", id, err)
	}
	root := filepath.Join(wt, rel)
	env, err := workerEnv(a.Tools.Env, a.Tools.Root, root)
	if err != nil {
		gitwt.Remove(top, wt, branch)
		return toolErr("worker %s not started: %s", id, err)
	}
	var fa *tools.FileAccess
	if dr, dw := a.Tools.FileAccess.Rules(); len(dr)+len(dw) > 0 {
		if fa, err = tools.NewFileAccess(root, dr, dw); err != nil {
			gitwt.Remove(top, wt, branch)
			return toolErr("worker %s not started: %s", id, err)
		}
		fa.AddDirs(a.Tools.ExtraDirs)
	}

	out := io.Discard
	if a.Verbose {
		out = a.Out
	}
	child := &Agent{
		Model:     a.Workers.Model,
		ModelName: a.Workers.ModelName,
		Tools: &tools.Executor{
			Root:               root,
			Env:                env,
			BashTimeout:        a.Tools.BashTimeout,
			MaxOutputChars:     a.Tools.MaxOutputChars,
			MaxProcesses:       a.Tools.MaxProcesses,
			ProcessBufferBytes: a.Tools.ProcessBufferBytes,
			CommandTools:       a.Tools.CommandTools,
			CommandLog:         a.Tools.CommandLog,
			ApplyPatch:         a.Tools.ApplyPatch,
			FileAccess:         fa,
			ExtraDirs:          a.Tools.ExtraDirs,
			Redactor:           a.Tools.Redactor,
			Only:               only,
		},
		Config:         cfg,
		Out:            out,
		Err:            a.Err,
		Verbose:        a.Verbose,
		SkipAgentsMD:   a.SkipAgentsMD,
		ScopedAgentsMD: a.ScopedAgentsMD,
		NoCompact:      a.NoCompact,
		Hooks:          a.Hooks,
		// The permission policy also governs a worker's own tool calls.
		Policy:             a.Policy,
		PermissionApprover: a.PermissionApprover,
		subagentID:         id,
		isWorker:           true,
	}
	if a.OnEvent != nil {
		parent := a.OnEvent
		child.OnEvent = func(kind string, fields map[string]any) {
			if fields == nil {
				fields = map[string]any{}
			}
			fields["subagent"] = id
			parent(kind, fields)
		}
	}
	started := map[string]any{"id": id, "kind": "worker", "tool_call_id": call.ID, "model": child.ModelName,
		"parent_step": t.Steps, "branch": branch, "worktree": wt, "base_commit": base}
	if label != "" {
		started["label"] = label
	}
	a.event("subagent.started", started)
	if a.Verbose {
		fmt.Fprintf(a.Err, "worker %s started%s (%s) in %s\n", id, labelSuffix(label), child.ModelName, wt)
	}

	ct := child.Run(ctx, args.Task)

	t.Cost += ct.Cost
	t.InputTokens += ct.InputTokens
	t.OutputTokens += ct.OutputTokens
	t.CacheReadTokens += ct.CacheReadTokens
	t.CacheWriteTokens += ct.CacheWriteTokens
	t.SubagentSteps += ct.Steps
	ok := ct.ExitStatus == StatusSubmitted || ct.ExitStatus == StatusCompleted

	// tinyagent, not the worker, commits, so the worker cannot change what
	// is recorded after it finishes. Partial work is committed too.
	subject := label
	if subject == "" {
		subject = firstLine(args.Task, 60)
	}
	commit, changed, cerr := gitwt.Commit(wt, fmt.Sprintf("tinyagent worker %s: %s [%s]\n\nTask:\n%s", id, subject, ct.ExitStatus, args.Task))
	var stat, short string
	var files []string
	if cerr == nil && changed {
		stat, short = gitwt.DiffStat(top, base, commit)
		files, _ = gitwt.ChangedFiles(top, base, commit)
	} else if cerr == nil {
		gitwt.Remove(top, wt, branch)
	}

	rec := trajectory.Subagent{
		ID: id, ToolCallID: call.ID, Task: args.Task, Label: label, Model: child.ModelName,
		ParentStep: t.Steps, Status: ct.ExitStatus, Steps: ct.Steps,
		InputTokens: ct.InputTokens, OutputTokens: ct.OutputTokens,
		CacheReadTokens: ct.CacheReadTokens, CacheWriteTokens: ct.CacheWriteTokens,
		Cost: ct.Cost, Messages: ct.Messages,
		Kind: "worker", BaseCommit: base, Commit: commit,
	}
	if changed {
		rec.Worktree, rec.Branch = wt, branch
	}
	if ok {
		rec.Report = ct.Submission
	}
	t.Subagents = append(t.Subagents, rec)
	finished := map[string]any{
		"id": id, "kind": "worker", "status": ct.ExitStatus, "steps": ct.Steps,
		"input_tokens": ct.InputTokens, "output_tokens": ct.OutputTokens, "cost_usd": ct.Cost,
		"files_changed": len(files),
	}
	if changed {
		finished["branch"], finished["commit"] = branch, commit
	}
	a.event("subagent.finished", finished)

	var footer string
	switch {
	case cerr != nil:
		footer = fmt.Sprintf("[worker %s: %s · branch %s · committing failed: %s · %d steps · $%.4f]", id, ct.ExitStatus, branch, cerr, ct.Steps, ct.Cost)
	case changed:
		footer = fmt.Sprintf("[worker %s: %s · branch %s · commit %s · %s · %d steps · $%.4f]", id, ct.ExitStatus, branch, shortSHA(commit), short, ct.Steps, ct.Cost)
	default:
		footer = fmt.Sprintf("[worker %s: %s · no changes (worktree and branch removed) · %d steps · $%.4f]", id, ct.ExitStatus, ct.Steps, ct.Cost)
	}
	if a.Verbose {
		fmt.Fprintf(a.Err, "worker %s finished%s: %s\n", id, labelSuffix(label), strings.Trim(footer, "[]"))
	}
	tail := "\n\n" + footer
	if stat != "" {
		tail += "\n" + clipRunes(stat, maxDelegateDetail)
	}
	if changed {
		tail += fmt.Sprintf("\nReview it with `git show %s`; call apply_worker with worker %q to apply it to your working tree.", shortSHA(commit), id)
	}

	if ok {
		report := ct.Submission
		if strings.TrimSpace(report) == "" {
			report = lastAssistantText(ct.Messages)
		}
		if strings.TrimSpace(report) == "" {
			report = "(the worker returned an empty report)"
		}
		res := a.Tools.LimitResult(call, report, false)
		res.Text += tail
		res.Notice = ""
		return res
	}
	switch ct.ExitStatus {
	case StatusInterrupted, StatusTimeLimit, StatusCostLimit:
		a.delegateStop = ct.ExitStatus
	}
	msg := fmt.Sprintf("error: worker %s did not finish: %s", id, ct.ExitStatus)
	if ct.ExitStatus == StatusError && ct.Submission != "" {
		msg += " (" + clipRunes(ct.Submission, maxDelegateDetail) + ")"
	}
	if last := lastAssistantText(ct.Messages); last != "" {
		msg += "\nlast worker message:\n" + clipRunes(last, maxDelegateDetail)
	}
	return tools.Result{Text: msg + tail, IsError: true}
}

// applyWorker applies a worker's commit to the parent's working tree,
// atomically: on any conflict nothing changes.
func (a *Agent) applyWorker(t *trajectory.Trajectory, call model.ToolCall) tools.Result {
	var args struct {
		Worker string `json:"worker"`
	}
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
		return toolErr("invalid apply_worker arguments: %s", err)
	}
	id := strings.TrimSpace(args.Worker)
	var rec *trajectory.Subagent
	for i := range t.Subagents {
		if t.Subagents[i].Kind == "worker" && t.Subagents[i].ID == id {
			rec = &t.Subagents[i]
		}
	}
	switch {
	case rec == nil:
		return toolErr("no worker %q in this run", id)
	case rec.Commit == "":
		return toolErr("worker %s made no changes; there is nothing to apply", id)
	case rec.Applied:
		return toolErr("worker %s is already applied", id)
	}
	top, err := gitwt.Top(a.Tools.Root)
	if err != nil {
		return toolErr("%s", err)
	}
	files, err := gitwt.ChangedFiles(top, rec.BaseCommit, rec.Commit)
	if err != nil {
		return toolErr("reading worker %s's changes: %s", id, err)
	}
	for _, f := range files {
		full := filepath.Join(top, filepath.FromSlash(f))
		if !within(a.Tools.Root, full) {
			return toolErr("worker %s changed %s, outside the working directory; nothing was applied (inspect branch %s)", id, f, rec.Branch)
		}
		name, _ := filepath.Rel(a.Tools.Root, full)
		if err := a.Tools.FileAccess.CheckWrite(full, name); err != nil {
			return toolErr("%s; nothing was applied (inspect branch %s)", err, rec.Branch)
		}
	}
	conflicts, err := gitwt.Apply(top, rec.BaseCommit, rec.Commit)
	if err != nil {
		return toolErr("worker %s: %s; nothing was applied (inspect branch %s)", id, err, rec.Branch)
	}
	if len(conflicts) > 0 {
		return toolErr("worker %s does not apply cleanly; nothing was changed. Conflicting files:\n%s\nThe change stays on branch %s for manual resolution.",
			id, "  "+strings.Join(conflicts, "\n  "), rec.Branch)
	}
	rec.Applied = true
	a.event("worker.applied", map[string]any{"id": id, "branch": rec.Branch, "commit": rec.Commit, "files_changed": len(files)})
	return tools.Result{Text: fmt.Sprintf("applied worker %s (commit %s) to the working tree; files changed:\n  %s", id, shortSHA(rec.Commit), strings.Join(files, "\n  "))}
}

func shortSHA(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// firstLine returns s's first non-empty line, clipped to max runes.
func firstLine(s string, max int) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return clipRunes(l, max)
		}
	}
	return ""
}
