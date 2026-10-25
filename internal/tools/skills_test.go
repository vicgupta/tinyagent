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
	home := t.TempDir()
	t.Setenv("HOME", home)
	userSkill := filepath.Join(home, ".agents", "skills", "mine", "SKILL.md")
	os.MkdirAll(filepath.Dir(userSkill), 0o755)
	os.WriteFile(userSkill, []byte("---\nname: mine\ndescription: Mine.\n---\nuser body\n"), 0o644)
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, ".git"), 0o755)
	set, _ := skills.Discover(root, skills.UserDir())

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
		out, isErr := e.RunWithError(context.Background(), skillCall("skill", `{"name":"mine"}`))
		if isErr || !strings.Contains(out, "user body") {
			t.Errorf("read-only %v: %q", ro, out)
		}
		// AC7: other tools still cannot read outside the root.
		out, _ = e.RunWithError(context.Background(), skillCall("read_file", `{"path":"`+userSkill+`"}`))
		if strings.Contains(out, "user body") {
			t.Errorf("read-only %v: read_file escaped the root: %q", ro, out)
		}
	}
}
