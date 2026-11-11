package main

import (
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vicgupta/tinyagent/internal/config"
)

// withoutPostureWarning drops the unsandboxed-commands warning line (#415)
// so tests that count other warnings keep working.
func withoutPostureWarning(s string) string {
	var out []string
	for _, l := range strings.SplitAfter(s, "\n") {
		if strings.HasPrefix(l, "warning: commands run unsandboxed") {
			continue
		}
		out = append(out, l)
	}
	return strings.Join(out, "")
}

func TestCommandsPosture(t *testing.T) {
	cases := []struct {
		name     string
		cfg      config.AgentConfig
		mode     string
		env      string
		warnWith string
	}{
		{"default", config.AgentConfig{}, commandsUnsandboxed, "full", "your full environment"},
		{"command_env", config.AgentConfig{CommandEnv: config.CommandEnv{Inherit: "core"}}, commandsUnsandboxed, "filtered", "the environment command_env allows"},
		{"sandbox", config.AgentConfig{Sandbox: true}, commandsSandboxed, "full", ""},
		{"read-only", config.AgentConfig{ReadOnly: true, Sandbox: true}, commandsNone, "none", ""},
		{"container", config.AgentConfig{Environment: "container"}, commandsContainer, "none", ""},
	}
	for _, c := range cases {
		p := commandsPosture(c.cfg)
		if p.Mode != c.mode || p.Environment != c.env {
			t.Errorf("%s: %+v, want mode %s env %s", c.name, p, c.mode, c.env)
		}
		if (c.warnWith == "") != (p.Warning == "") || !strings.Contains(p.Warning, c.warnWith) {
			t.Errorf("%s: warning %q, want one containing %q", c.name, p.Warning, c.warnWith)
		}
	}
}

func TestWarnExecPosture(t *testing.T) {
	off := config.BoolPtr(false)
	cases := []struct {
		name string
		cfg  config.AgentConfig
		want string
	}{
		{"unsandboxed", config.AgentConfig{}, "warning: commands run unsandboxed, as your user, with your full environment"},
		{"sandboxed", config.AgentConfig{Sandbox: true}, ""},
		{"read-only, web on", config.AgentConfig{ReadOnly: true}, "read-only mode: web_fetch and web_search still make network requests; --no-web-fetch and --no-web-search turn them off\n"},
		{"read-only, search on", config.AgentConfig{ReadOnly: true, WebFetch: config.WebFetch{Enabled: off}}, "read-only mode: web_search still makes network requests; --no-web-search turns it off\n"},
		{"read-only, web off", config.AgentConfig{ReadOnly: true, WebFetch: config.WebFetch{Enabled: off}, WebSearch: config.WebSearch{Enabled: off}}, ""},
	}
	for _, c := range cases {
		var b strings.Builder
		warnExecPosture(&b, c.cfg)
		if (c.want == "") != (b.String() == "") || !strings.HasPrefix(b.String(), c.want) {
			t.Errorf("%s: got %q, want prefix %q", c.name, b.String(), c.want)
		}
	}
}

func TestCheckReportsPosture(t *testing.T) {
	t.Setenv("AGENT_MODEL", "")
	cases := []struct {
		name     string
		args     []string
		commands string
		web      string
		warn     bool
	}{
		{"default", nil, "commands:  UNSANDBOXED, as your user, full environment (pass --sandbox to confine them)", "web:       web_fetch on (any public host), web_search on; --no-web-fetch and --no-web-search turn them off", true},
		{"read-only", []string{"--read-only"}, "commands:  none (--read-only)", "web:       web_fetch on (any public host), web_search on; --read-only does not turn them off; --no-web-fetch and --no-web-search turn them off", false},
		{"web off", []string{"--no-web-fetch", "--no-web-search"}, "commands:  UNSANDBOXED", "web:       web_fetch off, web_search off\n", true},
		{"allowlist", []string{"--no-web-search", "--web-fetch-allow-domain", "go.dev"}, "commands:  UNSANDBOXED", "web:       web_fetch on (1 allowed: go.dev), web_search off; --no-web-fetch turns it off", true},
	}
	for _, c := range cases {
		code, stdout, stderr := runCLI(t, append([]string{"--check", "-m", "ollama/m", "--cwd", t.TempDir()}, c.args...)...)
		if code != 0 {
			t.Fatalf("%s: exit %d: %s", c.name, code, stderr)
		}
		if !strings.Contains(stdout, c.commands) || !strings.Contains(stdout, c.web) {
			t.Errorf("%s: stdout missing %q or %q:\n%s", c.name, c.commands, c.web, stdout)
		}
		if got := strings.Contains(stderr, "warning: commands run unsandboxed"); got != c.warn {
			t.Errorf("%s: stderr warning %v, want %v:\n%s", c.name, got, c.warn, stderr)
		}
		if strings.Contains(stdout, "warning:") {
			t.Errorf("%s: warning on stdout:\n%s", c.name, stdout)
		}
	}
}

func TestCheckJSONPosture(t *testing.T) {
	t.Setenv("AGENT_MODEL", "")
	code, stdout, stderr := runCLI(t, "--check", "--json", "-m", "ollama/m", "--cwd", t.TempDir(), "--read-only", "--no-web-fetch")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	var s checkSummary
	if err := json.Unmarshal([]byte(stdout), &s); err != nil {
		t.Fatalf("not one JSON object: %v\n%s", err, stdout)
	}
	if s.Commands != (checkCommandsInfo{Mode: commandsNone, Environment: "none"}) {
		t.Errorf("commands = %+v", s.Commands)
	}
	if s.WebTools.WebFetch || !s.WebTools.WebSearch || !s.WebTools.ReadOnly || s.WebTools.WebFetchDomains == nil {
		t.Errorf("web_tools = %+v", s.WebTools)
	}

	code, stdout, stderr = runCLI(t, "--check", "--json", "-m", "ollama/m", "--cwd", t.TempDir())
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	s = checkSummary{}
	if err := json.Unmarshal([]byte(stdout), &s); err != nil {
		t.Fatal(err)
	}
	if s.Commands.Mode != commandsUnsandboxed || s.Commands.Environment != "full" || s.Commands.Warning == "" || !s.WebTools.WebFetch || !s.WebTools.WebSearch || s.WebTools.ReadOnly {
		t.Errorf("summary = %+v %+v", s.Commands, s.WebTools)
	}
	if !strings.Contains(stderr, "warning: "+s.Commands.Warning) {
		t.Errorf("stderr lacks the warning:\n%s", stderr)
	}
}

func TestCheckSandboxedPostureHasNoWarning(t *testing.T) {
	t.Setenv("AGENT_MODEL", "")
	defer func(f func(string, bool, []string) (string, []string, error)) { probeSandbox = f }(probeSandbox)
	probeSandbox = func(string, bool, []string) (string, []string, error) { return "sandbox: fake", nil, nil }
	code, stdout, stderr := runCLI(t, "--check", "-m", "ollama/m", "--cwd", t.TempDir(), "--sandbox")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "commands:  sandboxed, full environment (see sandbox)") || strings.Contains(stderr, "unsandboxed") {
		t.Errorf("stdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
}

func TestRunWarnsUnsandboxedOnStderrOnly(t *testing.T) {
	var requests atomic.Int32
	srv := fakeOpenAI(t, &requests)
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	t.Setenv("OPENAI_API_KEY", "fake")
	t.Setenv("AGENT_MODEL", "")
	dir := t.TempDir()
	code, stdout, stderr := runCLI(t, "-t", "x", "--no-stream", "--json", "--cwd", dir, "-m", "openai/gpt-4o")
	if code != 0 || requests.Load() != 1 {
		t.Fatalf("exit %d, requests %d: %s", code, requests.Load(), stderr)
	}
	if !strings.Contains(stderr, "warning: commands run unsandboxed, as your user, with your full environment") {
		t.Errorf("stderr lacks the warning:\n%s", stderr)
	}
	for _, l := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if !json.Valid([]byte(l)) || strings.Contains(l, "unsandboxed") {
			t.Errorf("stdout line %q", l)
		}
	}

	code, _, stderr = runCLI(t, "-t", "x", "--no-stream", "--cwd", dir, "-m", "openai/gpt-4o", "--read-only")
	if code != 0 {
		t.Fatalf("read-only: exit %d: %s", code, stderr)
	}
	if strings.Contains(stderr, "unsandboxed") || !strings.Contains(stderr, "read-only mode: web_fetch and web_search still make network requests") {
		t.Errorf("read-only stderr:\n%s", stderr)
	}
}
