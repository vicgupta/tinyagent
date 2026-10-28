package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/skills"
	"github.com/vicgupta/tinyagent/internal/tools"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// maxRetainedSkillChars caps the loaded skill instructions re-attached after
// a compaction summary replaced the messages that loaded them.
const maxRetainedSkillChars = 32 << 10

const retainedHeader = "\n\n[Skill instructions loaded earlier in this run, kept verbatim through compaction]\n\n"

func skillInfos(s *skills.Set) []trajectory.SkillInfo {
	var out []trajectory.SkillInfo
	if s == nil {
		return nil
	}
	for _, k := range s.Skills {
		out = append(out, trajectory.SkillInfo{Name: k.Name, Scope: k.Scope, Path: k.Path, Plugin: k.Plugin})
	}
	return out
}

// skillCallArgs returns the name and file arguments of a skill call.
func skillCallArgs(call model.ToolCall) (name, file string, ok bool) {
	if call.Function.Name != skills.ToolName {
		return "", "", false
	}
	var a struct {
		Name string `json:"name"`
		File string `json:"file"`
	}
	if json.Unmarshal([]byte(call.Function.Arguments), &a) != nil {
		return "", "", false
	}
	return a.Name, a.File, true
}

// skillLoads maps the index in t.Messages of every tool result that returned
// a skill's instructions (a skill call without file) to the skill name.
func (a *Agent) skillLoads(t *trajectory.Trajectory) map[int]string {
	if a.Tools.Skills.Len() == 0 {
		return nil
	}
	calls := map[string]model.ToolCall{}
	out := map[int]string{}
	for i, m := range t.Messages {
		for _, c := range m.ToolCalls {
			calls[c.ID] = c
		}
		if m.Role != "tool" || !strings.HasPrefix(m.Content, skills.ContentPrefix) {
			continue
		}
		if name, file, ok := skillCallArgs(calls[m.ToolCallID]); ok && file == "" {
			out[i] = name
		}
	}
	return out
}

// isSkillLoad reports whether view message ix (an index into t.Messages)
// returned skill instructions; stage-1 compaction never elides those.
func isSkillLoad(loads map[int]string, ix int) bool {
	_, ok := loads[ix]
	return ok
}

// retainedSkills returns, when a summary has replaced earlier turns, the most
// recent load of each skill whose loading message is no longer in the view,
// in load order and within maxRetainedSkillChars, plus the names kept.
func (a *Agent) retainedSkills(t *trajectory.Trajectory, v *view) (string, map[string]bool) {
	loads := a.skillLoads(t)
	if len(loads) == 0 {
		return "", nil
	}
	summarized := false
	inView := map[int]bool{}
	for _, ix := range v.idx {
		if ix == -1 {
			summarized = true
		}
		inView[ix] = true
	}
	if !summarized {
		return "", nil
	}
	latest := map[string]int{}
	for ix, name := range loads {
		if ix > latest[name] {
			latest[name] = ix
		}
	}
	var sb strings.Builder
	kept := map[string]bool{}
	var omitted []string
	for ix := range t.Messages {
		name, ok := loads[ix]
		if !ok || latest[name] != ix || inView[ix] {
			continue
		}
		c := t.Messages[ix].Content
		if sb.Len()+len(c)+2 > maxRetainedSkillChars {
			omitted = append(omitted, name)
			continue
		}
		sb.WriteString(c)
		sb.WriteString("\n\n")
		kept[name] = true
	}
	if len(omitted) > 0 {
		fmt.Fprintf(&sb, "[Not kept (over the %d character cap); load again with the skill tool if needed: %s]\n\n", maxRetainedSkillChars, strings.Join(omitted, ", "))
	}
	if sb.Len() == 0 {
		return "", kept
	}
	return retainedHeader + strings.TrimRight(sb.String(), "\n"), kept
}

// request returns the messages sent to the model: the view with the plan
// pinned and any summarized skill instructions re-attached to the summary.
// It copies, so the view and trajectory never contain the extra text.
func (a *Agent) request(t *trajectory.Trajectory, v *view) []model.Message {
	out := a.withPlan(t, v)
	block, _ := a.retainedSkills(t, v)
	if block == "" {
		return out
	}
	if len(out) > 0 && &out[0] == &v.msgs[0] {
		out = append([]model.Message(nil), out...)
	}
	for i, ix := range v.idx {
		if ix == -1 {
			out[i].Content += block
			break
		}
	}
	return out
}

func (a *Agent) retainedChars(t *trajectory.Trajectory, v *view) int {
	block, _ := a.retainedSkills(t, v)
	return len(block)
}

// execTool runs a permitted call. A skill call whose instructions are still
// in context gets a short note instead of a second copy.
func (a *Agent) execTool(ctx context.Context, t *trajectory.Trajectory, call model.ToolCall) tools.Result {
	if call.Function.Name == tools.DelegateToolName && a.Tools.Delegate {
		return a.delegate(ctx, t, call)
	}
	if res, ok := a.workerCall(ctx, t, call); ok {
		return res
	}
	if name, file, ok := skillCallArgs(call); ok && file == "" && a.Tools.Skills.Len() > 0 && a.view != nil && a.skillInContext(t, a.view, name) {
		return tools.Result{Text: fmt.Sprintf("skill %q is already loaded earlier in this conversation; follow those instructions (no second copy was added)", name)}
	}
	res := a.Tools.RunResult(ctx, call)
	a.deliverScoped(t, call, &res)
	return res
}

func (a *Agent) skillInContext(t *trajectory.Trajectory, v *view, name string) bool {
	loads := a.skillLoads(t)
	for _, ix := range v.idx {
		if n, ok := loads[ix]; ok && n == name {
			return true
		}
	}
	_, kept := a.retainedSkills(t, v)
	return kept[name]
}
