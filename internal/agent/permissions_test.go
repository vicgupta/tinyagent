package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/hooks/hookstest"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/permissions"
)

func withPolicy(t *testing.T, a *Agent, cfg config.Permissions) {
	t.Helper()
	p, err := permissions.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	a.Policy = p
}

// permissionEvents returns a pointer to the permission events a run emits.
// The pointer targets the same slice variable the OnEvent closure appends to,
// so it reflects events emitted during Run.
func permissionEvents(a *Agent) *[]map[string]any {
	var events []map[string]any
	a.OnEvent = func(kind string, fields map[string]any) {
		if kind == "permission" {
			events = append(events, fields)
		}
	}
	return &events
}

func TestPermissionDeny(t *testing.T) {
	m := &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{call("1", "bash", `{"command":"touch ran"}`)}},
		{Role: "assistant", Content: "ok"},
	}}
	a := newAgent(t, m)
	withPolicy(t, a, config.Permissions{Rules: []config.PermissionRule{
		{Tool: "bash", Command: "touch *", Decision: "deny", Message: "no writes"},
	}})
	events := permissionEvents(a)

	tr := a.Run(context.Background(), "task")
	if tr.ExitStatus != StatusCompleted {
		t.Fatalf("trajectory = %+v", tr)
	}
	if _, err := os.Stat(filepath.Join(a.Tools.Root, "ran")); err == nil {
		t.Error("denied command ran")
	}
	res := toolResults(tr.Messages)
	if len(res) != 1 || res[0] != "error: denied by permission rule 0: no writes" {
		t.Errorf("results = %q", res)
	}
	if len(*events) != 1 || (*events)[0]["decision"] != "deny" || (*events)[0]["source"] != "rule" || (*events)[0]["rule"] != 0 || (*events)[0]["tool_name"] != "bash" {
		t.Errorf("permission events = %v", *events)
	}
}

func TestPermissionPrecedenceDenyOverAllow(t *testing.T) {
	m := &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{call("1", "bash", `{"command":"git push origin main"}`)}},
		{Role: "assistant", Content: "ok"},
	}}
	a := newAgent(t, m)
	withPolicy(t, a, config.Permissions{Rules: []config.PermissionRule{
		{Tool: "bash", Command: "git *", Decision: "allow"},
		{Tool: "bash", Command: "git push *", Decision: "deny", Message: "no push"},
	}})
	tr := a.Run(context.Background(), "task")
	res := toolResults(tr.Messages)
	if len(res) != 1 || res[0] != "error: denied by permission rule 1: no push" {
		t.Errorf("results = %q", res)
	}
}

func TestPermissionAskHeadlessDeny(t *testing.T) {
	m := &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{call("1", "bash", `{"command":"git push origin main"}`)}},
		{Role: "assistant", Content: "ok"},
	}}
	a := newAgent(t, m)
	withPolicy(t, a, config.Permissions{Rules: []config.PermissionRule{
		{Tool: "bash", Command: "git push *", Decision: "ask"},
	}})
	events := permissionEvents(a)

	tr := a.Run(context.Background(), "task")
	res := toolResults(tr.Messages)
	if len(res) != 1 || res[0] != "error: denied: approval required by permission rule 0 but no one can approve in this run" {
		t.Errorf("results = %q", res)
	}
	if len(*events) != 1 || (*events)[0]["decision"] != "deny" || (*events)[0]["source"] != "non_interactive" || (*events)[0]["channel"] != "none" {
		t.Errorf("permission events = %v", *events)
	}
}

func TestPermissionAskHeadlessAllow(t *testing.T) {
	m := &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{call("1", "bash", `{"command":"echo ok"}`)}},
		{Role: "assistant", Content: "ok"},
	}}
	a := newAgent(t, m)
	withPolicy(t, a, config.Permissions{NonInteractive: "allow", Rules: []config.PermissionRule{
		{Tool: "bash", Command: "echo *", Decision: "ask"},
	}})
	events := permissionEvents(a)

	tr := a.Run(context.Background(), "task")
	res := toolResults(tr.Messages)
	if len(res) != 1 || !strings.Contains(res[0], "ok") {
		t.Errorf("results = %q, want the command to have run", res)
	}
	if len(*events) != 1 || (*events)[0]["decision"] != "allow" || (*events)[0]["source"] != "non_interactive" {
		t.Errorf("permission events = %v", *events)
	}
}

func TestPermissionAskApproverAllowOnce(t *testing.T) {
	m := &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{call("1", "bash", `{"command":"echo hi"}`)}},
		{Role: "assistant", ToolCalls: []model.ToolCall{call("2", "bash", `{"command":"echo hi"}`)}},
		{Role: "assistant", Content: "ok"},
	}}
	a := newAgent(t, m)
	withPolicy(t, a, config.Permissions{Rules: []config.PermissionRule{
		{Tool: "bash", Command: "echo *", Decision: "ask"},
	}})
	calls := 0
	a.PermissionApprover = func(_ context.Context, req permissions.Request) permissions.Answer {
		calls++
		return permissions.Answer{Decision: permissions.DecisionAllow, Channel: "terminal"}
	}
	tr := a.Run(context.Background(), "task")
	if tr.ExitStatus != StatusCompleted {
		t.Fatalf("trajectory = %+v", tr)
	}
	if calls != 2 {
		t.Errorf("approver called %d times, want 2 (once per identical call)", calls)
	}
	res := toolResults(tr.Messages)
	if len(res) != 2 || strings.HasPrefix(res[0], "error:") || strings.HasPrefix(res[1], "error:") {
		t.Errorf("results = %q", res)
	}
}

func TestPermissionAskApproverAllowAlways(t *testing.T) {
	m := &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{call("1", "bash", `{"command":"echo hi"}`)}},
		{Role: "assistant", ToolCalls: []model.ToolCall{call("2", "bash", `{"command":"echo hi"}`)}},
		{Role: "assistant", ToolCalls: []model.ToolCall{call("3", "bash", `{"command":"echo other"}`)}},
		{Role: "assistant", Content: "ok"},
	}}
	a := newAgent(t, m)
	withPolicy(t, a, config.Permissions{Rules: []config.PermissionRule{
		{Tool: "bash", Command: "echo *", Decision: "ask"},
	}})
	calls := 0
	a.PermissionApprover = func(_ context.Context, req permissions.Request) permissions.Answer {
		calls++
		return permissions.Answer{Decision: permissions.DecisionAllow, Always: true, Channel: "terminal"}
	}
	a.Run(context.Background(), "task")
	// The first call is allowed-always; the identical second call skips the
	// prompt, but a different command asks again.
	if calls != 2 {
		t.Errorf("approver called %d times, want 2 (once for echo hi, once for echo other)", calls)
	}
}

func TestPermissionAskApproverDenyReason(t *testing.T) {
	m := &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{call("1", "bash", `{"command":"git push origin main"}`)}},
		{Role: "assistant", Content: "ok"},
	}}
	a := newAgent(t, m)
	withPolicy(t, a, config.Permissions{Rules: []config.PermissionRule{
		{Tool: "bash", Command: "git push *", Decision: "ask"},
	}})
	a.PermissionApprover = func(_ context.Context, req permissions.Request) permissions.Answer {
		return permissions.Answer{Decision: permissions.DecisionDeny, Reason: "not today", Channel: "terminal"}
	}
	tr := a.Run(context.Background(), "task")
	res := toolResults(tr.Messages)
	if len(res) != 1 || res[0] != "error: denied by user: not today" {
		t.Errorf("results = %q", res)
	}
}

func TestPermissionAllowRunsHooks(t *testing.T) {
	m := &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{call("1", "bash", `{"command":"echo ok"}`)}},
		{Role: "assistant", Content: "ok"},
	}}
	a := newAgent(t, m)
	withPolicy(t, a, config.Permissions{Rules: []config.PermissionRule{
		{Tool: "bash", Command: "echo *", Decision: "allow"},
	}})
	log := filepath.Join(t.TempDir(), "log")
	withHooks(t, a, config.Hooks{PreTool: []config.Hook{hookstest.Hook(t, "bash", "log", log, "pre")}})
	tr := a.Run(context.Background(), "task")
	res := toolResults(tr.Messages)
	if len(res) != 1 || strings.HasPrefix(res[0], "error:") {
		t.Fatalf("results = %q", res)
	}
	if b, _ := os.ReadFile(log); string(b) != "pre\n" {
		t.Errorf("hook log = %q, want the allowed call to run hooks", b)
	}
}

func TestPermissionDenySkipsHooks(t *testing.T) {
	m := &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{call("1", "bash", `{"command":"touch ran"}`)}},
		{Role: "assistant", Content: "ok"},
	}}
	a := newAgent(t, m)
	withPolicy(t, a, config.Permissions{Rules: []config.PermissionRule{
		{Tool: "bash", Command: "touch *", Decision: "deny"},
	}})
	log := filepath.Join(t.TempDir(), "log")
	withHooks(t, a, config.Hooks{PreTool: []config.Hook{hookstest.Hook(t, "bash", "log", log, "pre")}})
	a.Run(context.Background(), "task")
	if b, _ := os.ReadFile(log); string(b) != "" {
		t.Errorf("hook log = %q, want no hook for a policy-denied call", b)
	}
}

func TestPermissionDefaultDeny(t *testing.T) {
	m := &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{call("1", "bash", `{"command":"echo ok"}`)}},
		{Role: "assistant", Content: "ok"},
	}}
	a := newAgent(t, m)
	withPolicy(t, a, config.Permissions{Default: "deny"})
	tr := a.Run(context.Background(), "task")
	res := toolResults(tr.Messages)
	if len(res) != 1 || res[0] != "error: denied by permission policy" {
		t.Errorf("results = %q", res)
	}
}

func TestPermissionExemptsPlanAndAskUser(t *testing.T) {
	// update_plan is handled by the loop, never the policy.
	m := &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{call("1", "update_plan", `{"items":[{"id":"a","text":"do it","status":"done"}]}`)}},
		{Role: "assistant", Content: "ok"},
	}}
	a := newAgent(t, m)
	a.Tools.Plan = true
	withPolicy(t, a, config.Permissions{Rules: []config.PermissionRule{{Tool: "*", Decision: "deny"}}})
	tr := a.Run(context.Background(), "task")
	res := toolResults(tr.Messages)
	if len(res) != 1 || strings.HasPrefix(res[0], "error: denied") {
		t.Fatalf("update_plan was denied: %q", res)
	}

	// ask_user ends the run NeedsInput, never denied.
	m = &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{call("q", "ask_user", askArgs)}},
	}}
	a = newAgent(t, m)
	a.Tools.AskUser = true
	withPolicy(t, a, config.Permissions{Rules: []config.PermissionRule{{Tool: "*", Decision: "deny"}}})
	tr = a.Run(context.Background(), "task")
	if tr.ExitStatus != StatusNeedsInput {
		t.Fatalf("ask_user status = %s (%q), want NeedsInput", tr.ExitStatus, tr.Submission)
	}
}

func TestPermissionPathDenyWriteFile(t *testing.T) {
	m := &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{call("1", "write_file", `{"path":"migrations/001.sql","content":"x"}`)}},
		{Role: "assistant", Content: "ok"},
	}}
	a := newAgent(t, m)
	withPolicy(t, a, config.Permissions{Rules: []config.PermissionRule{
		{Tool: "write_file|edit_file|apply_patch", Path: "migrations/**", Decision: "deny", Message: "no migrations"},
	}})
	tr := a.Run(context.Background(), "task")
	if _, err := os.Stat(filepath.Join(a.Tools.Root, "migrations", "001.sql")); err == nil {
		t.Error("denied write_file wrote the file")
	}
	res := toolResults(tr.Messages)
	if len(res) != 1 || res[0] != "error: denied by permission rule 0: no migrations" {
		t.Errorf("results = %q", res)
	}
}
