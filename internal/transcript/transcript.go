// Package transcript renders a saved trajectory as a Markdown transcript
// for people: the task, each step's assistant text, tool calls and results,
// the run's plan, compactions, model switches and resume boundaries, and
// the outcome.
//
// Every string that came from the task, the model, tools, MCP servers or
// hooks is treated as untrusted: it is redacted, and it is written only
// inside fenced code blocks or inline code spans longer than any backtick
// run it contains, so it cannot render as a link, image or HTML. Image data
// is never written. The output depends only on the trajectory, the
// redactor and the limit, so it is byte-identical across runs.
package transcript

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/vicgupta/tinyagent/internal/eventlog"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// DefaultLimit is the number of characters kept of each tool result, tool
// call's arguments and tinyagent-generated user message.
const DefaultLimit = 2000

// Options configures Render.
type Options struct {
	// Redactor masks secrets; nil still masks token-shaped strings.
	Redactor *eventlog.Redactor
	// Limit is the number of characters kept of each long block; 0 means
	// DefaultLimit.
	Limit int
}

type renderer struct {
	b     bytes.Buffer
	t     *trajectory.Trajectory
	red   *eventlog.Redactor
	limit int
}

// Render writes t as Markdown to w.
func Render(w io.Writer, t *trajectory.Trajectory, opts Options) error {
	r := &renderer{t: t, red: opts.Redactor, limit: opts.Limit}
	if r.limit <= 0 {
		r.limit = DefaultLimit
	}
	r.header()
	r.body()
	r.footer()
	_, err := w.Write(r.b.Bytes())
	return err
}

func (r *renderer) printf(format string, args ...any) { fmt.Fprintf(&r.b, format, args...) }

// clean redacts s and normalizes its line endings.
func (r *renderer) clean(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return r.red.Redact(s)
}

// truncate keeps the first r.limit characters of s, with a notice saying
// how many were omitted.
func (r *renderer) truncate(s string) string {
	n := utf8.RuneCountInString(s)
	if n <= r.limit {
		return s
	}
	i, kept := 0, 0
	for kept < r.limit {
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
		kept++
	}
	return s[:i] + fmt.Sprintf("\n[... %s characters omitted ...]", formatInt(n-r.limit))
}

// longestRun returns the length of the longest run of backticks in s.
func longestRun(s string) int {
	longest, cur := 0, 0
	for i := 0; i < len(s); i++ {
		if s[i] == '`' {
			cur++
			longest = max(longest, cur)
		} else {
			cur = 0
		}
	}
	return longest
}

// fence writes s, already cleaned, as a fenced code block whose fence is
// longer than any backtick run in s, so nothing in it renders as Markdown.
func (r *renderer) fence(info, s string) {
	f := strings.Repeat("`", max(3, longestRun(s)+1))
	s = strings.TrimSuffix(s, "\n")
	r.printf("%s%s\n%s\n%s\n\n", f, info, s, f)
}

// text fences an untrusted string after redacting it.
func (r *renderer) text(s string) { r.fence("text", r.clean(s)) }

// textLimited fences an untrusted string after redacting and truncating it.
func (r *renderer) textLimited(s string) { r.fence("text", r.truncate(r.clean(s))) }

// code returns s as an inert inline code span on one line.
func (r *renderer) code(s string) string {
	s = strings.Join(strings.Fields(r.clean(s)), " ")
	if s == "" {
		return "(none)"
	}
	f := strings.Repeat("`", longestRun(s)+1)
	if strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") {
		s = " " + s + " " // one space each side is stripped when rendered
	}
	return f + s + f
}

func (r *renderer) header() {
	t := r.t
	r.printf("# tinyagent transcript\n\n")
	status := t.ExitStatus
	if status == "" {
		status = "unfinished"
	}
	r.printf("- **Exit status:** %s\n", r.code(status))
	if t.Model != "" {
		line := r.code(t.Model)
		if t.ActiveModel != "" && t.ActiveModel != t.Model {
			line += " (active: " + r.code(t.ActiveModel) + ")"
		}
		r.printf("- **Model:** %s\n", line)
	}
	if t.ReasoningEffort != "" {
		r.printf("- **Reasoning effort:** %s\n", r.code(t.ReasoningEffort))
	}
	if t.Profile != "" {
		r.printf("- **Profile:** %s\n", r.code(t.Profile))
	}
	steps := fmt.Sprint(t.Steps)
	if t.SubagentSteps > 0 {
		steps += fmt.Sprintf(" (plus %d subagent steps)", t.SubagentSteps)
	}
	r.printf("- **Steps:** %s\n", steps)
	tokens := fmt.Sprintf("%s input / %s output", formatInt(t.InputTokens), formatInt(t.OutputTokens))
	if t.CacheReadTokens > 0 || t.CacheWriteTokens > 0 {
		tokens += fmt.Sprintf(" (input includes %s cache read, %s cache write)", formatInt(t.CacheReadTokens), formatInt(t.CacheWriteTokens))
	}
	r.printf("- **Tokens:** %s\n", tokens)
	if t.Cost > 0 {
		r.printf("- **Cost:** %s\n", formatCost(t.Cost))
	}
	if t.ReadOnly {
		r.printf("- **Read-only:** yes\n")
	}
	if sb := t.Sandbox; sb != nil {
		net := "off"
		switch {
		case len(sb.AllowedDomains) > 0:
			net = "allowlist " + r.code(strings.Join(sb.AllowedDomains, " "))
		case sb.Network:
			net = "on"
		}
		r.printf("- **Sandbox:** %s, network %s\n", r.code(sb.Backend), net)
	}
	if e := t.Environment; e != nil {
		line := r.code(e.Name)
		if e.Runtime != "" || e.Image != "" {
			line += " (" + r.code(strings.TrimSpace(e.Runtime+" "+e.Image)) + ")"
		}
		r.printf("- **Environment:** %s\n", line)
	}
	if wt := t.Worktree; wt != nil {
		r.printf("- **Worktree:** %s on branch %s\n", r.code(wt.Path), r.code(wt.Branch))
	}
	if rv := t.Review; rv != nil {
		line := r.code(rv.Target)
		if rv.Ref != "" {
			line += " " + r.code(rv.Ref)
		}
		r.printf("- **Review:** %s, %d files changed\n", line, rv.FilesChanged)
	}
	if t.Root != "" {
		r.printf("- **Root:** %s\n", r.code(t.Root))
	}
	if t.FormatErrors > 0 {
		r.printf("- **Format errors:** %d\n", t.FormatErrors)
	}
	if t.Truncations > 0 {
		r.printf("- **Truncated responses:** %d\n", t.Truncations)
	}
	r.printf("\n")
	if len(t.Messages) > 0 && t.Messages[0].Role == "system" {
		r.printf("_System prompt omitted (%s characters)._\n\n", formatInt(utf8.RuneCountInString(t.Messages[0].Content)))
	}
}

// boundaries returns the resume and follow-up boundaries by message index.
func (r *renderer) boundaries() map[int]*trajectory.ResumeInfo {
	out := map[int]*trajectory.ResumeInfo{}
	for b := r.t.Resume; b != nil; b = b.Previous {
		out[b.Message] = b
	}
	return out
}

func (r *renderer) body() {
	t := r.t
	msgs := t.Messages
	start := 0
	if len(msgs) > 0 && msgs[0].Role == "system" {
		start = 1
	}
	r.printf("## Task\n\n")
	if start < len(msgs) && msgs[start].Role == "user" {
		r.userContent(msgs[start])
		start++
	} else {
		r.textLimited(t.Task)
	}

	bounds := r.boundaries()
	formatErrors := map[int]bool{}
	for _, i := range t.FormatErrorMessages {
		formatErrors[i] = true
	}
	subagents := map[string]trajectory.Subagent{}
	for _, s := range t.Subagents {
		subagents[s.ToolCallID] = s
	}
	results := map[string]bool{}
	for _, m := range msgs {
		if m.Role == "tool" {
			results[m.ToolCallID] = true
		}
	}
	names := map[string]string{}
	step := 0
	var lastCalls []model.ToolCall
	pendingNote := func() {
		for _, c := range lastCalls {
			if !results[c.ID] {
				r.printf("_No result recorded for %s (call %s)._\n\n", r.code(c.Function.Name), r.code(c.ID))
			}
		}
		lastCalls = nil
	}
	for i := start; i < len(msgs); i++ {
		m := msgs[i]
		switch m.Role {
		case "assistant":
			pendingNote()
			step++
			r.between(step)
			r.printf("### Step %d\n\n", step)
			if served, _ := m.Extra[model.ExtraModel].(string); served != "" && served != t.Model {
				r.printf("_Model: %s_\n\n", r.code(served))
			}
			if strings.TrimSpace(m.Content) != "" {
				r.text(m.Content)
			}
			for _, c := range m.ToolCalls {
				names[c.ID] = c.Function.Name
				r.printf("**Tool call:** %s\n\n", r.code(c.Function.Name))
				r.fence("json", r.truncate(r.clean(prettyJSON(c.Function.Arguments))))
			}
			lastCalls = m.ToolCalls
		case "tool":
			summary := "Result"
			if name := names[m.ToolCallID]; name != "" {
				summary = "Result of " + html.EscapeString(r.clean(name))
			}
			if formatErrors[i] {
				summary += " (format error correction)"
			}
			r.printf("<details><summary>%s</summary>\n\n", summary)
			r.fence("text", r.truncate(r.clean(m.Content))+r.imageLines(m.Images))
			r.printf("</details>\n\n")
			if s, ok := subagents[m.ToolCallID]; ok {
				r.subagent(s)
			}
		case "user":
			pendingNote()
			if b := bounds[i]; b != nil {
				kind := "Resumed"
				if b.FollowUp {
					kind = "Follow-up"
				}
				r.printf("---\n\n**%s** after %s at step %d (from %s).\n\n", kind, r.code(b.ExitStatus), b.Steps, r.code(b.Source))
			} else {
				r.printf("**User message**\n\n")
			}
			r.userContent(m)
		default:
			r.printf("**%s message**\n\n", r.code(m.Role))
			r.textLimited(m.Content)
		}
	}
	pendingNote()
	// Events after the last step: a compaction or plan change after it, or
	// a model switch for a request that never came back.
	last := step
	for _, c := range t.Compactions {
		last = max(last, c.Step)
	}
	for _, s := range t.ModelSwitches {
		last = max(last, s.Step-1)
	}
	for _, p := range t.PlanHistory {
		last = max(last, p.Step)
	}
	for n := step + 1; n <= last+1; n++ {
		r.between(n)
	}
}

// userContent renders a user message: its text and image placeholders.
func (r *renderer) userContent(m model.Message) {
	content := strings.TrimSuffix(m.Content, model.AttachmentLabels(m.Images))
	r.fence("text", r.truncate(r.clean(content))+r.imageLines(m.Images))
}

// imageLines returns a placeholder line for each image, never its data.
func (r *renderer) imageLines(images []model.Image) string {
	var b strings.Builder
	for _, im := range images {
		parts := []string{im.MIMEType}
		if im.Label != "" {
			parts = append(parts, im.Label)
		}
		parts = append(parts, formatInt(im.Size())+" bytes")
		b.WriteString("\n[image: " + r.clean(strings.Join(parts, ", ")) + "]")
	}
	return b.String()
}

// between renders what happened after step n-1 and before step n: plan
// updates and compactions at step n-1, then model switches for step n.
func (r *renderer) between(n int) {
	t := r.t
	for _, p := range t.PlanHistory {
		if p.Step != n-1 {
			continue
		}
		r.printf("> **Plan updated** to version %d at step %d.\n\n", p.Version, p.Step)
		if p.Diff != "" {
			r.textLimited(p.Diff)
		}
		if t.Plan != nil && p.Version == t.Plan.Version {
			r.plan()
		}
	}
	for _, c := range t.Compactions {
		if c.Step != n-1 {
			continue
		}
		r.printf("> **Compaction** after step %d: %s, %s, %s → %s tokens", c.Step, r.code(c.Reason), r.code(c.Stage), formatInt(c.TokensBefore), formatInt(c.TokensAfter))
		if len(c.Elided) > 0 {
			r.printf(", tool outputs elided: %d", len(c.Elided))
		}
		if len(c.Summarized) > 0 {
			r.printf(", messages summarized: %d", len(c.Summarized))
		}
		r.printf(". The messages below and above are the full history, not the model's view.\n\n")
		if c.Summary != "" {
			r.printf("<details><summary>Compaction summary</summary>\n\n")
			r.textLimited(c.Summary)
			r.printf("</details>\n\n")
		}
	}
	for _, s := range t.ModelSwitches {
		if s.Step == n {
			r.printf("> **Model switch** before step %d: %s → %s (%s).\n\n", s.Step, r.code(s.From), r.code(s.To), r.code(s.Reason))
		}
	}
}

// plan renders the final plan as a checklist.
func (r *renderer) plan() {
	p := r.t.Plan
	r.printf("Final plan (version %d, %d updates):\n\n", p.Version, len(r.t.PlanHistory))
	var b strings.Builder
	for _, it := range p.Items {
		mark := " "
		switch it.Status {
		case "done":
			mark = "x"
		case "in_progress":
			mark = "~"
		case "blocked":
			mark = "!"
		}
		fmt.Fprintf(&b, "[%s] %s. %s (%s)\n", mark, it.ID, it.Text, it.Status)
	}
	if p.Note != "" {
		b.WriteString("Note: " + p.Note + "\n")
	}
	r.text(b.String())
}

func (r *renderer) subagent(s trajectory.Subagent) {
	line := fmt.Sprintf("> **Subagent** %s", r.code(s.ID))
	if s.Label != "" {
		line += " " + r.code(s.Label)
	}
	line += fmt.Sprintf(" on %s: %s after %d steps, %s input / %s output tokens", r.code(s.Model), r.code(s.Status), s.Steps, formatInt(s.InputTokens), formatInt(s.OutputTokens))
	if s.Cost > 0 {
		line += ", " + formatCost(s.Cost)
	}
	r.printf("%s. Delegated task:\n\n", line)
	r.textLimited(s.Task)
}

func (r *renderer) footer() {
	t := r.t
	switch {
	case len(t.StructuredOutput) > 0:
		r.printf("## Structured output\n\n")
		r.fence("json", r.clean(prettyJSON(string(t.StructuredOutput))))
	case t.Submission != "":
		if trajectory.Finished(t.ExitStatus) {
			r.printf("## Submission\n\n")
		} else {
			r.printf("## Final message\n\n")
		}
		r.text(t.Submission)
	}
	if t.StopReason != "" {
		r.printf("## Stop hook feedback\n\n")
		r.textLimited(t.StopReason)
	}
	if len(t.Snapshots) > 0 {
		r.printf("## Snapshots\n\n")
		for _, s := range t.Snapshots {
			r.printf("- %s: %s, step %d, %d files\n", r.code(s.ID), r.code(s.Kind), s.Step, s.Files)
		}
		r.printf("\nRestore one with `tinyagent --rewind <trajectory.json> --cwd <root> --to <id>`.\n")
	}
}

// prettyJSON indents s when it is valid JSON and returns it unchanged
// otherwise.
func prettyJSON(s string) string {
	var b bytes.Buffer
	if err := json.Indent(&b, []byte(s), "", "  "); err != nil {
		return s
	}
	return b.String()
}

// formatCost uses 2 decimals from one cent up and 4 below it.
func formatCost(usd float64) string {
	if usd >= 0.01 {
		return fmt.Sprintf("$%.2f", usd)
	}
	return fmt.Sprintf("$%.4f", usd)
}

// formatInt adds thousands separators: 48213 -> 48,213.
func formatInt(n int) string { return model.FormatBytes(n) }
