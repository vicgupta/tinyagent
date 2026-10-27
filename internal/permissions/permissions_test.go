package permissions

import (
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/config"
)

func policy(t *testing.T, cfg config.Permissions) *Policy {
	t.Helper()
	p, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

func TestNewNilWhenUnconfigured(t *testing.T) {
	if p, err := New(config.Permissions{}); p != nil || err != nil {
		t.Fatalf("New({}) = %v, %v; want nil, nil", p, err)
	}
}

func TestCheckNilPolicyAllows(t *testing.T) {
	var p *Policy
	if r := p.Check(Call{Tool: "bash", Command: "rm -rf /"}); r.Decision != DecisionAllow {
		t.Fatalf("nil policy decision = %q, want allow", r.Decision)
	}
}

func TestDefaultDecision(t *testing.T) {
	p := policy(t, config.Permissions{Default: "deny"})
	if r := p.Check(Call{Tool: "bash", Command: "echo hi"}); r.Decision != DecisionDeny || r.Source != SourceDefault || r.Rule != -1 {
		t.Fatalf("default deny = %+v", r)
	}
}

func TestDenyRule(t *testing.T) {
	p := policy(t, config.Permissions{Rules: []config.PermissionRule{
		{Tool: "bash", Command: "curl *", Decision: "deny", Message: "use web_fetch"},
	}})
	r := p.Check(Call{Tool: "bash", Command: "curl https://x"})
	if r.Decision != DecisionDeny || r.Source != SourceRule || r.Rule != 0 || r.Message != "use web_fetch" {
		t.Fatalf("deny = %+v", r)
	}
	// A non-matching command is allowed by default.
	if r := p.Check(Call{Tool: "bash", Command: "git status"}); r.Decision != DecisionAllow {
		t.Fatalf("non-matching = %+v", r)
	}
}

func TestPrecedence(t *testing.T) {
	// deny > ask > allow, regardless of order.
	p := policy(t, config.Permissions{Rules: []config.PermissionRule{
		{Tool: "bash", Command: "git *", Decision: "allow"},
		{Tool: "bash", Command: "git push *", Decision: "deny"},
	}})
	if r := p.Check(Call{Tool: "bash", Command: "git push origin main"}); r.Decision != DecisionDeny {
		t.Fatalf("deny precedence = %+v", r)
	}
	if r := p.Check(Call{Tool: "bash", Command: "git status"}); r.Decision != DecisionAllow {
		t.Fatalf("allow = %+v", r)
	}

	p = policy(t, config.Permissions{Rules: []config.PermissionRule{
		{Tool: "bash", Command: "git push *", Decision: "ask"},
		{Tool: "bash", Command: "git *", Decision: "allow"},
	}})
	if r := p.Check(Call{Tool: "bash", Command: "git push origin main"}); r.Decision != DecisionAsk {
		t.Fatalf("ask precedence = %+v", r)
	}
}

func TestCompoundCommands(t *testing.T) {
	// allow "go test *" only, default ask: a compound with an uncovered
	// segment requires approval.
	p := policy(t, config.Permissions{
		Default: "ask",
		Rules:   []config.PermissionRule{{Tool: "bash", Command: "go test *", Decision: "allow"}},
	})
	if r := p.Check(Call{Tool: "bash", Command: "go test ./... && rm -rf build"}); r.Decision != DecisionAsk {
		t.Fatalf("compound partially allowed = %+v, want ask", r)
	}
	// Every segment covered by an allow rule allows it.
	if r := p.Check(Call{Tool: "bash", Command: "go test ./..."}); r.Decision != DecisionAllow {
		t.Fatalf("single segment allowed = %+v", r)
	}

	// deny "rm *" applies to any segment.
	p = policy(t, config.Permissions{Rules: []config.PermissionRule{
		{Tool: "bash", Command: "rm *", Decision: "deny"},
	}})
	if r := p.Check(Call{Tool: "bash", Command: "go test ./... && rm -rf build"}); r.Decision != DecisionDeny {
		t.Fatalf("compound denied = %+v", r)
	}

	// Command substitution never matches an allow rule.
	p = policy(t, config.Permissions{Rules: []config.PermissionRule{
		{Tool: "bash", Command: "echo *", Decision: "allow"},
	}})
	if r := p.Check(Call{Tool: "bash", Command: "echo $(rm x)"}); r.Decision != DecisionAllow {
		t.Fatalf("substitution matched allow = %+v", r)
	}
	// eval never matches an allow rule either.
	if r := p.Check(Call{Tool: "bash", Command: "eval rm x"}); r.Decision != DecisionAllow {
		t.Fatalf("eval matched allow = %+v", r)
	}
}

func TestPathRule(t *testing.T) {
	p := policy(t, config.Permissions{Rules: []config.PermissionRule{
		{Tool: "write_file|edit_file|apply_patch", Path: "migrations/**", Decision: "deny"},
	}})
	for _, tool := range []string{"write_file", "edit_file", "apply_patch"} {
		var paths []string
		if tool == "apply_patch" {
			paths = []string{"src/a.go", "migrations/001.sql"}
		} else {
			paths = []string{"migrations/001.sql"}
		}
		if r := p.Check(Call{Tool: tool, Paths: paths}); r.Decision != DecisionDeny {
			t.Fatalf("%s denied = %+v", tool, r)
		}
	}
	if r := p.Check(Call{Tool: "write_file", Paths: []string{"src/a.go"}}); r.Decision != DecisionAllow {
		t.Fatalf("write elsewhere = %+v", r)
	}
}

func TestHostAndToolGlob(t *testing.T) {
	p := policy(t, config.Permissions{Rules: []config.PermissionRule{
		{Tool: "web_fetch", Host: "*.internal.example.com", Decision: "ask"},
		{Tool: "srv__*", Decision: "ask"},
	}})
	if r := p.Check(Call{Tool: "web_fetch", Host: "api.internal.example.com"}); r.Decision != DecisionAsk {
		t.Fatalf("host ask = %+v", r)
	}
	if r := p.Check(Call{Tool: "srv__create_issue"}); r.Decision != DecisionAsk {
		t.Fatalf("mcp glob ask = %+v", r)
	}
	if r := p.Check(Call{Tool: "web_fetch", Host: "example.com"}); r.Decision != DecisionAllow {
		t.Fatalf("other host = %+v", r)
	}
}

func TestToolOnlyRule(t *testing.T) {
	p := policy(t, config.Permissions{Rules: []config.PermissionRule{
		{Tool: "github__*", Decision: "ask"},
	}})
	if r := p.Check(Call{Tool: "github__create_pr"}); r.Decision != DecisionAsk {
		t.Fatalf("tool-only ask = %+v", r)
	}
}

func TestSplitCommand(t *testing.T) {
	segs, unsafe := splitCommand(`go test ./... && echo "a && b" | cat; ls`)
	if len(segs) != 4 || unsafe {
		t.Fatalf("split = %q unsafe=%v", segs, unsafe)
	}
	if segs[0] != "go test ./..." || segs[1] != `echo "a && b"` || segs[2] != "cat" || segs[3] != "ls" {
		t.Fatalf("split = %q", segs)
	}
	segs, unsafe = splitCommand("a | b\nc")
	if len(segs) != 3 || unsafe {
		t.Fatalf("newline split = %q unsafe=%v", segs, unsafe)
	}
}

func TestNewValidationErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  config.Permissions
		want string
	}{
		{"default", config.Permissions{Default: "nope"}, "permissions.default"},
		{"non_interactive", config.Permissions{NonInteractive: "ask"}, "permissions.non_interactive"},
		{"no tool", config.Permissions{Rules: []config.PermissionRule{{Decision: "deny"}}}, "tool is required"},
		{"no decision", config.Permissions{Rules: []config.PermissionRule{{Tool: "bash"}}}, "decision must be"},
		{"two matchers", config.Permissions{Rules: []config.PermissionRule{{Tool: "bash", Command: "x", Path: "y", Decision: "deny"}}}, "at most one"},
		{"path on bash", config.Permissions{Rules: []config.PermissionRule{{Tool: "bash", Path: "x", Decision: "deny"}}}, "does not apply"},
		{"command on read_file", config.Permissions{Rules: []config.PermissionRule{{Tool: "read_file", Command: "x", Decision: "deny"}}}, "does not apply"},
		{"host on bash", config.Permissions{Rules: []config.PermissionRule{{Tool: "bash", Host: "*.x", Decision: "deny"}}}, "does not apply"},
		{"bad path", config.Permissions{Rules: []config.PermissionRule{{Tool: "write_file", Path: "a/../b", Decision: "deny"}}}, "path"},
		{"bad host", config.Permissions{Rules: []config.PermissionRule{{Tool: "web_fetch", Host: "*", Decision: "deny"}}}, "host"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.cfg.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Validate() = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestSummary(t *testing.T) {
	p := policy(t, config.Permissions{Default: "ask", NonInteractive: "allow", Rules: []config.PermissionRule{
		{Tool: "bash", Command: "curl *", Decision: "deny"},
	}})
	s := p.Summary()
	if s.Default != "ask" || s.NonInteractive != "allow" || s.Rules != 1 {
		t.Fatalf("Summary = %+v", s)
	}
	if len(p.Rules()) != 1 || p.Rules()[0].Decision != "deny" {
		t.Fatalf("Rules = %+v", p.Rules())
	}
}
