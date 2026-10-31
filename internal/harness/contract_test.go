package harness

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestExampleContractWorkflow(t *testing.T) {
	w, err := Load("../../docs/workflows/workflow.yaml")
	if err != nil {
		t.Fatal(err)
	}
	sk := w.Skills["write-contract"]
	if sk == nil {
		t.Fatal("write-contract skill not loaded")
	}
	for _, in := range []string{"surface", "repo_path", "contracts_dir"} {
		if !contains(sk.Inputs, in) {
			t.Errorf("write-contract inputs %v missing %q", sk.Inputs, in)
		}
	}
	if !strings.Contains(sk.Body, "# Contract: <surface>") || !strings.Contains(sk.Body, "Last verified:") {
		t.Error("write-contract skill doesn't define the contract header")
	}
	p := w.Pipelines["contract"]
	if p == nil || len(p.Stages) != 1 || p.Stages[0].Skill.Name != "write-contract" || len(p.Stages[0].Check) == 0 {
		t.Fatalf("contract pipeline = %+v", p)
	}
	if p.Stages[0].Inputs["contracts_dir"] != "docs/contracts" {
		t.Errorf("contract stage inputs = %v", p.Stages[0].Inputs)
	}
	r, err := w.Route("Write a contract doc for the internal/tools executor surface", "")
	if err != nil || r.Name != "contract" {
		t.Errorf("contract task routed to %+v, %v", r, err)
	}
	r, err = w.Route("Implement the spec in SPEC.md", "")
	if err != nil || r.Name != "implement" {
		t.Errorf("implement task routed to %+v, %v", r, err)
	}
}

func TestContractWorkflowCheck(t *testing.T) {
	w, err := Load("../../docs/workflows/workflow.yaml")
	if err != nil {
		t.Fatal(err)
	}
	argv := w.Pipelines["contract"].Stages[0].Check
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "docs", "contracts"), 0o755); err != nil {
		t.Fatal(err)
	}
	doc := filepath.Join(dir, "docs", "contracts", "tools.md")
	if err := os.WriteFile(doc, []byte("# Contract: tools\nCovers: internal/tools\nLast verified: abc1234\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath(argv[0]); err != nil {
		t.Skipf("%s not on PATH", argv[0])
	}
	run := func(d string) error {
		c := exec.Command(argv[0], argv[1:]...)
		c.Dir = d
		return c.Run()
	}
	if err := run(dir); err != nil {
		t.Errorf("check %v failed with a contract doc present: %v", argv, err)
	}
	if err := run(t.TempDir()); err == nil {
		t.Errorf("check %v passed with no contract doc", argv)
	}
}

func TestPlanTasksGetsContractsDir(t *testing.T) {
	w, err := Load("../../docs/workflows/workflow.yaml")
	if err != nil {
		t.Fatal(err)
	}
	plan := w.Pipelines["implement"].Stages[0]
	if plan.Skill.Name != "plan-tasks" || !contains(plan.Skill.Inputs, "contracts_dir") {
		t.Fatalf("plan-tasks inputs = %v", plan.Skill.Inputs)
	}
	for _, want := range []string{"Contract candidate:", "Contract used:", "Last verified:"} {
		if !strings.Contains(plan.Skill.Body, want) {
			t.Errorf("plan-tasks skill missing %q", want)
		}
	}
	plan.Gate = "" // only the prompt matters here
	var prompts []string
	res := Run(context.Background(), Options{
		Route:    Route{Pipeline: &Pipeline{Name: "plan", Stages: []Stage{plan}}, Name: "plan", Kind: "workflow"},
		Task:     "task text",
		Cwd:      t.TempDir(),
		Dir:      t.TempDir(),
		RunAgent: fakeAgent(t, [][2]string{{"Submitted", "Task 1: x\nSUCCESS CHECK: PASS"}}, &prompts),
		Emit:     func(string, map[string]any) {},
	})
	if res.Status != StatusPassed || len(prompts) != 1 {
		t.Fatalf("result = %+v", res)
	}
	if !strings.Contains(prompts[0], "### contracts_dir\n\ndocs/contracts") {
		t.Errorf("plan-tasks prompt lacks the contracts_dir input:\n%s", prompts[0])
	}
}
