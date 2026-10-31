package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestCheckSummaryTable(t *testing.T) {
	t.Setenv("AGENT_MODEL", "")
	t.Setenv("ANTHROPIC_API_KEY", "sk-table-sentinel-4242")
	cfg := writeConfig(t, "permissions:\n  default: allow\n  rules:\n    - tool: bash\n      command: \"rm *\"\n      decision: deny\n")
	code, stdout, stderr := runCLI(t, "--check", "-m", "anthropic/claude-sonnet-4-5", "--cwd", t.TempDir(), "-c", cfg)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	want := "summary\n" +
		"  model        anthropic/claude-sonnet-4-5\n" +
		"  provider     anthropic\n" +
		"  api key      ANTHROPIC_API_KEY set\n" +
		"  sandbox      off\n" +
		"  permissions  1 rules, default allow, non-interactive deny\n" +
		"  lockdown     off\n" +
		"  config       CFG (-c)\n\n"
	got := strings.ReplaceAll(stdout, cfg, "CFG")
	if !strings.HasPrefix(got, want) {
		t.Errorf("table:\n%s\nwant prefix:\n%s", got, want)
	}
	if !strings.Contains(got, "\nconfig:    CFG (from -c)\n") || !strings.HasSuffix(got, "ok\n") {
		t.Errorf("detail lines missing:\n%s", got)
	}
	if strings.Contains(stdout, "sentinel") || strings.Contains(stdout, "\x1b[") {
		t.Errorf("table leaks the key or has color:\n%s", stdout)
	}
}

func TestCheckJSON(t *testing.T) {
	t.Setenv("AGENT_MODEL", "")
	t.Setenv("ANTHROPIC_API_KEY", "sk-json-sentinel-4242")
	dir := t.TempDir()
	cfg := writeConfig(t, "step_limit: 7\ncost_limit: 1.5\n")
	code, stdout, stderr := runCLI(t, "--check", "--json", "-m", "anthropic/claude-sonnet-4-5", "--cwd", dir, "-c", cfg)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(stdout), &raw); err != nil {
		t.Fatalf("not one JSON object: %v\n%s", err, stdout)
	}
	for _, k := range []string{"schema_version", "ok", "config", "profile", "model", "provider", "fallbacks", "sandbox", "permissions", "lockdown", "cwd", "limits", "error"} {
		if _, ok := raw[k]; !ok {
			t.Errorf("missing field %q", k)
		}
	}
	var s checkSummary
	if err := json.Unmarshal([]byte(stdout), &s); err != nil {
		t.Fatal(err)
	}
	if s.SchemaVersion != 1 || !s.OK || s.Error != nil || s.Model.Spec != "anthropic/claude-sonnet-4-5" || s.Model.Source != "-m" ||
		s.Provider != (checkProvider{"anthropic", "ANTHROPIC_API_KEY", "set"}) || s.Config.Path != cfg || s.Config.Source != "-c" ||
		s.Limits.Steps != 7 || s.Limits.CostUSD != 1.5 || s.Cwd == "" || s.Sandbox.Enabled {
		t.Errorf("summary = %+v", s)
	}
	if strings.Contains(stdout, "sentinel") {
		t.Errorf("JSON leaks the key:\n%s", stdout)
	}
}

func TestCheckJSONMissingKey(t *testing.T) {
	t.Setenv("AGENT_MODEL", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "sk-other-sentinel-4242")
	code, stdout, stderr := runCLI(t, "--check", "--json", "-m", "anthropic/claude-sonnet-4-5", "--cwd", t.TempDir())
	if code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	var s checkSummary
	if err := json.Unmarshal([]byte(stdout), &s); err != nil {
		t.Fatalf("%v\n%s", err, stdout)
	}
	if s.OK || s.Provider.APIKey != "missing" || s.Provider.APIKeyEnv != "ANTHROPIC_API_KEY" || s.Error == nil || !strings.Contains(s.Error.Message, "ANTHROPIC_API_KEY") || s.Error.Hint == "" {
		t.Errorf("summary = %+v", s)
	}
	if !strings.Contains(stderr, "error: ANTHROPIC_API_KEY is not set") {
		t.Errorf("stderr = %q", stderr)
	}
	if strings.Contains(stdout+stderr, "sentinel") {
		t.Error("output leaks a key value")
	}
}

func TestCheckSummarySandboxUnavailable(t *testing.T) {
	t.Setenv("AGENT_MODEL", "")
	defer func(f func(string, bool, []string) (string, []string, error)) { probeSandbox = f }(probeSandbox)
	probeSandbox = func(string, bool, []string) (string, []string, error) { return "", nil, errUnavailableForTest }
	code, stdout, stderr := runCLI(t, "--check", "--json", "-m", "ollama/llama3", "--cwd", t.TempDir(), "--sandbox")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	var s checkSummary
	if err := json.Unmarshal([]byte(stdout), &s); err != nil {
		t.Fatal(err)
	}
	if !s.Sandbox.Enabled || s.Sandbox.Available || s.Sandbox.Backend != "auto" || s.Provider.APIKey != "n/a" {
		t.Errorf("summary = %+v", s)
	}
}

var errUnavailableForTest = errors.New("sandbox: not here")
