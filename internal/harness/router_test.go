package harness

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func loadTestWorkflow(t *testing.T, yaml string) *Workflow {
	t.Helper()
	dir := writeFiles(t, t.TempDir(), map[string]string{
		"skills/plan-tasks.md": planSkill,
		"skills/run-tests.md":  testSkill,
		"wf.yaml":              yaml,
	})
	w, err := Load(filepath.Join(dir, "wf.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return w
}

const twoStage = "version: 1\nworkflows:\n  build:\n    intent: implement a feature end to end with planning and tests\n    stages:\n      - skill: plan-tasks\n      - skill: run-tests\n"

func TestRouteByName(t *testing.T) {
	w := loadTestWorkflow(t, twoStage)
	r, err := w.Route("anything", "run-tests")
	if err != nil || r.Kind != "skill" || r.How != "named" || len(r.Pipeline.Stages) != 1 {
		t.Fatalf("route = %+v, %v", r, err)
	}
	if r.Pipeline.Stages[0].StepLimit != DefaultStepLimit || r.Pipeline.Stages[0].Retries != DefaultRetries {
		t.Errorf("defaults not applied: %+v", r.Pipeline.Stages[0])
	}
	if _, err := w.Route("x", "nope"); err == nil {
		t.Error("unknown name accepted")
	}
}

func TestRouteByIntent(t *testing.T) {
	w := loadTestWorkflow(t, twoStage)
	r, err := w.Route("Implement the export feature end to end, with tests", "")
	if err != nil || r.Name != "build" || r.How != "intent" {
		t.Fatalf("route = %+v, %v", r, err)
	}
	r, err = w.Route("break this spec into ordered tasks", "")
	if err != nil || r.Name != "plan-tasks" || r.Kind != "skill" {
		t.Fatalf("route = %+v, %v", r, err)
	}
	r, err = w.Route("please do run-tests now", "")
	if err != nil || r.Name != "run-tests" {
		t.Fatalf("named in task: %+v, %v", r, err)
	}
}

func TestRouteNoMatch(t *testing.T) {
	w := loadTestWorkflow(t, twoStage)
	_, err := w.Route("bake a cake", "")
	var re *RouteError
	if !errors.As(err, &re) {
		t.Fatalf("err = %v, want RouteError", err)
	}
	w = loadTestWorkflow(t, twoStage+"default: build\n")
	r, err := w.Route("bake a cake", "")
	if err != nil || r.Name != "build" || r.How != "default" {
		t.Fatalf("default route = %+v, %v", r, err)
	}
}

func TestLoadErrors(t *testing.T) {
	for name, yaml := range map[string]string{
		"version":       "version: 2\n",
		"unknown key":   "version: 1\nbogus: 1\n",
		"unknown skill": "version: 1\nworkflows:\n  a:\n    stages:\n      - skill: nope\n",
		"no stages":     "version: 1\nworkflows:\n  a:\n    stages: []\n",
		"retries":       "version: 1\nworkflows:\n  a:\n    stages:\n      - skill: plan-tasks\n        retries: 99\n",
		"empty check":   "version: 1\nworkflows:\n  a:\n    stages:\n      - skill: plan-tasks\n        check: []\n",
		"bad default":   "version: 1\ndefault: nope\n",
		"name clash":    "version: 1\nworkflows:\n  plan-tasks:\n    stages:\n      - skill: plan-tasks\n",
		"neg limit":     "version: 1\ndefaults:\n  step_limit: -1\n",
	} {
		dir := writeFiles(t, t.TempDir(), map[string]string{"skills/plan-tasks.md": planSkill, "wf.yaml": yaml})
		if _, err := Load(filepath.Join(dir, "wf.yaml")); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Error("missing file accepted")
	}
	dir := writeFiles(t, t.TempDir(), map[string]string{"wf.yaml": "version: 1\n"})
	os.Mkdir(filepath.Join(dir, "skills"), 0o755)
	if _, err := Load(filepath.Join(dir, "wf.yaml")); err == nil {
		t.Error("empty skills dir accepted")
	}
}

func TestSkillWithoutCheckNeedsCommand(t *testing.T) {
	noCheck := "# Skill: fmt\n> Intent: format the code\n\n**Inputs**\n- task\n"
	for yaml, ok := range map[string]bool{
		"version: 1\nworkflows:\n  a:\n    stages:\n      - skill: fmt\n":                            false,
		"version: 1\nworkflows:\n  a:\n    stages:\n      - skill: fmt\n        check: [\"true\"]\n": true,
	} {
		dir := writeFiles(t, t.TempDir(), map[string]string{"skills/fmt.md": noCheck, "wf.yaml": yaml})
		w, err := Load(filepath.Join(dir, "wf.yaml"))
		if (err == nil) != ok {
			t.Errorf("%q: err = %v", yaml, err)
		}
		if err == nil {
			if _, err := w.Route("x", "fmt"); err == nil {
				t.Error("check-less skill routable on its own")
			}
		}
	}
}

func TestStem(t *testing.T) {
	for in, want := range map[string]string{"tests": "test", "planning": "plan", "ordered": "order", "tasks": "task"} {
		if got := stem(in); got != want {
			t.Errorf("stem(%q) = %q, want %q", in, got, want)
		}
	}
}
