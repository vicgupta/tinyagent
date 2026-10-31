package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/skills"
)

func skillCall(name, args string) model.ToolCall {
	return model.ToolCall{ID: "1", Type: "function", Function: model.FunctionCall{Name: name, Arguments: args}}
}

func TestSkillToolCatalogAndReadOnly(t *testing.T) {
	root := t.TempDir()
	userSkill := filepath.Join(root, "skills", "mine", "SKILL.md")
	os.MkdirAll(filepath.Dir(userSkill), 0o755)
	os.WriteFile(userSkill, []byte("---\nname: mine\ndescription: Mine.\n---\nuser body\n"), 0o644)
	os.Mkdir(filepath.Join(root, ".git"), 0o755)
	set, _ := skills.Discover(root)

	for _, ro := range []bool{false, true} {
		e := &Executor{Root: root, ReadOnly: ro}
		for _, s := range e.Catalog() {
			if s.Function.Name == "skill" {
				t.Errorf("read-only %v: skill offered without skills", ro)
			}
		}
		e.Skills = set
		found := false
		for _, s := range e.Catalog() {
			found = found || s.Function.Name == "skill"
		}
		if !found {
			t.Errorf("read-only %v: skill not offered", ro)
		}
		r := e.RunResult(context.Background(), skillCall("skill", `{"name":"mine"}`))
		out := r.Text
		if r.IsError || !strings.Contains(out, "user body") {
			t.Errorf("read-only %v: %q", ro, out)
		}
	}
}
