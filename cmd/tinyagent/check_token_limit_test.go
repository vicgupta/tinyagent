package main

import (
	"strings"
	"testing"
)

func TestCheckShowsTokenLimit(t *testing.T) {
	checkEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "k")
	cfg := writeConfig(t, "model: anthropic/claude-haiku-4-5\ntoken_limit: 9000\n")
	_, stdout, stderr := runCLI(t, "--check", "-c", cfg)
	if got := reportLine(t, stdout, "limits"); got != "steps unlimited, cost unlimited, tokens 9000 (from config), time unlimited" {
		t.Errorf("limits line %q; stderr %s", got, stderr)
	}
	_, stdout, _ = runCLI(t, "--check", "-c", cfg, "--token-limit", "500")
	if got := reportLine(t, stdout, "limits"); !strings.Contains(got, "tokens 500 (from --token-limit)") {
		t.Errorf("limits line %q", got)
	}
}

func TestStarterConfigMentionsTokenLimit(t *testing.T) {
	if !strings.Contains(starterConfig, "\n# token_limit: 0 ") {
		t.Fatalf("starter config has no token_limit line:\n%s", starterConfig)
	}
}
