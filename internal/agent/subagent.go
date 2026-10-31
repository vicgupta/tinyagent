package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/vicgupta/tinyagent/internal/routing"
	"io"
	"strings"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/tools"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// Subagents configures the children the opt-in delegate tool runs.
type Subagents struct {
	// Model and ModelName are the children's model; the caller builds it
	// (by default the parent's model spec, without streaming).
	Model     model.Model
	ModelName string
	// MaxSteps caps each child's model calls; 0 means
	// config.DefaultSubagentMaxSteps.
	MaxSteps int
	// Streamed reports whether Model writes text to the terminal itself.
	Streamed bool
	// Routes pick another model per task (models.delegated.routes).
	Routes []ChildRoute
}

func (s *Subagents) maxSteps() int {
	if s.MaxSteps > 0 {
		return s.MaxSteps
	}
	return config.DefaultSubagentMaxSteps
}

// subagentNotice ends a delegate child's system prompt.
const subagentNotice = "You are a read-only subagent working for another agent, which sees only your final message. " +
	"Investigate the task with read_file, list_files, and grep, then reply with a concise, self-contained report: " +
	"the answer first, then the evidence, citing file:line wherever it helps. Do not ask questions; " +
	"if something cannot be determined, say so in the report."

// Bounds for what a delegate call reports beyond the child's own report.
const (
	maxDelegateLabel  = 80
	maxDelegateDetail = 2000
)

// takeDelegateStop returns and clears the status a delegate child left for
// the parent.
func (a *Agent) takeDelegateStop() string {
	s := a.delegateStop
	a.delegateStop = ""
	return s
}

// delegate runs one delegate call: a read-only child agent with a fresh
// context. Only the child's report (or failure) comes back as the result;
// its history is kept in t.Subagents and its usage is added to t.
func (a *Agent) delegate(ctx context.Context, t *trajectory.Trajectory, call model.ToolCall) tools.Result {
	var args struct {
		Task     string `json:"task"`
		Label    string `json:"label"`
		TaskType string `json:"task_type"`
	}
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
		return tools.Result{Text: "error: invalid delegate arguments: " + err.Error(), IsError: true}
	}
	if strings.TrimSpace(args.Task) == "" {
		return tools.Result{Text: "error: delegate needs a non-empty task: a complete brief of what to find and report", IsError: true}
	}
	if a.Subagents == nil || a.Subagents.Model == nil {
		return tools.Result{Text: "error: delegate is not available in this run", IsError: true}
	}
	label := clipRunes(strings.TrimSpace(args.Label), maxDelegateLabel)
	id := fmt.Sprintf("d%d", len(t.Subagents)+1)

	cfg := a.Config
	cfg.StepLimit = a.Subagents.maxSteps()
	cfg.WallTimeSec = 0 // the parent's deadline is already in ctx
	if a.Config.CostLimit > 0 {
		left := a.Config.CostLimit - t.Cost
		if left <= 0 {
			a.delegateStop = StatusCostLimit
			return tools.Result{Text: fmt.Sprintf("error: delegate %s not started: the cost limit is reached", id), IsError: true}
		}
		cfg.CostLimit = left
	}
	if left, ok := a.tokensLeft(t); ok {
		if left <= 0 {
			a.delegateStop = StatusTokenLimit
			return tools.Result{Text: fmt.Sprintf("error: delegate %s not started: the token limit is reached", id), IsError: true}
		}
		cfg.TokenLimit = left
	}
	out := io.Discard
	if a.Verbose {
		out = a.Out
	}
	childModel, childName, reason := pickChild(a.Subagents.Routes, a.Subagents.Model, a.Subagents.ModelName, args.Task, args.TaskType)
	if reason != "" {
		a.routeEvent(routing.RoleDelegated, routing.TaskDelegate, childName, reason, "")
	}
	child := &Agent{
		Model:     childModel,
		ModelName: childName,
		Tools: &tools.Executor{
			Root:           a.Tools.Root,
			ReadOnly:       true,
			MaxOutputChars: a.Tools.MaxOutputChars,
			OutputFiles:    a.Tools.OutputFiles,
			FileAccess:     a.Tools.FileAccess,
			ExtraDirs:      a.Tools.ExtraDirs,
		},
		Config:         cfg,
		Out:            out,
		Err:            a.Err,
		Verbose:        a.Verbose,
		Streamed:       a.Subagents.Streamed && a.Verbose,
		SkipAgentsMD:   a.SkipAgentsMD,
		ScopedAgentsMD: a.ScopedAgentsMD,
		NoCompact:      a.NoCompact,
		Hooks:          a.Hooks,
		subagentID:     id,
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
	started := map[string]any{"id": id, "tool_call_id": call.ID, "model": child.ModelName, "parent_step": t.Steps}
	if label != "" {
		started["label"] = label
	}
	a.event("subagent.started", started)
	if a.Verbose {
		fmt.Fprintf(a.Err, "delegate %s started%s (%s)\n", id, labelSuffix(label), child.ModelName)
	}

	ct := child.Run(ctx, args.Task)

	t.Cost += ct.Cost
	t.InputTokens += ct.InputTokens
	t.OutputTokens += ct.OutputTokens
	t.CacheReadTokens += ct.CacheReadTokens
	t.CacheWriteTokens += ct.CacheWriteTokens
	t.SubagentSteps += ct.Steps
	ok := ct.ExitStatus == StatusSubmitted || ct.ExitStatus == StatusCompleted
	rec := trajectory.Subagent{
		ID: id, ToolCallID: call.ID, Task: args.Task, Label: label, Model: child.ModelName,
		ParentStep: t.Steps, Status: ct.ExitStatus, Steps: ct.Steps,
		InputTokens: ct.InputTokens, OutputTokens: ct.OutputTokens,
		CacheReadTokens: ct.CacheReadTokens, CacheWriteTokens: ct.CacheWriteTokens,
		Cost: ct.Cost, Messages: ct.Messages,
	}
	if ok {
		rec.Report = ct.Submission
	}
	t.Subagents = append(t.Subagents, rec)
	a.event("subagent.finished", map[string]any{
		"id": id, "status": ct.ExitStatus, "steps": ct.Steps,
		"input_tokens": ct.InputTokens, "output_tokens": ct.OutputTokens, "cost_usd": ct.Cost,
	})
	footer := fmt.Sprintf("[delegate %s: %s, %d steps, %d in / %d out tokens, $%.4f]", id, ct.ExitStatus, ct.Steps, ct.InputTokens, ct.OutputTokens, ct.Cost)
	if a.Verbose {
		fmt.Fprintf(a.Err, "delegate %s finished%s: %s\n", id, labelSuffix(label), strings.Trim(footer, "[]"))
	}

	if ok {
		report := ct.Submission
		if strings.TrimSpace(report) == "" {
			report = lastAssistantText(ct.Messages)
		}
		if strings.TrimSpace(report) == "" {
			report = "(the subagent returned an empty report)"
		}
		res := a.Tools.LimitResult(call, report, false)
		res.Text += "\n\n" + footer
		res.Notice = ""
		return res
	}
	switch ct.ExitStatus {
	case StatusInterrupted, StatusTimeLimit, StatusCostLimit, StatusTokenLimit:
		a.delegateStop = ct.ExitStatus
	}
	msg := fmt.Sprintf("error: delegate %s did not finish: %s", id, ct.ExitStatus)
	if ct.ExitStatus == StatusError && ct.Submission != "" {
		msg += " (" + clipRunes(ct.Submission, maxDelegateDetail) + ")"
	}
	if last := lastAssistantText(ct.Messages); last != "" {
		msg += "\nlast subagent message:\n" + clipRunes(last, maxDelegateDetail)
	}
	return tools.Result{Text: msg + "\n\n" + footer, IsError: true}
}

func labelSuffix(label string) string {
	if label == "" {
		return ""
	}
	return " [" + label + "]"
}

// lastAssistantText returns the newest non-empty assistant text in msgs.
func lastAssistantText(msgs []model.Message) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "assistant" && strings.TrimSpace(msgs[i].Content) != "" {
			return strings.TrimSpace(msgs[i].Content)
		}
	}
	return ""
}

// clipRunes shortens s to at most max runes, marking the cut.
func clipRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "..."
}
