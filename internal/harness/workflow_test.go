package harness

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestExampleWorkflowLoads(t *testing.T) {
	w, err := Load("../../docs/workflows/workflow.yaml")
	if err != nil {
		t.Fatal(err)
	}
	r, err := w.Route("Implement the spec in SPEC.md", "")
	if err != nil || r.Name != "implement" || len(r.Pipeline.Stages) != 5 {
		t.Fatalf("route = %+v, %v", r, err)
	}
	st := r.Pipeline.Stages
	if st[1].StepLimit != 100 || st[0].StepLimit != 50 || st[3].CheckTimeout != 600 || st[0].CheckTimeout != DefaultCheckTimeout {
		t.Errorf("settings = %+v", st)
	}
	if st[4].Skill.Name != "verify-behavior" || st[4].Behavior != ".tinyagent/behavior.yaml" || len(st[4].Check) != 0 {
		t.Errorf("verify-behavior stage = %+v", st[4])
	}
}

func TestGateKey(t *testing.T) {
	w := loadTestWorkflow(t, gated)
	r, _ := w.Route("", "build")
	if r.Pipeline.Stages[0].Gate != GateSpec || r.Pipeline.Stages[1].Gate != "" {
		t.Errorf("stages = %+v", r.Pipeline.Stages)
	}
	dir := writeFiles(t, t.TempDir(), map[string]string{
		"skills/plan-tasks.md": planSkill,
		"wf.yaml":              "version: 1\nworkflows:\n  build:\n    stages:\n      - skill: plan-tasks\n        gate: vibes\n",
	})
	if _, err := Load(filepath.Join(dir, "wf.yaml")); err == nil || !strings.Contains(err.Error(), `unknown gate "vibes"`) {
		t.Errorf("err = %v", err)
	}
	ex, err := Load("../../docs/workflows/workflow.yaml")
	if err != nil || ex.Pipelines["implement"].Stages[0].Gate != GateSpec {
		t.Errorf("example workflow gate: %v", err)
	}
}
