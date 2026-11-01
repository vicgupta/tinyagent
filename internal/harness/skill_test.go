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

// TestParseSkillBackticks: inline code spans survive in the intent and
// success check, bold around a code span is removed without eating the
// backticks, and lines inside fenced code blocks are never parsed as the
// skill's heading, intent, inputs or success check, while the fence stays
// verbatim in Body (the stage prompt).
func TestParseSkillBackticks(t *testing.T) {
	src := "# Skill: tests\n> **Intent:** Run `go test ./...` and fix `--json` output\n\n" +
		"```md\n# not the title\n> Intent: fake\n**Inputs**\n- fake\n**Success check:** fake check\n```\n\n" +
		"~~~sh\n- also-fake\n~~~\n\n" +
		"**Inputs**\n- `spec`: the `-t` task\n- `repo_path`\n\n" +
		"**Success check:** **`make test`** passes and `a__b` is kept\n"
	s, err := ParseSkill("tests", "tests.md", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if s.Title != "tests" {
		t.Errorf("title = %q", s.Title)
	}
	if s.Intent != "Run `go test ./...` and fix `--json` output" {
		t.Errorf("intent = %q", s.Intent)
	}
	if strings.Join(s.Inputs, ",") != "spec,repo_path" {
		t.Errorf("inputs = %q", s.Inputs)
	}
	if s.SuccessCheck != "`make test` passes and `a__b` is kept" {
		t.Errorf("success check = %q", s.SuccessCheck)
	}
	for _, want := range []string{"```md\n# not the title", "- fake\n", "~~~sh\n- also-fake\n~~~"} {
		if !strings.Contains(s.Body, want) {
			t.Errorf("body lost %q", want)
		}
	}
	st := Stage{Skill: s}
	if p := Prompt(1, 1, st, "fix `x`", "", "", "/w", ""); !strings.Contains(p, "```md\n# not the title") || !strings.Contains(p, "fix `x`") || !strings.Contains(p, "`make test` passes") {
		t.Errorf("prompt lost backtick content:\n%s", p)
	}
}

// TestParseSkillFenceBeforeHeading: a "# comment" in a fence before the
// heading doesn't become the title, so frontmatter name is used.
func TestParseSkillFenceBeforeHeading(t *testing.T) {
	src := "---\nname: setup\ndescription: Install deps with `go mod download`\n---\n```sh\n# install deps\ngo mod download\n```\n\n**Inputs**\n- spec\n"
	s, err := ParseSkill("setup", "SKILL.md", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	if s.Title != "setup" || s.Intent != "Install deps with `go mod download`" {
		t.Errorf("title %q intent %q", s.Title, s.Intent)
	}
}
