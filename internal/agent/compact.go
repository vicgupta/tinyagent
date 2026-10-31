package agent

import (
	"context"
	"errors"
	"fmt"
	"github.com/vicgupta/tinyagent/internal/routing"
	"strings"

	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// Compaction thresholds, as fractions of the context window.
const (
	compactAt   = 0.80 // compact before a request estimated above this
	elideTarget = 0.50 // eliding alone is enough below this
	keepRecent  = 0.25 // recent turns kept verbatim, at least minExchanges
)

// minExchanges is how many of the latest assistant turns, with their tool
// results, are always kept verbatim.
const minExchanges = 2

const elidedPrefix = "[output elided: "

// errStillTooLong ends a run when compaction couldn't make the context fit.
var errStillTooLong = errors.New("context still exceeds the window after compaction")

const summarizerSystem = "You summarize the earlier part of a coding agent's run so the agent can continue " +
	"with only your summary in place of that history. Be specific: keep file paths, function names, " +
	"commands, error messages, and decisions. Do not call tools."

const summarizerInstructions = "Summarize the transcript above under exactly these headings:\n\n" +
	"## Task\n## Constraints\n## Progress so far\n## Files changed\n## Key decisions\n## Open problems\n## Next steps"

const summaryHeader = "[Summary of earlier steps. The history before this point was compacted to fit " +
	"the context window; this summary replaces it.]\n\n"

// view is the history the model is sent. It starts as a copy of the
// trajectory's messages and diverges once compacted; idx maps each view
// message to its index in Trajectory.Messages, or -1 for a summary.
type view struct {
	msgs []model.Message
	idx  []int
	// Token estimate: the last request's reported size, plus the characters
	// appended since then at ~4 per token.
	lastTokens   int
	pendingChars int
}

func newView(msgs []model.Message) *view {
	v := &view{}
	for i, m := range msgs {
		v.msgs = append(v.msgs, m)
		v.idx = append(v.idx, i)
	}
	return v
}

// add appends m, which was just appended to t.Messages.
func (v *view) add(t *trajectory.Trajectory, m model.Message) {
	v.msgs = append(v.msgs, m)
	v.idx = append(v.idx, len(t.Messages)-1)
	v.pendingChars += msgChars(m)
}

// queried records a model response's reported usage.
func (v *view) queried(c model.Cost) {
	v.lastTokens, v.pendingChars = 0, 0
	if c.InputTokens > 0 {
		v.lastTokens = c.InputTokens + c.OutputTokens
	}
}

// estimate returns the approximate size in tokens of the next request.
func (v *view) estimate() int {
	if v.lastTokens > 0 {
		return v.lastTokens + v.pendingChars/4
	}
	n := 0
	for _, m := range v.msgs {
		n += msgChars(m)
	}
	return n / 4
}

func msgChars(m model.Message) int {
	// Each image counts as a fixed estimate, never its base64 length.
	n := len(m.Content) + model.ReasoningChars(m) + len(m.Images)*model.ImageTokenEstimate*4
	for _, c := range m.ToolCalls {
		n += len(c.Function.Name) + len(c.Function.Arguments)
	}
	return n
}

// protectedFrom returns the index where the verbatim tail of the view begins.
// It always falls on an assistant message, so a tool call is never separated
// from its results, and is never below 2 (system prompt and task).
func (v *view) protectedFrom(keepTokens int) int {
	b, tokens, exchanges := len(v.msgs), 0, 0
	end := len(v.msgs)
	for i := len(v.msgs) - 1; i >= 2; i-- {
		if v.msgs[i].Role != "assistant" {
			continue
		}
		ex := 0
		for _, m := range v.msgs[i:end] {
			ex += msgChars(m) / 4
		}
		if exchanges >= minExchanges && tokens+ex > keepTokens {
			break
		}
		b, end, tokens, exchanges = i, i, tokens+ex, exchanges+1
	}
	return b
}

func (a *Agent) contextWindow() int {
	if a.Config.ContextWindow > 0 {
		return a.Config.ContextWindow
	}
	return model.ContextWindow(a.activeModel())
}

// fallback returns the agent's fallback chain model, or nil without one.
func (a *Agent) fallback() *model.Fallback {
	if len(a.FallbackModels) == 0 {
		return nil
	}
	fb, _ := a.Model.(*model.Fallback)
	return fb
}

// activeModel is the spec of the model serving the next request.
func (a *Agent) activeModel() string {
	if fb := a.fallback(); fb != nil {
		return fb.Active()
	}
	return a.ModelName
}

// needsCompaction reports whether the next request is estimated to be over
// compactAt of a known context window.
func (a *Agent) needsCompaction(t *trajectory.Trajectory, v *view) bool {
	w := a.contextWindow()
	return !a.NoCompact && w > 0 && float64(v.estimate()+(a.planChars(t)+a.retainedChars(t, v))/4) > compactAt*float64(w)
}

// viewFromTrajectory rebuilds the model's view of t: the last compaction's
// view (with every recorded elision and the summary it references) followed
// by the messages recorded after it. Without compactions it is Messages.
func viewFromTrajectory(t *trajectory.Trajectory) *view {
	if len(t.Compactions) == 0 {
		return newView(t.Messages)
	}
	elided := map[int]bool{}
	summary := ""
	for _, c := range t.Compactions {
		for _, ix := range c.Elided {
			elided[ix] = true
		}
		if c.Summary != "" {
			summary = c.Summary
		}
	}
	last := t.Compactions[len(t.Compactions)-1]
	v := &view{}
	maxIdx := 1
	appendMsg := func(ix int) {
		m := t.Messages[ix]
		if elided[ix] {
			m = elide(m)
		}
		v.msgs = append(v.msgs, m)
		v.idx = append(v.idx, ix)
	}
	for _, ix := range last.View {
		if ix == -1 {
			v.msgs = append(v.msgs, model.Message{Role: "user", Content: summaryHeader + summary})
			v.idx = append(v.idx, -1)
			continue
		}
		appendMsg(ix)
		maxIdx = max(maxIdx, ix)
	}
	for ix := maxIdx + 1; ix < len(t.Messages); ix++ {
		v.msgs = append(v.msgs, t.Messages[ix])
		v.idx = append(v.idx, ix)
	}
	return v
}

// compact shrinks the view: first by eliding old tool outputs, then, if that
// isn't enough, by replacing old turns with a model-written summary. The
// summary call's cost goes into t's totals but isn't a step.
func (a *Agent) compact(ctx context.Context, t *trajectory.Trajectory, v *view, reason string) error {
	w := a.contextWindow()
	before := v.estimate()
	target := before / 2 // window unknown: aim to halve it
	if w > 0 {
		target = int(elideTarget * float64(w))
	}
	b := v.protectedFrom(int(keepRecent * float64(w)))
	rec := trajectory.Compaction{Step: t.Steps, Reason: reason, Stage: "elide", TokensBefore: before}

	// Stage 1: elide old tool outputs.
	freed := 0
	loads := a.skillLoads(t)
	for i := 2; i < b; i++ {
		m := &v.msgs[i]
		if m.Role != "tool" || strings.HasPrefix(m.Content, elidedPrefix) || isSkillLoad(loads, v.idx[i]) {
			continue
		}
		e := elide(*m)
		if len(m.Images) == 0 && len(e.Content) >= len(m.Content) {
			continue
		}
		freed += msgChars(*m) - msgChars(e)
		*m = e
		rec.Elided = append(rec.Elided, v.idx[i])
	}
	after := max(before-freed/4, 0)

	// Stage 2: summarize everything before the protected tail.
	if after >= target && b > 2 {
		rec.Stage = "summarize"
		costBefore := t.Cost
		summary, err := a.summarize(ctx, t, v.msgs[1].Content, v.msgs[2:b])
		if err != nil {
			return err
		}
		removed := 0
		for i := 2; i < b; i++ {
			removed += msgChars(v.msgs[i])
			if v.idx[i] >= 0 {
				rec.Summarized = append(rec.Summarized, v.idx[i])
			}
		}
		sm := model.Message{Role: "user", Content: summaryHeader + summary}
		v.msgs = append([]model.Message{v.msgs[0], v.msgs[1], sm}, v.msgs[b:]...)
		v.idx = append([]int{v.idx[0], v.idx[1], -1}, v.idx[b:]...)
		rec.Summary = summary
		rec.USD = t.Cost - costBefore
		after = max(after-removed/4+msgChars(sm)/4, 0)
	}
	if after == 0 {
		v.lastTokens, v.pendingChars = 0, 0
		after = v.estimate()
	} else {
		v.lastTokens, v.pendingChars = after, 0
	}
	rec.TokensAfter = after
	rec.View = append([]int(nil), v.idx...)
	if a.Tools.Plan && t.Plan != nil {
		rec.PlanVersion = t.Plan.Version
	}

	changed := len(rec.Elided) > 0 || rec.Stage == "summarize"
	if changed {
		t.Compactions = append(t.Compactions, rec)
		detail := fmt.Sprintf("elided %d tool outputs", len(rec.Elided))
		if rec.Stage == "summarize" {
			detail = fmt.Sprintf("summarized %d messages", len(rec.Summarized))
			if len(rec.Elided) > 0 {
				detail = fmt.Sprintf("elided %d tool outputs, summarized %d messages", len(rec.Elided), len(rec.Summarized))
			}
		}
		fmt.Fprintf(a.Err, "compacted context: ~%s → ~%s tokens (%s)\n", formatInt(before), formatInt(after), detail)
		a.event("compaction", map[string]any{"step": rec.Step, "reason": rec.Reason, "stage": rec.Stage, "tokens_before": rec.TokensBefore, "tokens_after": rec.TokensAfter, "elided_messages": len(rec.Elided), "summarized_messages": len(rec.Summarized)})
	}
	if !changed || (w > 0 && float64(after) > compactAt*float64(w)) {
		return errStillTooLong
	}
	return nil
}

// summarize asks the model, with no tools, for a summary of msgs. Its usage
// is added to t's totals.
func (a *Agent) summarize(ctx context.Context, t *trajectory.Trajectory, task string, msgs []model.Message) (string, error) {
	var sb strings.Builder
	fmt.Fprintf(&sb, "The agent's task:\n\n%s\n\nTranscript of its earlier steps:\n\n", task)
	for _, m := range msgs {
		switch m.Role {
		case "tool":
			fmt.Fprintf(&sb, "[tool result %s]\n%s\n\n", m.ToolCallID, m.Content)
		default:
			if m.Content != "" {
				fmt.Fprintf(&sb, "[%s]\n%s\n\n", m.Role, m.Content)
			}
			for _, c := range m.ToolCalls {
				fmt.Fprintf(&sb, "[tool call %s] %s %s\n\n", c.ID, c.Function.Name, c.Function.Arguments)
			}
		}
	}
	if a.Tools.Plan {
		fmt.Fprintf(&sb, "The agent's current plan (kept separately; it is shown to the agent every step):\n\n%s\n\n", renderPlan(t.Plan))
	}
	sb.WriteString(summarizerInstructions)
	req := []model.Message{
		{Role: "system", Content: summarizerSystem},
		{Role: "user", Content: sb.String()},
	}
	resp, cost, err := a.helperQuery(ctx, routing.TaskCompaction, req)
	if err != nil {
		return "", fmt.Errorf("summarizing context: %w", err)
	}
	addUsage(t, routing.RoleAuxiliary, routing.TaskCompaction, a.helperName(routing.TaskCompaction), cost)
	t.Cost += cost.USD
	t.InputTokens += cost.InputTokens
	t.OutputTokens += cost.OutputTokens
	t.CacheReadTokens += cost.CacheReadTokens
	t.CacheWriteTokens += cost.CacheWriteTokens
	s := strings.TrimSpace(resp.Content)
	if s == "" {
		return "", errors.New("summarizing context: the model returned an empty summary")
	}
	return s, nil
}

// formatInt adds thousands separators: 48213 -> 48,213.
func formatInt(n int) string {
	if n < 0 {
		return "-" + formatInt(-n)
	}
	s := fmt.Sprint(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// elide replaces a tool result's text with a size note and each of its
// images with an "[image elided: ...]" note.
func elide(m model.Message) model.Message {
	text := fmt.Sprintf("%s%s chars]", elidedPrefix, formatInt(len(m.Content)))
	for _, im := range m.Images {
		label := im.Label
		if label == "" {
			label = im.MIMEType
		}
		text += fmt.Sprintf("\n[image elided: %s, %s bytes]", label, formatInt(im.Size()))
	}
	m.Content, m.Images = text, nil
	return m
}
