package agent

import (
	"context"
	"testing"

	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

func TestUsageByRole(t *testing.T) {
	a, _, _ := subagentAgent(t,
		[]model.Message{
			assistantCalls(delegateCall("p1", "Find the schema file.")),
			{Role: "assistant", Content: "fixed" + submitMarker},
		},
		[]model.Message{{Role: "assistant", Content: "found" + submitMarker}})
	rm := &specModel{scripted: scripted{replies: []model.Message{{Role: "assistant", Content: "routed" + submitMarker}}}}
	a.Subagents.Model = rm
	a.Subagents.Routes = []ChildRoute{{Match: []string{"schema"}, Model: rm, ModelName: "routed-model"}}
	var routes []map[string]any
	var requests []map[string]any
	a.OnEvent = func(kind string, f map[string]any) {
		switch kind {
		case "model.route":
			routes = append(routes, f)
		case "model.request":
			requests = append(requests, f)
		}
	}
	tr := a.Run(context.Background(), "fix the bug")
	if !tr.RoutedUsage() || len(tr.UsageByRole) != 2 {
		t.Fatalf("usage_by_role = %+v", tr.UsageByRole)
	}
	m, d := tr.UsageByRole[0], tr.UsageByRole[1]
	if m.Role != "main" || m.Task != "main" || m.Requests != 2 || d.Role != "delegated" || d.Task != "delegate" || d.Model != "routed-model" || d.Requests != 1 {
		t.Errorf("usage_by_role = %+v", tr.UsageByRole)
	}
	if m.InputTokens+d.InputTokens != tr.InputTokens || m.OutputTokens+d.OutputTokens != tr.OutputTokens {
		t.Errorf("split %+v does not add up to %d/%d", tr.UsageByRole, tr.InputTokens, tr.OutputTokens)
	}
	if len(routes) != 1 || routes[0]["model"] != "routed-model" || routes[0]["reason"] != `match "schema"` || routes[0]["task"] != "delegate" {
		t.Errorf("model.route = %v", routes)
	}
	if len(requests) == 0 || requests[0]["role"] != "main" || requests[0]["task"] != "main" {
		t.Errorf("model.request = %v", requests)
	}
}

func TestUsageByRoleMainOnly(t *testing.T) {
	a := newAgent(t, &specModel{scripted: scripted{replies: []model.Message{{Role: "assistant", Content: "ok" + submitMarker}}}})
	tr := a.Run(context.Background(), "x")
	if tr.RoutedUsage() {
		t.Errorf("main-only run reports routed usage: %+v", tr.UsageByRole)
	}
}

func TestAddUsageMerges(t *testing.T) {
	var tr trajectory.Trajectory
	tr.AddUsage(trajectory.RoleUsage{Role: "auxiliary", Task: "review", Model: "m", Requests: 1, InputTokens: 5, CostUSD: 0.5})
	tr.AddUsage(trajectory.RoleUsage{Role: "auxiliary", Task: "review", Model: "m", Requests: 1, InputTokens: 7, CostUSD: 0.25})
	if len(tr.UsageByRole) != 1 || tr.UsageByRole[0].Requests != 2 || tr.UsageByRole[0].InputTokens != 12 || tr.UsageByRole[0].CostUSD != 0.75 {
		t.Errorf("merged = %+v", tr.UsageByRole)
	}
}
