package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/model"
)

// checkAgent returns a verify agent whose computational checks are checks,
// and a pointer to the verify events it emits with their fields.
func checkAgent(t *testing.T, m model.Model, checks ...string) (*Agent, *[]string, *[]map[string]any) {
	t.Helper()
	a := newAgent(t, m)
	a.Config.VerifyOnStop = config.VerifyOnStop{Enabled: true, Checks: checks, MaxReprompts: 2}
	var kinds []string
	var fields []map[string]any
	a.OnEvent = func(kind string, f map[string]any) {
		if strings.HasPrefix(kind, "verify.") {
			kinds = append(kinds, kind)
			fields = append(fields, f)
		}
	}
	return a, &kinds, &fields
}

func TestVerifyChecksAllPassFinishVerified(t *testing.T) {
	m := &scripted{replies: []model.Message{
		edit(),
		{Role: "assistant", Content: "Done."},
	}}
	a, kinds, fields := checkAgent(t, m, "true lint", "true test")
	tr := a.Run(context.Background(), "task")
	if tr.ExitStatus != StatusCompleted || tr.Submission != "Done." || tr.Unverified || tr.VerifyPending || tr.VerifyReprompts != 0 {
		t.Fatalf("trajectory = %+v", tr)
	}
	if strings.Join(*kinds, ",") != "verify.check,verify.check" {
		t.Fatalf("events = %v", *kinds)
	}
	if (*fields)[0]["command"] != "true lint" || (*fields)[1]["command"] != "true test" || (*fields)[1]["passed"] != true {
		t.Errorf("checks ran out of order or failed: %v", *fields)
	}
	if verifyNotices(tr.Messages) != 0 {
		t.Error("passing checks still re-prompted the model")
	}
}

func TestVerifyChecksStopAtFirstFailure(t *testing.T) {
	m := &scripted{replies: []model.Message{
		edit(),
		{Role: "assistant", Content: "Done."},
		{Role: "assistant", Content: "Fixed."},
		{Role: "assistant", Content: "Still done."},
	}}
	a, kinds, fields := checkAgent(t, m, "true lint", "echo lint-broke; false", "true test")
	tr := a.Run(context.Background(), "task")
	if tr.ExitStatus != StatusCompleted || !tr.Unverified || tr.VerifyReprompts != 2 {
		t.Fatalf("trajectory = %+v", tr)
	}
	// First stop: check 1 passes, check 2 fails, check 3 never runs.
	if (*fields)[0]["passed"] != true || (*fields)[1]["passed"] != false || (*kinds)[2] != "verify.reprompt" {
		t.Errorf("events = %v %v", *kinds, *fields)
	}
	notice := m.seen[2][len(m.seen[2])-1].Content
	if !strings.Contains(notice, "check 2 of 3 failed") || !strings.Contains(notice, "lint-broke") {
		t.Errorf("notice does not show the failing check: %q", notice)
	}
	for _, f := range *fields {
		if f["command"] == "true test" {
			t.Error("a check after the failing one ran")
		}
	}
}

func TestVerifyChecksFixedAfterReprompt(t *testing.T) {
	m := &scripted{replies: []model.Message{
		edit(),
		{Role: "assistant", Content: "Done."},
		{Role: "assistant", ToolCalls: []model.ToolCall{call("w2", "write_file", `{"path":"fixed","content":"ok"}`)}},
		{Role: "assistant", Content: "Fixed."},
	}}
	a, _, _ := checkAgent(t, m, "test -f fixed")
	tr := a.Run(context.Background(), "task")
	if tr.ExitStatus != StatusCompleted || tr.Submission != "Fixed." || tr.Unverified || tr.VerifyReprompts != 1 {
		t.Fatalf("trajectory = %+v", tr)
	}
}

func TestVerifyChecksModelRunCounts(t *testing.T) {
	m := &scripted{replies: []model.Message{
		edit(),
		bashMsg("v", "true lint"),
		{Role: "assistant", Content: "Done."},
	}}
	a, kinds, _ := checkAgent(t, m, "true lint")
	tr := a.Run(context.Background(), "task")
	if tr.Unverified || tr.VerifyPending || len(*kinds) != 0 {
		t.Fatalf("a passing check run by the model should verify: %+v, events %v", tr, *kinds)
	}
}

func TestVerifyChecksRefused(t *testing.T) {
	m := &scripted{replies: []model.Message{
		edit(),
		{Role: "assistant", Content: "Done."},
	}}
	a, kinds, _ := checkAgent(t, m, "true lint")
	withPolicy(t, a, config.Permissions{Rules: []config.PermissionRule{
		{Tool: "bash", Command: "true lint", Decision: "deny"},
	}})
	tr := a.Run(context.Background(), "task")
	if tr.ExitStatus != StatusCompleted || !tr.Unverified || tr.VerifyReprompts != 0 {
		t.Fatalf("trajectory = %+v", tr)
	}
	if strings.Join(*kinds, ",") != "verify.check,verify.unverified" {
		t.Errorf("events = %v", *kinds)
	}
}

func TestVerifyChecksOffByDefault(t *testing.T) {
	m := &scripted{replies: []model.Message{
		edit(),
		{Role: "assistant", Content: "Done."},
	}}
	a, kinds, _ := checkAgent(t, m, "false")
	a.Config.VerifyOnStop.Enabled = false
	tr := a.Run(context.Background(), "task")
	if tr.ExitStatus != StatusCompleted || tr.Unverified || len(*kinds) != 0 {
		t.Fatalf("checks ran with the guard off: %+v, %v", tr, *kinds)
	}
}

func TestIsVerifyCommandChecks(t *testing.T) {
	c := config.VerifyOnStop{Checks: []string{"golangci-lint run"}}
	if !isVerifyCommand(c, "golangci-lint run ./...") {
		t.Error("a check should count as verification")
	}
	if isVerifyCommand(c, "go test ./...") {
		t.Error("checks should replace the default patterns")
	}
	if h := verifyHint(c); !strings.Contains(h, "`golangci-lint run`") {
		t.Errorf("hint = %q", h)
	}
}
