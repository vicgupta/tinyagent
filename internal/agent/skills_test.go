package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/hooks/hookstest"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/skills"
)

const skillBody = "# Release\nAlways run make release-check first."

func writeSkill(t *testing.T, root, name, desc string) {
	t.Helper()
	dir := filepath.Join(root, ".agents", "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: " + name + "\ndescription: " + desc + "\n---\n" + skillBody + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(md), 0o644); err != nil {
		t.Fatal(err)
	}
}

// enableSkills writes a project skill into a's root, isolates HOME, and
// discovers skills the way the CLI does.
func enableSkills(t *testing.T, a *Agent) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	writeSkill(t, a.Tools.Root, "release", "Cut a release of this repo.")
	set, warns := skills.Discover(a.Tools.Root, skills.UserDir())
	if set.Len() != 1 || len(warns) != 0 {
		t.Fatalf("discover: %v %v", set.Names(), warns)
	}
	a.Tools.Skills = set
	a.SkillsEnabled = true
}

var loadRelease = call("s1", "skill", `{"name":"release"}`)

func TestSkillsCatalogAndToolInFirstRequest(t *testing.T) {
	f := &fakeModel{replies: []reply{
		{msg: model.Message{Role: "assistant", ToolCalls: []model.ToolCall{loadRelease}}},
		{msg: submit},
	}}
	a := newAgent(t, f)
	enableSkills(t, a)
	tr := a.Run(context.Background(), "ship it")
	if tr.ExitStatus != StatusSubmitted {
		t.Fatalf("status %s", tr.ExitStatus)
	}
	first := f.agentRequests()[0]
	sys := first.msgs[0].Content
	if !strings.Contains(sys, "## Skills") || !strings.Contains(sys, "release: Cut a release of this repo.") || strings.Contains(sys, "make release-check") {
		t.Errorf("system prompt = %q", sys)
	}
	var spec *model.ToolSpec
	for i := range first.tools {
		if first.tools[i].Function.Name == "skill" {
			spec = &first.tools[i]
		}
	}
	if spec == nil {
		t.Fatalf("no skill tool in %+v", first.tools)
	}
	if !strings.Contains(fmt.Sprint(spec.Function.Parameters), "enum:[release]") {
		t.Errorf("spec = %+v", spec.Function)
	}
	res := toolResults(tr.Messages)
	if len(res) != 1 || !strings.Contains(res[0], skillBody) || !strings.HasPrefix(res[0], skills.ContentPrefix) {
		t.Errorf("results = %q", res)
	}
	if !tr.SkillsEnabled || len(tr.Skills) != 1 || tr.Skills[0].Name != "release" || tr.Skills[0].Scope != skills.ScopeProject {
		t.Errorf("trajectory skills = %v %+v", tr.SkillsEnabled, tr.Skills)
	}
}

func TestNoSkillsLeavesPromptAndToolsUnchanged(t *testing.T) {
	run := func(enable bool) request {
		f := &fakeModel{replies: []reply{{msg: submit}}}
		a := newAgent(t, f)
		t.Setenv("HOME", t.TempDir())
		if enable {
			set, _ := skills.Discover(a.Tools.Root, skills.UserDir())
			a.Tools.Skills, a.SkillsEnabled = set, true
		}
		a.Run(context.Background(), "task")
		r := f.agentRequests()[0]
		r.msgs[0].Content = strings.ReplaceAll(r.msgs[0].Content, a.Tools.Root, "ROOT")
		return r
	}
	off, on := run(false), run(true)
	if !reflect.DeepEqual(off.msgs, on.msgs) || !reflect.DeepEqual(off.tools, on.tools) {
		t.Errorf("enabled with zero skills changed the request")
	}
}

func TestSkillLoadDeduped(t *testing.T) {
	m := &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{loadRelease}},
		{Role: "assistant", ToolCalls: []model.ToolCall{call("s2", "skill", `{"name":"release"}`)}},
		{Role: "assistant", Content: "ok"},
	}}
	a := newAgent(t, m)
	enableSkills(t, a)
	tr := a.Run(context.Background(), "x")
	res := toolResults(tr.Messages)
	if len(res) != 2 || !strings.Contains(res[0], skillBody) || strings.Contains(res[1], skillBody) || !strings.Contains(res[1], "already loaded") {
		t.Errorf("results = %q", res)
	}
}

func TestSkillLoadSurvivesElision(t *testing.T) {
	f := &fakeModel{replies: []reply{
		{msg: model.Message{Role: "assistant", Content: "step 1", ToolCalls: []model.ToolCall{loadRelease}}},
		{msg: reads(2, 1)}, {msg: reads(3, 1)}, {msg: reads(4, 1)}, {msg: reads(5, 1)},
		{msg: reads(6, 1)}, {msg: reads(7, 1), input: 15000},
		{msg: submit},
	}}
	a, _ := compactAgent(t, f, 20000)
	enableSkills(t, a)
	tr := a.Run(context.Background(), "task")
	if len(tr.Compactions) == 0 || tr.Compactions[0].Stage != "elide" {
		t.Fatalf("compactions = %+v", tr.Compactions)
	}
	if reflect.DeepEqual(tr.Compactions[0].Elided, []int{}) || containsInt(tr.Compactions[0].Elided, 3) {
		t.Errorf("elided = %v", tr.Compactions[0].Elided)
	}
	reqs := f.agentRequests()
	last := reqs[len(reqs)-1].msgs
	if !strings.Contains(last[3].Content, skillBody) || !strings.HasPrefix(last[5].Content, elidedPrefix) {
		t.Errorf("skill %.60q / read %.60q", last[3].Content, last[5].Content)
	}
}

func containsInt(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func TestSkillRetainedAfterSummary(t *testing.T) {
	step1 := reads(1, 2)
	step1.ToolCalls = append([]model.ToolCall{loadRelease}, step1.ToolCalls...)
	f := &fakeModel{summary: "## Task\nship", replies: []reply{
		{msg: step1},
		{msg: reads(2, 3)}, {msg: reads(3, 1), input: 23000},
		{msg: model.Message{Role: "assistant", ToolCalls: []model.ToolCall{call("s2", "skill", `{"name":"release"}`)}}},
		{msg: submit},
	}}
	a, _ := compactAgent(t, f, 30000)
	enableSkills(t, a)
	tr := a.Run(context.Background(), "task")
	if tr.ExitStatus != StatusSubmitted || len(tr.Compactions) != 1 || tr.Compactions[0].Stage != "summarize" {
		t.Fatalf("status %s (%s) compactions %+v", tr.ExitStatus, tr.Submission, tr.Compactions)
	}
	reqs := f.agentRequests()
	after := reqs[3].msgs
	if after[2].Role != "user" || !strings.HasPrefix(after[2].Content, summaryHeader) || !strings.Contains(after[2].Content, skillBody) {
		t.Errorf("summary message = %q", after[2].Content)
	}
	// The re-attached copy counts as loaded, and never enters the trajectory.
	res := toolResults(tr.Messages)
	if !strings.Contains(res[len(res)-1], "already loaded") {
		t.Errorf("reload result = %q", res[len(res)-1])
	}
	for _, m := range tr.Messages {
		if m.Role == "user" && strings.Contains(m.Content, summaryHeader) && strings.Contains(m.Content, skillBody) {
			t.Errorf("retained block persisted: %q", m.Content)
		}
	}
	checkPairing(t, f.reqs)
}

func TestSkillToolHonorsPreToolHook(t *testing.T) {
	m := &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{loadRelease}},
		{Role: "assistant", Content: "ok"},
	}}
	a := newAgent(t, m)
	enableSkills(t, a)
	withHooks(t, a, config.Hooks{PreTool: []config.Hook{hookstest.Hook(t, "skill", "exit2", "skills are blocked")}})
	tr := a.Run(context.Background(), "x")
	res := toolResults(tr.Messages)
	if len(res) != 1 || !strings.Contains(res[0], "skills are blocked") || strings.Contains(res[0], skillBody) {
		t.Errorf("results = %q", res)
	}
}

func TestSkillToolInReadOnly(t *testing.T) {
	m := &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{loadRelease, call("b", "bash", `{"command":"true"}`)}},
		{Role: "assistant", Content: "ok"},
	}}
	a := newAgent(t, m)
	a.Config.ReadOnly = true
	a.Tools.ReadOnly = true
	enableSkills(t, a)
	tr := a.Run(context.Background(), "x")
	res := toolResults(tr.Messages)
	if len(res) != 2 || !strings.Contains(res[0], skillBody) || !strings.Contains(res[1], "unavailable in read-only mode") {
		t.Errorf("results = %q", res)
	}
}

func TestResumeRefusesChangedSkills(t *testing.T) {
	m := &scripted{replies: []model.Message{{Role: "assistant", ToolCalls: []model.ToolCall{loadRelease}}}}
	a := newAgent(t, m)
	enableSkills(t, a)
	a.Config.StepLimit = 1
	tr := a.Run(context.Background(), "x")
	if tr.ExitStatus != StatusStepLimit {
		t.Fatalf("status %s", tr.ExitStatus)
	}
	if err := ValidateResume(tr, a.Config, a.Tools.Root); err != nil {
		t.Fatalf("unchanged: %v", err)
	}
	writeSkill(t, a.Tools.Root, "release", "A different description.")
	if err := ValidateResume(tr, a.Config, a.Tools.Root); err == nil || !strings.Contains(err.Error(), "skills catalog") {
		t.Errorf("changed description: %v", err)
	}
	writeSkill(t, a.Tools.Root, "release", "Cut a release of this repo.")
	writeSkill(t, a.Tools.Root, "extra", "Another.")
	if err := ValidateResume(tr, a.Config, a.Tools.Root); err == nil {
		t.Error("added skill: no error")
	}
}

func TestLegacyCheckpointIgnoresSkillDirs(t *testing.T) {
	m := &scripted{replies: []model.Message{{Role: "assistant", ToolCalls: []model.ToolCall{call("r", "list_dir", `{"path":"."}`)}}}}
	a := newAgent(t, m)
	a.Config.StepLimit = 1
	t.Setenv("HOME", t.TempDir())
	tr := a.Run(context.Background(), "x")
	if tr.SkillsEnabled || tr.Skills != nil {
		t.Fatalf("skills recorded: %+v", tr.Skills)
	}
	writeSkill(t, a.Tools.Root, "release", "New skill.")
	if err := ValidateResume(tr, a.Config, a.Tools.Root); err != nil {
		t.Errorf("legacy resume: %v", err)
	}
}
