package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// checkEnv isolates --check from the caller's environment: no user config,
// no keys, no $AGENT_MODEL, and OPENAI_BASE_URL pointing at a server that
// counts requests, so a test can assert none were made.
func checkEnv(t *testing.T) *atomic.Int32 {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		http.Error(w, "unexpected request", http.StatusTeapot)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("AGENT_MODEL", "")
	for _, k := range []string{"OPENAI_API_KEY", "OPENROUTER_API_KEY", "ANTHROPIC_API_KEY", "OPENAI_BASE_URL", "ANTHROPIC_BASE_URL", "OLLAMA_HOST"} {
		t.Setenv(k, "")
	}
	t.Setenv("ANTHROPIC_BASE_URL", srv.URL)
	return &hits
}

// reportLine returns the value of the "key:" line in a --check report.
func reportLine(t *testing.T, out, key string) string {
	t.Helper()
	for _, l := range strings.Split(out, "\n") {
		if v, ok := strings.CutPrefix(l, key+":"); ok {
			return strings.TrimSpace(v)
		}
	}
	t.Fatalf("no %q line in report:\n%s", key, out)
	return ""
}

func TestCheckValidSetupExits0WithoutRequestOrSecret(t *testing.T) {
	hits := checkEnv(t)
	const sentinel = "sk-ant-SENTINEL-do-not-print"
	t.Setenv("ANTHROPIC_API_KEY", sentinel)
	dir := t.TempDir()
	code, stdout, stderr := runCLI(t, "--check", "-m", "anthropic/claude-sonnet-4-5", "--cwd", dir)
	if code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, stderr)
	}
	if strings.Contains(stdout+stderr, sentinel) {
		t.Fatalf("the key value leaked into the output:\n%s%s", stdout, stderr)
	}
	if got := reportLine(t, stdout, "model"); got != "anthropic/claude-sonnet-4-5 (from -m)" {
		t.Errorf("model line %q", got)
	}
	if got := reportLine(t, stdout, "provider"); got != "anthropic, ANTHROPIC_API_KEY is set" {
		t.Errorf("provider line %q", got)
	}
	if got := reportLine(t, stdout, "config"); got != "none (built-in defaults)" {
		t.Errorf("config line %q", got)
	}
	if got := reportLine(t, stdout, "fallback"); got != "none" {
		t.Errorf("fallback line %q", got)
	}
	root, _ := canonicalRoot(dir)
	if got := reportLine(t, stdout, "cwd"); got != root+" (from --cwd)" {
		t.Errorf("cwd line %q, want %q", got, root+" (from --cwd)")
	}
	if got := reportLine(t, stdout, "limits"); got != "steps unlimited, cost unlimited, tokens unlimited, time unlimited" {
		t.Errorf("limits line %q", got)
	}
	if !strings.HasSuffix(stdout, "\nok\n") {
		t.Errorf("report doesn't end with ok:\n%s", stdout)
	}
	if hits.Load() != 0 {
		t.Errorf("--check made %d requests", hits.Load())
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("--check wrote into --cwd: %v", entries)
	}
}

func TestCheckSourceLabels(t *testing.T) {
	checkEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "k")
	t.Setenv("OPENROUTER_API_KEY", "k")
	cfg := writeConfig(t, "model: openrouter/some/model\nstep_limit: 40\ncost_limit: 2.5\n")

	_, stdout, stderr := runCLI(t, "--check", "-c", cfg, "--time-limit", "90")
	if got := reportLine(t, stdout, "model"); got != "openrouter/some/model (from config)" {
		t.Errorf("config model: %q; stderr %s", got, stderr)
	}
	if got := reportLine(t, stdout, "config"); got != cfg+" (from -c)" {
		t.Errorf("config line %q", got)
	}
	if got := reportLine(t, stdout, "limits"); got != "steps 40 (from config), cost $2.50 (from config), tokens unlimited, time 90s (from --time-limit)" {
		t.Errorf("limits line %q", got)
	}
	if got := reportLine(t, stdout, "cwd"); !strings.HasSuffix(got, " (default)") {
		t.Errorf("cwd line %q", got)
	}

	t.Setenv("AGENT_MODEL", "anthropic/claude-haiku-4-5")
	_, stdout, _ = runCLI(t, "--check", "-c", cfg)
	if got := reportLine(t, stdout, "model"); got != "anthropic/claude-haiku-4-5 (from $AGENT_MODEL)" {
		t.Errorf("env model: %q", got)
	}

	_, stdout, _ = runCLI(t, "--check", "-c", cfg, "-m", "openrouter/x/y")
	if got := reportLine(t, stdout, "model"); got != "openrouter/x/y (from -m)" {
		t.Errorf("flag model: %q", got)
	}

}

func TestCheckProjectConfigAndFallbacks(t *testing.T) {
	checkEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "k")
	t.Setenv("OPENROUTER_API_KEY", "k")
	xdg := os.Getenv("XDG_CONFIG_HOME")
	p := filepath.Join(xdg, "tinyagent", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("model: anthropic/claude-sonnet-4-5\nfallback_models: [openrouter/a/b]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The old user config is no longer read: a project file is.
	cwd := t.TempDir()
	if err := os.Rename(p, filepath.Join(cwd, "tinyagent.yaml")); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runCLI(t, "--check", "--cwd", cwd)
	if code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, stderr)
	}
	if got, want := reportLine(t, stdout, "config"), filepath.Join(cwd, "tinyagent.yaml")+" (cwd)"; got != want {
		t.Errorf("config line %q, want %q", got, want)
	}
	if got := reportLine(t, stdout, "fallback"); got != "openrouter/a/b (from config)" {
		t.Errorf("fallback line %q", got)
	}
	_, stdout, _ = runCLI(t, "--check", "--cwd", cwd, "--no-fallback")
	if got := reportLine(t, stdout, "fallback"); got != "none (--no-fallback)" {
		t.Errorf("no-fallback line %q", got)
	}
}

func TestCheckProviderDetails(t *testing.T) {
	checkEnv(t)
	t.Setenv("OLLAMA_HOST", "0.0.0.0:9999")
	_, stdout, stderr := runCLI(t, "--check", "-m", "ollama/llama3")
	if got := reportLine(t, stdout, "provider"); got != "ollama, OLLAMA_HOST resolves to http://127.0.0.1:9999" {
		t.Errorf("ollama line %q; stderr %s", got, stderr)
	}

	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	auth := filepath.Join(home, "auth.json")
	if err := os.WriteFile(auth, []byte(`{"tokens":{"access_token":"SENTINEL-ACCESS","refresh_token":"SENTINEL-REFRESH","account_id":"a"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runCLI(t, "--check", "-m", "codex/gpt-5")
	if code != 0 {
		t.Fatalf("codex: exit %d; stderr:\n%s", code, stderr)
	}
	if got := reportLine(t, stdout, "provider"); got != "codex, login found at "+auth {
		t.Errorf("codex line %q", got)
	}
	if strings.Contains(stdout+stderr, "SENTINEL") {
		t.Errorf("codex token leaked:\n%s%s", stdout, stderr)
	}

	t.Setenv("GW_KEY", "SENTINEL-GW")
	cfg := writeConfig(t, "providers:\n  - id: gw\n    api: chat\n    base_url: https://gw.example.com/v1\n    api_key_env: GW_KEY\n")
	_, stdout, stderr = runCLI(t, "--check", "-m", "gw/m1", "-c", cfg)
	if got := reportLine(t, stdout, "provider"); got != "gw (configured, api chat, base_url https://gw.example.com/v1), GW_KEY is set" {
		t.Errorf("configured line %q; stderr %s", got, stderr)
	}
	if strings.Contains(stdout+stderr, "SENTINEL") {
		t.Errorf("configured key leaked:\n%s%s", stdout, stderr)
	}
}

// TestCheckErrorsMatchRun checks that each setup error exits 2 with the
// same error line a real run prints.
func TestCheckErrorsMatchRun(t *testing.T) {
	hits := checkEnv(t)
	badKey := writeConfig(t, "modle: openai/gpt-4o\n")
	gw := writeConfig(t, "providers:\n  - id: gw\n    api: chat\n    base_url: https://gw.example.com/v1\n    api_key_env: GW_KEY\n")
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"unknown provider", []string{"-m", "nope/m"}, `error: unknown provider "nope"`},
		{"unknown config key", []string{"-c", badKey, "-m", "ollama/m"}, "error: "},
		{"unreadable config", []string{"-c", filepath.Join(t.TempDir(), "missing.yaml"), "-m", "ollama/m"}, "error: read config"},
		{"configured key unset", []string{"-c", gw, "-m", "gw/m"}, `error: provider "gw": api_key_env GW_KEY is unset or empty`},
		{"bad fallback", []string{"-m", "ollama/m", "--fallback-model", "ollama/m"}, `error: fallback model "ollama/m" is the primary model`},
	}
	for _, c := range cases {
		code, stdout, stderr := runCLI(t, append([]string{"--check"}, c.args...)...)
		if code != 2 || stdout != "" || !strings.HasPrefix(stderr, c.want) {
			t.Errorf("%s: exit %d, stdout %q, stderr %q; want exit 2 and %q", c.name, code, stdout, stderr, c.want)
			continue
		}
		_, _, runErr := runCLI(t, append([]string{"-t", "x", "--no-stream"}, c.args...)...)
		first := func(s string) string { l, _, _ := strings.Cut(s, "\n"); return l }
		if first(stderr) != first(runErr) {
			t.Errorf("%s: --check said %q, a run said %q", c.name, first(stderr), first(runErr))
		}
	}
	if hits.Load() != 0 {
		t.Errorf("%d requests made", hits.Load())
	}
}

func TestCheckMissingKeyExits2WithHint(t *testing.T) {
	hits := checkEnv(t)
	code, stdout, stderr := runCLI(t, "--check", "-m", "anthropic/claude-sonnet-4-5")
	if code != 2 || stdout != "" {
		t.Fatalf("exit %d, stdout %q", code, stdout)
	}
	if !strings.HasPrefix(stderr, "error: ANTHROPIC_API_KEY is not set\nhint: ANTHROPIC_API_KEY isn't set") {
		t.Errorf("stderr %q", stderr)
	}
	if hits.Load() != 0 {
		t.Errorf("%d requests made", hits.Load())
	}
}

func TestCheckRejectsRunFlags(t *testing.T) {
	checkEnv(t)
	for _, args := range [][]string{{"-t", "x"}, {"--resume", "r.json"}, {"--rewind", "r.json"}, {"--worktree"}} {
		code, _, stderr := runCLI(t, append([]string{"--check", "-m", "ollama/m"}, args...)...)
		if code != 2 || !strings.Contains(stderr, "--check makes no model request") {
			t.Errorf("%v: exit %d, stderr %q", args, code, stderr)
		}
	}
}
