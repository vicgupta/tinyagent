package agent

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vicgupta/tinyagent/internal/model"
)

// loopReplies returns n responses that each call list_files.
func loopReplies(n int) []model.Message {
	out := make([]model.Message, n)
	for i := range out {
		out[i] = assistantCalls(call("c", "list_files", `{}`))
	}
	return out
}

func budgetNotices(msgs []model.Message) int {
	n := 0
	for _, m := range msgs {
		if m.Role == "user" && strings.HasPrefix(m.Content, "Budget notice:") {
			n++
		}
	}
	return n
}

func TestTurnCapStopsAtN(t *testing.T) {
	m := &scripted{replies: loopReplies(10)}
	a := newAgent(t, m)
	a.Config.StepLimit = 3
	tr := a.Run(context.Background(), "x")
	if tr.ExitStatus != StatusStepLimit || tr.Steps != 3 || len(m.seen) != 3 {
		t.Errorf("status %s, steps %d, requests %d", tr.ExitStatus, tr.Steps, len(m.seen))
	}
}

func TestTokenLimitStopsRun(t *testing.T) {
	m := &scripted{replies: loopReplies(10)} // 120 tokens per response
	a := newAgent(t, m)
	a.Config.TokenLimit = 300
	tr := a.Run(context.Background(), "x")
	if tr.ExitStatus != StatusTokenLimit || tr.Steps != 3 || len(m.seen) != 3 {
		t.Errorf("status %s, steps %d, requests %d", tr.ExitStatus, tr.Steps, len(m.seen))
	}
	if got := tr.InputTokens + tr.OutputTokens; got != 360 {
		t.Errorf("tokens = %d, want 360", got)
	}
}

func TestTokenBudgetWarnsOnceAt80Percent(t *testing.T) {
	m := &scripted{replies: loopReplies(10)}
	a := newAgent(t, m)
	a.Config.TokenLimit = 600 // warn at 480 tokens, after the 4th response
	var warnings int
	a.OnEvent = func(kind string, _ map[string]any) {
		if kind == "budget.warning" {
			warnings++
		}
	}
	tr := a.Run(context.Background(), "x")
	if tr.ExitStatus != StatusTokenLimit || tr.Steps != 5 {
		t.Fatalf("status %s, steps %d", tr.ExitStatus, tr.Steps)
	}
	if n := budgetNotices(tr.Messages); n != 1 || warnings != 1 || !tr.BudgetWarned {
		t.Errorf("notices %d, events %d, warned %v; want one", n, warnings, tr.BudgetWarned)
	}
	if budgetNotices(m.seen[3]) != 0 {
		t.Error("warned before reaching 80%")
	}
	last := m.seen[4][len(m.seen[4])-1]
	if last.Role != "user" || !strings.Contains(last.Content, "80% of its token budget (480 of 600 tokens)") {
		t.Errorf("5th request ends with %+v", last)
	}
}

func TestCostBudgetWarnsOnce(t *testing.T) {
	m := &scripted{replies: loopReplies(10)} // $0.01 per response
	a := newAgent(t, m)
	a.Config.CostLimit = 0.045
	tr := a.Run(context.Background(), "x")
	if tr.ExitStatus != StatusCostLimit || tr.Steps != 5 {
		t.Fatalf("status %s, steps %d", tr.ExitStatus, tr.Steps)
	}
	if n := budgetNotices(tr.Messages); n != 1 || !strings.Contains(m.seen[4][len(m.seen[4])-1].Content, "cost budget") {
		t.Errorf("notices %d", n)
	}
}

func TestNoBudgetNoWarning(t *testing.T) {
	m := &scripted{replies: loopReplies(5)}
	a := newAgent(t, m)
	a.Config.StepLimit = 5
	tr := a.Run(context.Background(), "x")
	if budgetNotices(tr.Messages) != 0 || tr.BudgetWarned {
		t.Error("warned without a token or cost budget")
	}
}

func TestBudgetWarningRearmsWhenLimitRaised(t *testing.T) {
	m := &scripted{replies: loopReplies(10)}
	a := newAgent(t, m)
	a.Config.TokenLimit = 600
	tr := a.Run(context.Background(), "x") // warned, then stopped at 600
	a.Config.TokenLimit = 6000
	a.warnBudget(tr, viewFromTrajectory(tr))
	if tr.BudgetWarned {
		t.Error("raised limit did not re-arm the warning")
	}
}

// unpricedModel reports tokens but no cost, like a model with no price.
type unpricedModel struct{ scripted }

func (u *unpricedModel) Query(ctx context.Context, msgs []model.Message, specs []model.ToolSpec) (model.Message, model.Cost, error) {
	msg, c, err := u.scripted.Query(ctx, msgs, specs)
	c.USD = 0
	return msg, c, err
}

func TestUnpricedModelFallsBackToTokenBudget(t *testing.T) {
	m := &unpricedModel{scripted{replies: loopReplies(10)}}
	a := newAgent(t, m)
	a.Config.CostLimit = 0.01 // can't be enforced without a price
	a.Config.TokenLimit = 300
	tr := a.Run(context.Background(), "x")
	if tr.ExitStatus != StatusTokenLimit || tr.Cost != 0 || tr.Steps != 3 {
		t.Errorf("status %s, cost %v, steps %d", tr.ExitStatus, tr.Cost, tr.Steps)
	}
	for _, msg := range tr.Messages {
		if strings.Contains(msg.Content, "cost budget") {
			t.Error("cost warning for an unpriced model")
		}
	}
}

func TestDelegateTokensCountTowardParentLimit(t *testing.T) {
	a, pm, cm := subagentAgent(t,
		[]model.Message{assistantCalls(delegateCall("p1", "spend")), {Role: "assistant", Content: "done"}},
		loopReplies(4))
	a.Config.TokenLimit = 300
	tr := a.Run(context.Background(), "x")
	if tr.ExitStatus != StatusTokenLimit {
		t.Fatalf("status = %s", tr.ExitStatus)
	}
	// Parent 120; the child gets the 180 left: 120, then 240 >= 180.
	if len(cm.seen) != 2 || len(pm.seen) != 1 || tr.Subagents[0].Status != StatusTokenLimit {
		t.Errorf("child requests %d, parent requests %d, record %+v", len(cm.seen), len(pm.seen), tr.Subagents[0])
	}
	if got := tr.InputTokens + tr.OutputTokens; got != 360 {
		t.Errorf("tokens = %d, want 360", got)
	}
}

func TestDelegateNotStartedWhenTokensSpent(t *testing.T) {
	a, _, cm := subagentAgent(t,
		[]model.Message{assistantCalls(delegateCall("p1", "spend")), {Role: "assistant", Content: "done"}}, nil)
	a.Config.TokenLimit = 120
	tr := a.Run(context.Background(), "x")
	if tr.ExitStatus != StatusTokenLimit || len(cm.seen) != 0 {
		t.Errorf("status %s, child requests %d", tr.ExitStatus, len(cm.seen))
	}
}

func TestWorkersSharedTokenLimitCancelsAll(t *testing.T) {
	loop := assistantCalls(call("c", "write_file", `{"path":"spin.txt","content":"x\n"}`))
	tm := &taskModel{scripts: map[string][]model.Message{"task-spend": {loop}, "task-wait": {loop}}}
	tm.before = func(ctx context.Context, key string) error {
		if key == "task-wait" {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(20 * time.Second):
				return errors.New("never cancelled")
			}
		}
		time.Sleep(time.Millisecond)
		return nil
	}
	a := batchAgent(t, []model.Message{
		assistantCalls(workerCallMsg("p1", "task-spend"), workerCallMsg("p2", "task-wait")),
		{Role: "assistant", Content: "done" + submitMarker},
	}, tm, 2)
	a.Config.TokenLimit = 200 // the parent's first call uses 120, each worker call 15
	a.Workers.MaxSteps = 1000
	start := time.Now()
	tr := a.Run(context.Background(), "go")
	if time.Since(start) > 15*time.Second {
		t.Fatal("workers were not cancelled")
	}
	if tr.ExitStatus != StatusTokenLimit {
		t.Fatalf("exit = %s %s", tr.ExitStatus, tr.Submission)
	}
	for _, s := range tr.Subagents {
		if s.Status != StatusTokenLimit {
			t.Errorf("worker %s status = %s", s.ID, s.Status)
		}
	}
	if got := tr.InputTokens + tr.OutputTokens; got < 200 {
		t.Errorf("parent tokens = %d; worker usage not counted", got)
	}
}
