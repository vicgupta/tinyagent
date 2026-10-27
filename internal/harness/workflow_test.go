package harness

import "testing"

func TestExampleWorkflowLoads(t *testing.T) {
	w, err := Load("../../docs/workflows/workflow.yaml")
	if err != nil {
		t.Fatal(err)
	}
	r, err := w.Route("Implement the spec in SPEC.md", "")
	if err != nil || r.Name != "implement" || len(r.Pipeline.Stages) != 4 {
		t.Fatalf("route = %+v, %v", r, err)
	}
	st := r.Pipeline.Stages
	if st[1].StepLimit != 100 || st[0].StepLimit != 50 || st[3].CheckTimeout != 600 || st[0].CheckTimeout != DefaultCheckTimeout {
		t.Errorf("settings = %+v", st)
	}
}
