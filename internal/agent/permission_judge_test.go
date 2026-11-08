package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/permissions"
	"github.com/vicgupta/tinyagent/internal/routing"
)

// judgeFake is a canned permission-judge model.
type judgeFake struct {
	content string
	err     error
	calls   int
	last    []model.Message
}

func (j *judgeFake) Query(_ context.Context, msgs []model.Message, _ []model.ToolSpec) (model.Message, model.Cost, error) {
	j.calls++
	j.last = append([]model.Message(nil), msgs...)
	if j.err != nil {
		return model.Message{}, model.Cost{}, j.err
	}
	return model.Message{Content: j.content}, model.Cost{USD: 0.001, InputTokens: 20, OutputTokens: 5}, nil
}

func withJudge(t *testing.T, a *Agent, cfg config.Permissions, fake *judgeFake) {
	t.Helper()
	cfg.Judge.Enabled = true
	withPolicy(t, a, cfg)
	a.Helpers = map[string]model.Model{routing.TaskPermission: fake}
}

func TestPermissionJudgeAutoAllow(t *testing.T) {
	main := &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{call("1", "bash", `{"command":"go test ./..."}`)}},
		{Role: "assistant", Content: "ok"},
	}}
	a := newAgent(t, main)
	fake := &judgeFake{content: `{"verdict":"allow","confidence":0.95,"reason":"running tests"}`}
	withJudge(t, a, config.Permissions{Rules: []config.PermissionRule{
		{Tool: "bash", Command: "go test *", Decision: "ask"},
	}}, fake)
	prompted := false
	a.PermissionApprover = func(context.Context, permissions.Request) permissions.Answer {
		prompted = true
		return permissions.Answer{Decision: permissions.DecisionDeny, Channel: "terminal"}
	}
	events := permissionEvents(a)

	tr := a.Run(context.Background(), "task")
	if tr.ExitStatus != StatusCompleted {
		t.Fatalf("status = %s", tr.ExitStatus)
	}
	if prompted {
		t.Fatal("high-confidence allow should not prompt the user")
	}
	if fake.calls != 1 {
		t.Fatalf("judge calls = %d", fake.calls)
	}
	if len(*events) != 1 {
		t.Fatalf("events = %+v", *events)
	}
	ev := (*events)[0]
	if ev["decision"] != "allow" || ev["source"] != "judge" {
		t.Fatalf("event = %+v", ev)
	}
	j, _ := ev["judge"].(map[string]any)
	if j["verdict"] != "allow" || j["reason"] != "running tests" {
		t.Fatalf("judge = %+v", j)
	}
}

func TestPermissionJudgeLowConfidencePrompts(t *testing.T) {
	main := &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{call("1", "bash", `{"command":"echo hi"}`)}},
		{Role: "assistant", Content: "ok"},
	}}
	a := newAgent(t, main)
	fake := &judgeFake{content: `{"verdict":"allow","confidence":0.4,"reason":"unsure"}`}
	withJudge(t, a, config.Permissions{Rules: []config.PermissionRule{
		{Tool: "bash", Command: "echo *", Decision: "ask"},
	}}, fake)
	a.PermissionApprover = func(context.Context, permissions.Request) permissions.Answer {
		return permissions.Answer{Decision: permissions.DecisionAllow, Channel: "terminal"}
	}
	events := permissionEvents(a)

	a.Run(context.Background(), "task")
	if len(*events) != 1 || (*events)[0]["decision"] != "allow" || (*events)[0]["source"] != "user" {
		t.Fatalf("event = %+v", *events)
	}
	j, _ := (*events)[0]["judge"].(map[string]any)
	if j["verdict"] != "ask" {
		t.Fatalf("low confidence should become ask, got %+v", j)
	}
}

func TestPermissionJudgeRiskyPrompts(t *testing.T) {
	main := &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{call("1", "bash", `{"command":"git push origin main"}`)}},
		{Role: "assistant", Content: "ok"},
	}}
	a := newAgent(t, main)
	fake := &judgeFake{content: `{"verdict":"risky","confidence":0.99,"reason":"pushes to remote"}`}
	withJudge(t, a, config.Permissions{Rules: []config.PermissionRule{
		{Tool: "bash", Command: "git push *", Decision: "ask"},
	}}, fake)
	a.PermissionApprover = func(context.Context, permissions.Request) permissions.Answer {
		return permissions.Answer{Decision: permissions.DecisionDeny, Reason: "no", Channel: "terminal"}
	}
	events := permissionEvents(a)

	tr := a.Run(context.Background(), "task")
	res := toolResults(tr.Messages)
	if len(res) != 1 || !strings.Contains(res[0], "denied by user") {
		t.Fatalf("result = %q", res)
	}
	j, _ := (*events)[0]["judge"].(map[string]any)
	if j["verdict"] != "risky" || (*events)[0]["source"] != "user" {
		t.Fatalf("event = %+v judge=%+v", (*events)[0], j)
	}
}

func TestPermissionJudgeModelErrorPrompts(t *testing.T) {
	main := &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{call("1", "bash", `{"command":"echo hi"}`)}},
		{Role: "assistant", Content: "ok"},
	}}
	a := newAgent(t, main)
	fake := &judgeFake{err: errors.New("boom")}
	withJudge(t, a, config.Permissions{Rules: []config.PermissionRule{
		{Tool: "bash", Command: "echo *", Decision: "ask"},
	}}, fake)
	a.PermissionApprover = func(context.Context, permissions.Request) permissions.Answer {
		return permissions.Answer{Decision: permissions.DecisionAllow, Channel: "terminal"}
	}
	events := permissionEvents(a)

	a.Run(context.Background(), "task")
	j, _ := (*events)[0]["judge"].(map[string]any)
	if j["verdict"] != "error" || (*events)[0]["source"] != "user" || (*events)[0]["decision"] != "allow" {
		t.Fatalf("event = %+v judge=%+v", (*events)[0], j)
	}
	// Judge must not fall back to the main model on error.
	if fake.calls != 1 {
		t.Fatalf("judge calls = %d", fake.calls)
	}
}

func TestPermissionJudgeNeverBypassesDeny(t *testing.T) {
	main := &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{call("1", "bash", `{"command":"rm -rf /"}`)}},
		{Role: "assistant", Content: "ok"},
	}}
	a := newAgent(t, main)
	fake := &judgeFake{content: `{"verdict":"allow","confidence":1,"reason":"sure"}`}
	withJudge(t, a, config.Permissions{Rules: []config.PermissionRule{
		{Tool: "bash", Command: "rm *", Decision: "deny", Message: "no rm"},
		{Tool: "bash", Command: "*", Decision: "ask"},
	}}, fake)
	events := permissionEvents(a)

	tr := a.Run(context.Background(), "task")
	res := toolResults(tr.Messages)
	if len(res) != 1 || !strings.Contains(res[0], "denied by permission rule 0") {
		t.Fatalf("result = %q", res)
	}
	if fake.calls != 0 {
		t.Fatal("judge must not run for an explicit deny")
	}
	if (*events)[0]["source"] != "rule" || (*events)[0]["decision"] != "deny" {
		t.Fatalf("event = %+v", (*events)[0])
	}
}

func TestParseJudgeResponse(t *testing.T) {
	op := parseJudgeResponse(`{"verdict":"allow","confidence":0.9,"reason":"ok"}`, 0.85)
	if !op.AutoAllow || op.Verdict != judgeAllow {
		t.Fatalf("allow = %+v", op)
	}
	op = parseJudgeResponse("```json\n{\"verdict\":\"risky\",\"confidence\":0.8,\"reason\":\"x\"}\n```", 0.85)
	if op.AutoAllow || op.Verdict != judgeRisky {
		t.Fatalf("risky = %+v", op)
	}
	op = parseJudgeResponse(`{"verdict":"allow","confidence":0.5,"reason":"maybe"}`, 0.85)
	if op.AutoAllow || op.Verdict != judgeAsk {
		t.Fatalf("low = %+v", op)
	}
	op = parseJudgeResponse("not json", 0.85)
	if op.Verdict != judgeError || op.AutoAllow {
		t.Fatalf("bad = %+v", op)
	}
}
