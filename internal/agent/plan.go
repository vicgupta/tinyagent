package agent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// planHeader starts the pinned plan block sent with every request.
const planHeader = "[Current plan]"

// updatePlan handles an update_plan call: it validates the complete
// replacement list, stores it in t, and returns the rendered plan. It never
// touches the filesystem.
func (a *Agent) updatePlan(t *trajectory.Trajectory, call model.ToolCall) (string, bool) {
	var in struct {
		Items []trajectory.PlanItem `json:"items"`
		Note  string                `json:"note"`
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(call.Function.Arguments)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		return "error: invalid update_plan arguments: " + err.Error(), true
	}
	if in.Items == nil {
		return "error: update_plan requires items (use [] to clear the plan)", true
	}
	if err := trajectory.ValidatePlanItems(in.Items, in.Note); err != nil {
		return "error: " + err.Error(), true
	}
	var old []trajectory.PlanItem
	version := 1
	if t.Plan != nil {
		old, version = t.Plan.Items, t.Plan.Version+1
	}
	t.Plan = &trajectory.Plan{Version: version, Items: in.Items, Note: in.Note, UpdatedStep: t.Steps}
	diff := planDiff(old, in.Items)
	t.PlanHistory = append(t.PlanHistory, trajectory.PlanChange{Step: t.Steps, Version: version, Diff: diff})
	a.event("plan.updated", map[string]any{"step": t.Steps, "version": version, "items": in.Items, "note": in.Note, "diff": diff})
	if a.Out != nil {
		fmt.Fprintf(a.Out, "plan updated:\n%s\n", renderPlan(t.Plan))
	}
	return renderPlan(t.Plan), false
}

// renderPlan formats p for the model.
func renderPlan(p *trajectory.Plan) string {
	var sb strings.Builder
	if p == nil {
		sb.WriteString(planHeader + "\nNo plan yet. Use update_plan to record one for multi-step work.")
		return sb.String()
	}
	fmt.Fprintf(&sb, "%s (version %d, updated at step %d)\n", planHeader, p.Version, p.UpdatedStep)
	if len(p.Items) == 0 {
		sb.WriteString("(empty)\n")
	}
	for _, it := range p.Items {
		mark := map[string]string{"pending": "[ ]", "in_progress": "[>]", "done": "[x]", "blocked": "[!]", "failed": "[-]"}[it.Status]
		fmt.Fprintf(&sb, "%s %s: %s (%s)\n", mark, it.ID, it.Text, it.Status)
	}
	if p.Note != "" {
		fmt.Fprintf(&sb, "Note: %s\n", p.Note)
	}
	return strings.TrimRight(sb.String(), "\n")
}

// planDiff summarizes the change from old to items for the audit history.
func planDiff(old, items []trajectory.PlanItem) string {
	prev := map[string]trajectory.PlanItem{}
	for _, it := range old {
		prev[it.ID] = it
	}
	var lines []string
	seen := map[string]bool{}
	for _, it := range items {
		seen[it.ID] = true
		o, ok := prev[it.ID]
		switch {
		case !ok:
			lines = append(lines, fmt.Sprintf("+ %s [%s] %s", it.ID, it.Status, it.Text))
		case o.Status != it.Status || o.Text != it.Text:
			lines = append(lines, fmt.Sprintf("~ %s [%s→%s] %s", it.ID, o.Status, it.Status, it.Text))
		}
	}
	for _, it := range old {
		if !seen[it.ID] {
			lines = append(lines, fmt.Sprintf("- %s", it.ID))
		}
	}
	if len(lines) == 0 {
		return "(no item changes)"
	}
	return strings.Join(lines, "\n")
}

// withPlan returns the request messages: v's messages with the plan block
// pinned to the task message, or to the summary that replaced earlier turns.
// It copies, so the view and trajectory never contain the block and
// compaction can never elide or summarize it.
func (a *Agent) withPlan(t *trajectory.Trajectory, v *view) []model.Message {
	if !a.Tools.Plan || len(v.msgs) < 2 {
		return v.msgs
	}
	out := append([]model.Message(nil), v.msgs...)
	at := 1
	if len(v.idx) > 2 && v.idx[2] == -1 {
		at = 2
	}
	out[at].Content += "\n\n" + a.planBlock(t)
	return out
}

// planChars is the pinned block's size, counted toward the token estimate.
func (a *Agent) planChars(t *trajectory.Trajectory) int {
	if !a.Tools.Plan {
		return 0
	}
	return len(a.planBlock(t)) + 2
}

// planRequiredHint replaces "No plan yet" when plan.require_first is on.
const planRequiredHint = "\nNo plan yet. Before changing anything, call update_plan with the ordered steps for this task; then work through them in order, keeping each step's status current."

// planBlock is the pinned plan block for this run.
func (a *Agent) planBlock(t *trajectory.Trajectory) string {
	if t.Plan == nil && a.planFirst() {
		return planHeader + planRequiredHint
	}
	return renderPlan(t.Plan)
}

// planFirst reports whether a plan must exist before the first
// workspace-changing tool call (plan.require_first, --plan-first).
func (a *Agent) planFirst() bool {
	return a.Tools != nil && a.Tools.Plan && a.Config.Plan.RequireFirst
}

// planGate returns the error result for a call that must wait for a plan,
// or "" when the call may run: read-only tools, ask_user and submitting
// never wait, so trivial or read-only tasks aren't burdened.
func (a *Agent) planGate(t *trajectory.Trajectory, call model.ToolCall) string {
	if !a.planFirst() || (t.Plan != nil && len(t.Plan.Items) > 0) || readOnlyTools[call.Function.Name] {
		return ""
	}
	a.event("plan.required", map[string]any{"step": t.Steps, "name": call.Function.Name})
	return fmt.Sprintf("error: plan required: call update_plan with the ordered steps for this task before running %s (plan.require_first is on). Nothing was run.", call.Function.Name)
}
