package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/routing"
)

type helperFake struct {
	name  string
	err   error
	calls int
}

func (h *helperFake) Query(context.Context, []model.Message, []model.ToolSpec) (model.Message, model.Cost, error) {
	h.calls++
	return model.Message{Content: h.name}, model.Cost{USD: 0.01, InputTokens: 10, OutputTokens: 2}, h.err
}

func TestHelperQueryRoutes(t *testing.T) {
	main, aux := &helperFake{name: "main"}, &helperFake{name: "aux"}
	a := &Agent{Model: main, Helpers: map[string]model.Model{routing.TaskCompaction: aux}}
	resp, cost, err := a.helperQuery(context.Background(), routing.TaskCompaction, nil)
	if err != nil || resp.Content != "aux" || cost.InputTokens != 10 || main.calls != 0 {
		t.Errorf("routed: %q %+v %v, main calls %d", resp.Content, cost, err, main.calls)
	}
	resp, _, err = a.helperQuery(context.Background(), routing.TaskReview, nil)
	if err != nil || resp.Content != "main" {
		t.Errorf("unrouted task: %q %v", resp.Content, err)
	}
}

func TestHelperQueryFallsBackToMain(t *testing.T) {
	main, aux := &helperFake{name: "main"}, &helperFake{name: "aux", err: errors.New("boom")}
	a := &Agent{Model: main, Helpers: map[string]model.Model{routing.TaskReview: aux}}
	resp, cost, err := a.helperQuery(context.Background(), routing.TaskReview, nil)
	if err != nil || resp.Content != "main" || aux.calls != 1 || main.calls != 1 {
		t.Errorf("fallback: %q %v aux=%d main=%d", resp.Content, err, aux.calls, main.calls)
	}
	if cost.InputTokens != 20 || cost.USD < 0.019 {
		t.Errorf("cost should cover both attempts: %+v", cost)
	}
}
