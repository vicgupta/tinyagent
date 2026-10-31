package tools

import "testing"

func TestWithTaskType(t *testing.T) {
	base := DelegateSpec()
	if got := WithTaskType(base, nil); len(got.Function.Parameters["properties"].(map[string]any)) != len(base.Function.Parameters["properties"].(map[string]any)) {
		t.Fatal("no types should leave the schema alone")
	}
	got := WithTaskType(base, []string{"docs"})
	p := got.Function.Parameters["properties"].(map[string]any)
	if tt, ok := p["task_type"].(map[string]any); !ok || tt["enum"].([]string)[0] != "docs" {
		t.Errorf("task_type = %v", p["task_type"])
	}
	if _, ok := base.Function.Parameters["properties"].(map[string]any)["task_type"]; ok {
		t.Error("WithTaskType mutated the base spec")
	}
}
