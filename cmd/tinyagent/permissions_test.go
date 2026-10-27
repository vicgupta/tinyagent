package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/permissions"
)

func TestCheckPermissionsSummary(t *testing.T) {
	checkEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-sentinel")
	dir := t.TempDir()
	cfg := filepath.Join(dir, "c.yaml")
	os.WriteFile(cfg, []byte(`
permissions:
  default: ask
  non_interactive: allow
  rules:
    - tool: bash
      command: "curl *"
      decision: deny
`), 0o644)
	code, stdout, stderr := runCLI(t, "--check", "-m", "anthropic/claude-sonnet-4-5", "--cwd", dir, "-c", cfg)
	if code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, stderr)
	}
	if got := reportLine(t, stdout, "permissions"); got != "1 rules, default ask, non-interactive allow" {
		t.Errorf("permissions line = %q", got)
	}
}

func TestCheckInvalidPermissionsExits2(t *testing.T) {
	checkEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "sk-ant-sentinel")
	dir := t.TempDir()
	cfg := filepath.Join(dir, "c.yaml")
	os.WriteFile(cfg, []byte("permissions:\n  rules:\n    - tool: bash\n      decision: maybe\n"), 0o644)
	code, _, stderr := runCLI(t, "--check", "-m", "anthropic/claude-sonnet-4-5", "--cwd", dir, "-c", cfg)
	if code != 2 || !strings.Contains(stderr, "permissions.rules[0]") {
		t.Fatalf("exit %d; stderr:\n%s", code, stderr)
	}
}

func TestParseTerminalAnswer(t *testing.T) {
	for _, tc := range []struct {
		line string
		want permissions.Answer
	}{
		{"y", permissions.Answer{Decision: permissions.DecisionAllow, Channel: "terminal"}},
		{"Y", permissions.Answer{Decision: permissions.DecisionAllow, Channel: "terminal"}},
		{"a", permissions.Answer{Decision: permissions.DecisionAllow, Always: true, Channel: "terminal"}},
		{"n", permissions.Answer{Decision: permissions.DecisionDeny, Channel: "terminal"}},
		{"n not today", permissions.Answer{Decision: permissions.DecisionDeny, Reason: "not today", Channel: "terminal"}},
		{"N because risky", permissions.Answer{Decision: permissions.DecisionDeny, Reason: "because risky", Channel: "terminal"}},
		{"", permissions.Answer{Decision: permissions.DecisionDeny, Channel: "terminal"}},
		{"garbage", permissions.Answer{Decision: permissions.DecisionDeny, Channel: "terminal"}},
	} {
		if got := parseTerminalAnswer(tc.line); got != tc.want {
			t.Errorf("parseTerminalAnswer(%q) = %+v, want %+v", tc.line, got, tc.want)
		}
	}
}
