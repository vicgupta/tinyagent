package harness

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseMinimalSkill(t *testing.T) {
	s, err := ParseSkill("plan-tasks", "plan-tasks.md", []byte(planSkill))
	if err != nil {
		t.Fatal(err)
	}
	if s.Title != "plan-tasks" || !strings.HasPrefix(s.Intent, "Break a spec") {
		t.Errorf("title %q intent %q", s.Title, s.Intent)
	}
	if !reflect.DeepEqual(s.Inputs, []string{"spec", "repo_path"}) {
		t.Errorf("inputs = %q", s.Inputs)
	}
	if !strings.HasPrefix(s.SuccessCheck, "every task has acceptance criteria") {
		t.Errorf("success check = %q", s.SuccessCheck)
	}
	if !strings.Contains(s.Body, "# Skill: plan-tasks") {
		t.Errorf("body = %q", s.Body)
	}
}

func TestParseSkillFrontmatterAndInputDescriptions(t *testing.T) {
	src := "---\nname: review\ndescription: Review a diff for bugs.\n---\n# Review\n\n## Inputs\n\n- `diff`: the change\n- notes - optional\n\nSuccess check: no blocking issue is left unreported\n"
	s, err := ParseSkill("review", "SKILL.md", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if s.Intent != "Review a diff for bugs." || !reflect.DeepEqual(s.Inputs, []string{"diff", "notes"}) || s.SuccessCheck == "" {
		t.Errorf("got %+v", s)
	}
	if strings.Contains(s.Body, "description:") {
		t.Error("frontmatter left in body")
	}
}

func TestParseSkillMissingParts(t *testing.T) {
	for name, src := range map[string]string{
		"no heading": "> Intent: x\n**Inputs**\n- a\n",
		"no intent":  "# Skill: x\n**Inputs**\n- a\n",
		"no inputs":  "# Skill: x\n> Intent: y\n",
		"bad front":  "---\nname: [\n---\n# x\n",
	} {
		if _, err := ParseSkill("x", "x.md", []byte(src)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if _, err := ParseSkill("x", "x.md", []byte(strings.Repeat("a", MaxSkillBytes+1))); err == nil {
		t.Error("oversized skill accepted")
	}
}

func TestLoadSkills(t *testing.T) {
	dir := writeFiles(t, t.TempDir(), map[string]string{
		"plan-tasks.md":      planSkill,
		"run-tests/SKILL.md": testSkill,
		"README.md":          "not a skill",
		"notes.txt":          "ignored",
		"empty-folder/x.md":  "ignored",
	})
	got, err := LoadSkills(dir)
	if err != nil {
		t.Fatal(err)
	}
	if names := sortedNames(got); !reflect.DeepEqual(names, []string{"plan-tasks", "run-tests"}) {
		t.Errorf("names = %v", names)
	}
	writeFiles(t, dir, map[string]string{"plan-tasks/SKILL.md": planSkill})
	if _, err := LoadSkills(dir); err == nil || !strings.Contains(err.Error(), "defined twice") {
		t.Errorf("duplicate: %v", err)
	}
	bad := writeFiles(t, t.TempDir(), map[string]string{"Bad Name.md": planSkill})
	if _, err := LoadSkills(bad); err == nil {
		t.Error("bad name accepted")
	}
}

func TestLoadSkillFile(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "lint", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(good), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(good, []byte("# Skill: lint\n> Intent: Lint.\n\n**Inputs**\n- task\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := LoadSkillFile(good)
	if err != nil || s.Name != "lint" {
		t.Fatalf("got %+v, %v", s, err)
	}
	bad := filepath.Join(dir, "Bad Name.md")
	if err := os.WriteFile(bad, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSkillFile(bad); err == nil {
		t.Fatal("bad name accepted")
	}
}
