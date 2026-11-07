package agent

import (
	"encoding/json"
	"math"
	"sort"
	"strings"

	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/skills"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// ContextItem is one line of a context breakdown: a tool definition or a
// large tool result.
type ContextItem struct {
	Label  string
	Tokens int
}

// ContextUsage is how full the next model request's context window is.
// Token counts are estimates from the same character counter compaction
// uses (~4 characters per token, images at a fixed size). When Measured
// is set, the non-pending parts are scaled so they sum to the last turn's
// provider-reported usage, and Pending is the estimate of what was appended
// after that response.
type ContextUsage struct {
	Model     string
	Window    int
	NoCompact bool
	// Handoff means a CLI handoff provider owns the prompt. Only the
	// reported total is known; the category fields stay zero.
	Handoff     bool
	Measured    bool
	ReportedIn  int
	ReportedOut int
	Pending     int

	Used      int
	Threshold int // auto-compact point, 0 when the window is unknown

	System, Agents, Memory, Rules, Skills int
	SkillNames                            []string
	Tools                                 int
	ToolItems                             []ContextItem
	User, Assistant, ToolResults          int
	Largest                               []ContextItem
	Summary, Plan                         int
}

// Messages is the conversation aside from a compaction summary and from
// skill instructions, which are counted under Skills.
func (u ContextUsage) Messages() int {
	return u.User + u.Assistant + u.ToolResults
}

// largestToolResults is how many of the biggest tool results to list.
const largestToolResults = 3

type contextChunk struct {
	kind    string
	tokens  int
	pending bool
	label   string
}

// ContextUsage reports the context the next request would send.
// t is the conversation so far, or nil before the first turn.
// origin maps an external tool name to "mcp" or "plugin"; unknown
// external tools are labeled "external".
func (a *Agent) ContextUsage(t *trajectory.Trajectory, origin map[string]string) ContextUsage {
	name := a.activeModel()
	if name == "" {
		name = a.ModelName
	}
	u := ContextUsage{Model: name, Window: a.contextWindow(), NoCompact: a.NoCompact}
	if u.Window > 0 {
		u.Threshold = int(math.Round(compactAt * float64(u.Window)))
	}
	usage, measured := lastUsageOK(t)
	if model.IsHandoff(name) {
		u.Handoff = true
		if measured {
			u.Measured = true
			u.ReportedIn = usage.InputTokens
			u.ReportedOut = usage.OutputTokens
			u.Used = usage.InputTokens + usage.OutputTokens
		}
		return u
	}

	var msgs []model.Message
	var idx []int
	var v *view
	if t != nil && len(t.Messages) > 0 {
		v = viewFromTrajectory(t)
		msgs, idx = v.msgs, v.idx
	}
	if len(msgs) == 0 || msgs[0].Role != "system" {
		if sys := a.previewSystem(); sys != "" {
			msgs = append([]model.Message{{Role: "system", Content: sys}}, msgs...)
			idx = append([]int{-2}, idx...)
		}
	}

	var chunks []contextChunk
	calls := map[string]model.ToolCall{}
	for i, m := range msgs {
		ix := -2
		if i < len(idx) {
			ix = idx[i]
		}
		pending := measured && ix >= usage.Messages
		switch {
		case m.Role == "system":
			chunks = append(chunks, systemChunks(m)...)
		case ix == -1:
			chunks = append(chunks, contextChunk{kind: "summary", tokens: msgChars(m) / 4})
		case m.Role == "assistant":
			for _, c := range m.ToolCalls {
				calls[c.ID] = c
			}
			chunks = append(chunks, contextChunk{kind: "assistant", tokens: msgChars(m) / 4, pending: pending})
		case m.Role == "tool":
			if skill, ok := skillContentName(m.Content); ok {
				chunks = append(chunks, contextChunk{kind: "skills", tokens: msgChars(m) / 4, pending: pending, label: skill})
				continue
			}
			chunks = append(chunks, contextChunk{kind: "toolresult", tokens: msgChars(m) / 4, pending: pending, label: toolResultLabel(calls[m.ToolCallID])})
		default:
			chunks = append(chunks, contextChunk{kind: "user", tokens: msgChars(m) / 4, pending: pending})
		}
	}
	if t != nil && a.Tools != nil && a.Tools.Plan {
		if n := a.planChars(t) / 4; n > 0 {
			chunks = append(chunks, contextChunk{kind: "plan", tokens: n})
		}
	}
	if t != nil && v != nil {
		if block, _ := a.retainedSkills(t, v); block != "" {
			chunks = append(chunks, contextChunk{kind: "skills", tokens: len(block) / 4, label: "retained"})
			for _, n := range skillNamesIn(block) {
				chunks = append(chunks, contextChunk{kind: "skillname", label: n})
			}
		}
	}
	for _, spec := range a.toolSpecs() {
		n := toolSpecTokens(spec)
		label := spec.Function.Name
		if g := toolOrigin(spec.Function.Name, origin, a.externalNames()); g != "" {
			label = g + " " + label
		}
		chunks = append(chunks, contextChunk{kind: "tool", tokens: n, label: label})
	}

	if measured {
		target := usage.InputTokens + usage.OutputTokens
		if nonPendingTokens(chunks) == 0 && target > 0 {
			// The provider counted a prompt we could not attribute
			// (no messages in the view). Keep the total honest.
			chunks = append(chunks, contextChunk{kind: "system", tokens: target})
		} else {
			scaleChunks(chunks, target)
		}
		u.Measured = true
		u.ReportedIn = usage.InputTokens
		u.ReportedOut = usage.OutputTokens
	}
	u = fillUsage(u, chunks)
	if u.Window > 0 {
		u.Threshold = int(math.Round(compactAt * float64(u.Window)))
	}
	return u
}

func (a *Agent) previewSystem() string {
	if a.Tools == nil {
		return ""
	}
	return a.systemPrompt()
}

func (a *Agent) toolSpecs() []model.ToolSpec {
	if a.Tools == nil {
		return nil
	}
	specs := a.Tools.Catalog()
	if a.OutputSchema != nil {
		specs = append(specs, SubmitToolSpec(a.OutputSchema))
	}
	return specs
}

func (a *Agent) externalNames() map[string]bool {
	if a.Tools == nil {
		return nil
	}
	out := map[string]bool{}
	for _, s := range a.Tools.ExternalSpecs {
		out[s.Function.Name] = true
	}
	return out
}

func toolOrigin(name string, origin map[string]string, external map[string]bool) string {
	if g := origin[name]; g != "" {
		return g
	}
	if external[name] {
		return "external"
	}
	return ""
}

func toolSpecTokens(spec model.ToolSpec) int {
	b, err := json.Marshal(spec)
	if err != nil {
		return 0
	}
	return len(b) / 4
}

func lastUsageOK(t *trajectory.Trajectory) (trajectory.LastUsage, bool) {
	if t == nil || t.LastUsage == nil || t.LastUsage.InputTokens <= 0 {
		return trajectory.LastUsage{}, false
	}
	u := *t.LastUsage
	if u.Messages < 1 || u.Messages > len(t.Messages) || t.Messages[u.Messages-1].Role != "assistant" {
		return trajectory.LastUsage{}, false
	}
	for _, c := range t.Compactions {
		// A compaction recorded at or after this request replaced the
		// view that was measured.
		if c.Step >= u.Step {
			return trajectory.LastUsage{}, false
		}
	}
	return u, true
}

func fillUsage(u ContextUsage, chunks []contextChunk) ContextUsage {
	var largest []ContextItem
	seenSkill := map[string]bool{}
	for _, c := range chunks {
		if c.pending {
			u.Pending += c.tokens
		}
		u.Used += c.tokens
		switch c.kind {
		case "system":
			u.System += c.tokens
		case "agents":
			u.Agents += c.tokens
		case "memory":
			u.Memory += c.tokens
		case "rules":
			u.Rules += c.tokens
		case "skills":
			u.Skills += c.tokens
			addSkill(&u, seenSkill, c.label)
		case "skillname":
			addSkill(&u, seenSkill, c.label)
		case "tool":
			u.Tools += c.tokens
			if c.tokens > 0 {
				u.ToolItems = append(u.ToolItems, ContextItem{Label: c.label, Tokens: c.tokens})
			}
		case "user":
			u.User += c.tokens
		case "assistant":
			u.Assistant += c.tokens
		case "toolresult":
			u.ToolResults += c.tokens
			if c.tokens > 0 {
				largest = append(largest, ContextItem{Label: c.label, Tokens: c.tokens})
			}
		case "summary":
			u.Summary += c.tokens
		case "plan":
			u.Plan += c.tokens
		}
	}
	sort.Slice(u.ToolItems, func(i, j int) bool {
		if u.ToolItems[i].Tokens != u.ToolItems[j].Tokens {
			return u.ToolItems[i].Tokens > u.ToolItems[j].Tokens
		}
		return u.ToolItems[i].Label < u.ToolItems[j].Label
	})
	sort.Slice(largest, func(i, j int) bool {
		if largest[i].Tokens != largest[j].Tokens {
			return largest[i].Tokens > largest[j].Tokens
		}
		return largest[i].Label < largest[j].Label
	})
	if len(largest) > largestToolResults {
		largest = largest[:largestToolResults]
	}
	u.Largest = largest
	return u
}

func addSkill(u *ContextUsage, seen map[string]bool, name string) {
	if name == "" || name == "retained" || seen[name] {
		return
	}
	seen[name] = true
	u.SkillNames = append(u.SkillNames, name)
}

// scaleChunks scales non-pending chunks so they sum to target, leaving
// pending chunks as raw estimates. The split stays an estimate; only the
// total of the measured portion is the provider's number.
func nonPendingTokens(chunks []contextChunk) int {
	n := 0
	for _, c := range chunks {
		if !c.pending {
			n += c.tokens
		}
	}
	return n
}

func scaleChunks(chunks []contextChunk, target int) {
	sum := 0
	var idx []int
	for i, c := range chunks {
		if !c.pending {
			sum += c.tokens
			idx = append(idx, i)
		}
	}
	if sum == 0 || len(idx) == 0 || sum == target {
		return
	}
	type frac struct {
		i   int
		rem float64
	}
	var fs []frac
	used := 0
	for _, i := range idx {
		exact := float64(chunks[i].tokens) * float64(target) / float64(sum)
		base := int(math.Floor(exact))
		chunks[i].tokens = base
		used += base
		fs = append(fs, frac{i, exact - float64(base)})
	}
	sort.SliceStable(fs, func(a, b int) bool {
		if fs[a].rem != fs[b].rem {
			return fs[a].rem > fs[b].rem
		}
		return chunks[fs[a].i].tokens > chunks[fs[b].i].tokens
	})
	left := target - used
	if len(fs) == 0 {
		return
	}
	for left > 0 {
		for k := range fs {
			if left == 0 {
				break
			}
			chunks[fs[k].i].tokens++
			left--
		}
	}
}

func systemChunks(m model.Message) []contextChunk {
	system, agents, memory, rules, skillsText := splitSystem(m.Content)
	parts := []struct {
		kind, text string
	}{
		{"system", system},
		{"agents", agents},
		{"memory", memory},
		{"rules", rules},
		{"skills", skillsText},
	}
	var chunks []contextChunk
	sum := 0
	for _, p := range parts {
		if p.text == "" {
			continue
		}
		n := len(p.text) / 4
		sum += n
		chunks = append(chunks, contextChunk{kind: p.kind, tokens: n})
	}
	if whole := msgChars(m) / 4; whole > sum && len(chunks) > 0 {
		chunks[0].tokens += whole - sum
	}
	for _, name := range catalogSkillNames(skillsText) {
		chunks = append(chunks, contextChunk{kind: "skillname", label: name})
	}
	return chunks
}

// Section headers are the ones systemPrompt appends, matched from the end
// of each section so a copy of the words inside AGENTS.md is not required
// to be absent. Notices that follow the sections stay in the system part.
func splitSystem(content string) (system, agents, memory, rules, skills string) {
	type marker struct {
		header, kind string
	}
	markers := []marker{
		{"\n\n## Project instructions (AGENTS.md)\n\n", "agents"},
		{"\n\n# Project memory\n\nThe notes below were saved by earlier tinyagent runs", "memory"},
		{"\n\n## Project rules\n\nThese rules were recorded after earlier agent mistakes", "rules"},
		{"\n\n## Skills\n\nThese skills hold instructions for specific tasks.", "skills"},
	}
	type hit struct {
		at   int
		kind string
	}
	var hits []hit
	for _, m := range markers {
		if i := strings.LastIndex(content, m.header); i >= 0 {
			hits = append(hits, hit{at: i, kind: m.kind})
		}
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].at < hits[j].at })
	if len(hits) == 0 {
		return content, "", "", "", ""
	}
	var b strings.Builder
	prev := 0
	for i, h := range hits {
		b.WriteString(content[prev:h.at])
		var end int
		if i+1 < len(hits) {
			end = hits[i+1].at
		} else {
			end = systemSectionEnd(content, h.at)
		}
		section := content[h.at:end]
		switch h.kind {
		case "agents":
			agents = section
		case "memory":
			memory = section
		case "rules":
			rules = section
		case "skills":
			skills = section
		}
		prev = end
	}
	b.WriteString(content[prev:])
	return b.String(), agents, memory, rules, skills
}

func systemSectionEnd(content string, from int) int {
	end := len(content)
	for _, m := range []string{
		"\n\n<additional_directories>\n",
		"\n\n" + readOnlyNotice,
		"\n\n" + askUserNotice,
		"\n\n" + structuredOutputNotice,
		"\n\n" + workerNotice,
		"\n\n" + subagentNotice,
	} {
		if i := strings.Index(content[from:], m); i >= 0 {
			if at := from + i; at < end {
				end = at
			}
		}
	}
	return end
}

func catalogSkillNames(section string) []string {
	var names []string
	for _, line := range strings.Split(section, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		name, _, ok := strings.Cut(strings.TrimPrefix(line, "- "), ":")
		name = strings.TrimSpace(name)
		if ok && name != "" {
			names = append(names, name)
		}
	}
	return names
}

func skillContentName(content string) (string, bool) {
	if !strings.HasPrefix(content, skills.ContentPrefix) {
		return "", false
	}
	name, _, ok := strings.Cut(strings.TrimPrefix(content, skills.ContentPrefix), "\"")
	return name, ok && name != ""
}

func skillNamesIn(block string) []string {
	var names []string
	rest := block
	for {
		i := strings.Index(rest, skills.ContentPrefix)
		if i < 0 {
			return names
		}
		rest = rest[i+len(skills.ContentPrefix):]
		name, next, ok := strings.Cut(rest, "\"")
		if ok && name != "" {
			names = append(names, name)
		}
		rest = next
	}
}

func toolResultLabel(call model.ToolCall) string {
	name := call.Function.Name
	if name == "" {
		name = "tool"
	}
	var args map[string]any
	if json.Unmarshal([]byte(call.Function.Arguments), &args) == nil {
		for _, k := range []string{"command", "path", "url", "query", "pattern", "name", "file"} {
			s, ok := args[k].(string)
			if !ok || s == "" {
				continue
			}
			s = strings.Join(strings.Fields(s), " ")
			if len(s) > 40 {
				s = s[:40] + "…"
			}
			return name + " " + s
		}
	}
	return name
}
