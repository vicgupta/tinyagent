package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkill(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func skillMD(name string, check bool) string {
	s := "# Skill: " + name + "\n> Intent: Do a thing.\n\n**Inputs**\n- task\n"
	if check {
		s += "\n**Success check:** tests pass.\n"
	}
	return s
}

func TestSkillsValidateExamples(t *testing.T) {
	code, out, errOut := runCLI(t, "skills", "validate", "../../docs/workflows/workflow.yaml", "../../docs/workflows/skills")
	if code != 0 {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	for _, want := range []string{"ok plan-tasks", "ok verify-behavior", "ok workflow file"} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q:\n%s", want, out)
		}
	}
}

func TestSkillsValidateFileAndDir(t *testing.T) {
	dir := t.TempDir()
	writeSkill(t, filepath.Join(dir, "a.md"), skillMD("a", true))
	writeSkill(t, filepath.Join(dir, "b", "SKILL.md"), skillMD("b", false))
	code, out, errOut := runCLI(t, "skills", "validate", filepath.Join(dir, "a.md"), dir)
	if code != 0 {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	if strings.Count(out, "ok a\n") != 2 || !strings.Contains(out, "ok b\n") {
		t.Errorf("stdout %q", out)
	}
	if !strings.Contains(errOut, "skill b: no success check: needs a check: command when used as a stage") {
		t.Errorf("missing warning: %q", errOut)
	}
}

func TestSkillsValidateInvalid(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "broken.md")
	writeSkill(t, bad, "# Skill: broken\nno intent here\n")
	code, _, errOut := runCLI(t, "--skills-validate", bad)
	if code != 1 || !strings.Contains(errOut, "broken") {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
	code, _, errOut = runCLI(t, "skills", "validate", filepath.Join(dir, "missing.md"))
	if code != 2 {
		t.Fatalf("missing path: code %d, stderr %q", code, errOut)
	}
	wf := filepath.Join(dir, "wf.yaml")
	writeSkill(t, wf, "version: 1\nskills_dir: .\nworkflows:\n  w:\n    intent: x\n    stages:\n      - skill: nope\n")
	code, _, errOut = runCLI(t, "skills", "validate", wf)
	if code != 1 {
		t.Fatalf("bad workflow: code %d, stderr %q", code, errOut)
	}
}

func TestSkillsSubcommandHelpAndArgs(t *testing.T) {
	defer func(f func() bool) { stdinIsTerminal = f }(stdinIsTerminal)
	stdinIsTerminal = func() bool { return false }
	code, out, _ := runWith(t, "skills")
	if code != 0 || !strings.Contains(out, "validate <path>...") {
		t.Fatalf("bare skills: code %d, out %q", code, out)
	}
	code, _, errOut := runCLI(t, "skills", "validate")
	if code != 2 || !strings.Contains(errOut, "needs at least one value") {
		t.Fatalf("no paths: code %d, stderr %q", code, errOut)
	}
}

func TestSkillsValidateConflicts(t *testing.T) {
	code, _, errOut := runCLI(t, "--skills-validate", "../../docs/workflows/skills", "-t", "x")
	if code != 2 || !strings.Contains(errOut, "can't be combined") {
		t.Fatalf("code %d, stderr %q", code, errOut)
	}
}
