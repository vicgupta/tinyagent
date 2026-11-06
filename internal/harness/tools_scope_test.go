package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkflowToolsPrecedence(t *testing.T) {
	dir := t.TempDir()
	skills := filepath.Join(dir, "skills")
	os.MkdirAll(skills, 0o755)
	os.WriteFile(filepath.Join(skills, "s.md"), []byte("---\nname: s\ntools: [read_file]\n---\n# S\n\nIntent: do s\n\nInputs:\n- x\n\nSuccess check: done\n\nBody.\n"), 0o644)
	wf := filepath.Join(dir, "workflow.yaml")
	os.WriteFile(wf, []byte(`version: 1
skills_dir: skills
defaults:
  tools: [read_file, grep]
  step_limit: 5
workflows:
  main:
    stages:
      - skill: s
        tools: [read_file, list_files]
`), 0o644)
	w, err := Load(wf)
	if err != nil {
		t.Fatal(err)
	}
	p := w.Pipelines["main"]
	if p == nil || len(p.Stages) != 1 {
		t.Fatalf("%+v", w.Pipelines)
	}
	got := strings.Join(p.Stages[0].Tools, ",")
	if got != "read_file,list_files" {
		t.Fatalf("stage tools = %q (want stage > skill > defaults)", got)
	}
}

func TestWorkflowToolsFromSkillAndDefaults(t *testing.T) {
	dir := t.TempDir()
	skills := filepath.Join(dir, "skills")
	os.MkdirAll(skills, 0o755)
	os.WriteFile(filepath.Join(skills, "s.md"), []byte("---\ntools: [grep]\n---\n# S\n\nIntent: do s\n\nInputs:\n- x\n\nSuccess check: done\n\nBody.\n"), 0o644)
	wf := filepath.Join(dir, "workflow.yaml")
	os.WriteFile(wf, []byte(`version: 1
skills_dir: skills
defaults:
  tools: [read_file]
  step_limit: 5
workflows:
  main:
    stages:
      - skill: s
`), 0o644)
	w, err := Load(wf)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(w.Pipelines["main"].Stages[0].Tools, ",")
	if got != "grep" {
		t.Fatalf("skill tools should beat defaults: %q", got)
	}
}

func TestWorkflowToolsRejectsDuplicates(t *testing.T) {
	dir := t.TempDir()
	skills := filepath.Join(dir, "skills")
	os.MkdirAll(skills, 0o755)
	os.WriteFile(filepath.Join(skills, "s.md"), []byte("# S\n\nIntent: do s\n\nInputs:\n- x\n\nSuccess check: done\n\nBody.\n"), 0o644)
	wf := filepath.Join(dir, "workflow.yaml")
	os.WriteFile(wf, []byte(`version: 1
skills_dir: skills
defaults:
  tools: [read_file, read_file]
workflows:
  main:
    stages:
      - skill: s
`), 0o644)
	if _, err := Load(wf); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("got %v", err)
	}
}
