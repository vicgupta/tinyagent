package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fakeUserDirs points os.UserConfigDir at a temp dir on every OS and returns
// the default config path inside it.
func fakeUserDirs(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AppData", filepath.Join(home, "AppData"))
	p, err := DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p, home) || !strings.HasSuffix(p, filepath.Join("tinyagent", "config.yaml")) {
		t.Fatalf("DefaultPath = %q, want <%s/...>/tinyagent/config.yaml", p, home)
	}
	return p
}

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func load(t *testing.T, explicit string) (AgentConfig, error) {
	t.Helper()
	return Load(Find(explicit))
}

func TestNoUserConfigUsesDefaults(t *testing.T) {
	fakeUserDirs(t)
	if got := Find(""); got != "" {
		t.Errorf("Find(\"\") = %q, want \"\" when no user config exists", got)
	}
	cfg, err := load(t, "")
	if err != nil || !reflect.DeepEqual(cfg, Default()) {
		t.Errorf("load = %+v, %v; want the defaults", cfg, err)
	}
}

func TestUserConfigOverridesDefaults(t *testing.T) {
	p := fakeUserDirs(t)
	write(t, p, "step_limit: 50\nbash_timeout_seconds: 300\n")
	if got := Find(""); got != p {
		t.Errorf("Find(\"\") = %q, want %q", got, p)
	}
	cfg, err := load(t, "")
	if err != nil || cfg.StepLimit != 50 || cfg.BashTimeoutSec != 300 || cfg.MaxOutputChars != Default().MaxOutputChars {
		t.Errorf("load = %+v, %v; want step_limit 50, bash timeout 300, other defaults kept", cfg, err)
	}
}

func TestExplicitConfigWins(t *testing.T) {
	p := fakeUserDirs(t)
	write(t, p, "step_limit: 50\n")
	explicit := filepath.Join(t.TempDir(), "ci.yaml")
	write(t, explicit, "step_limit: 7\n")
	if got := Find(explicit); got != explicit {
		t.Errorf("Find(%q) = %q", explicit, got)
	}
	if cfg, err := load(t, explicit); err != nil || cfg.StepLimit != 7 {
		t.Errorf("load = %+v, %v; want step_limit 7 from the explicit file", cfg, err)
	}
}

func TestMissingExplicitConfigFails(t *testing.T) {
	p := fakeUserDirs(t)
	write(t, p, "step_limit: 50\n")
	missing := filepath.Join(t.TempDir(), "nope.yaml")
	if _, err := load(t, missing); err == nil || !strings.Contains(err.Error(), missing) {
		t.Errorf("err = %v, want a read error naming %s (no fallback to the user config)", err, missing)
	}
}

func TestBadUserConfigFailsNamingPath(t *testing.T) {
	p := fakeUserDirs(t)
	write(t, p, "step_limit: [not a number\n")
	if _, err := load(t, ""); err == nil || !strings.Contains(err.Error(), p) {
		t.Errorf("malformed: err = %v, want a parse error naming %s", err, p)
	}

	p = fakeUserDirs(t)
	if err := os.MkdirAll(p, 0o755); err != nil { // a directory where the file should be
		t.Fatal(err)
	}
	if _, err := load(t, ""); err == nil || !strings.Contains(err.Error(), p) {
		t.Errorf("unreadable: err = %v, want a read error naming %s", err, p)
	}
}

func TestNoUserConfigDir(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("AppData", "")
	if got := Find(""); got != "" {
		t.Errorf("Find(\"\") = %q, want \"\" when there's no user config dir", got)
	}
}

func TestConfigSetsModel(t *testing.T) {
	p := fakeUserDirs(t)
	write(t, p, "model: ollama/llama3.1\n")
	if cfg, err := load(t, ""); err != nil || cfg.Model != "ollama/llama3.1" {
		t.Errorf("user config: load = %+v, %v; want model ollama/llama3.1", cfg, err)
	}
	explicit := filepath.Join(t.TempDir(), "ci.yaml")
	write(t, explicit, "model: anthropic/claude-sonnet-4\n")
	if cfg, err := load(t, explicit); err != nil || cfg.Model != "anthropic/claude-sonnet-4" {
		t.Errorf("explicit config: load = %+v, %v; want model anthropic/claude-sonnet-4", cfg, err)
	}
	if Default().Model != "" {
		t.Errorf("Default().Model = %q, want empty so the CLI's fallbacks apply", Default().Model)
	}
}

func TestUnknownKeyFailsNamingPathAndKey(t *testing.T) {
	p := fakeUserDirs(t)
	write(t, p, "cost_limit: 2.0\nstep_limt: 20\n")
	if _, err := load(t, ""); err == nil || !strings.Contains(err.Error(), p) || !strings.Contains(err.Error(), "step_limt") {
		t.Errorf("user config: err = %v, want an error naming %s and step_limt", err, p)
	}

	explicit := filepath.Join(t.TempDir(), "ci.yaml")
	write(t, explicit, "step_limit: 7\nwall_time_secs: 60\n")
	if _, err := load(t, explicit); err == nil || !strings.Contains(err.Error(), explicit) || !strings.Contains(err.Error(), "wall_time_secs") {
		t.Errorf("explicit config: err = %v, want an error naming %s and wall_time_secs", err, explicit)
	}
}

func TestMCPServerConfig(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	write(t, p, "mcp_servers:\n  - alias: docs\n    command: /usr/local/bin/docs-mcp\n    args: [--stdio]\n    tools: [search]\n    env: [DOCS_TOKEN]\n    discovery_timeout_seconds: 10\n    call_timeout_seconds: 30\n")
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.MCPServers) != 1 || cfg.MCPServers[0].Alias != "docs" || cfg.MCPServers[0].Tools[0] != "search" || cfg.MCPServers[0].CallTimeoutSec != 30 {
		t.Fatalf("MCP config = %+v", cfg.MCPServers)
	}
}

func TestEmptyConfigUsesDefaults(t *testing.T) {
	for name, data := range map[string]string{"empty": "", "comments only": "# step_limit: 50\n\n# nothing set\n"} {
		p := filepath.Join(t.TempDir(), "config.yaml")
		write(t, p, data)
		if cfg, err := Load(p); err != nil || !reflect.DeepEqual(cfg, Default()) {
			t.Errorf("%s: Load = %+v, %v; want the defaults", name, cfg, err)
		}
	}
}

func TestHooksConfig(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.yaml")
	write(t, p, `hooks:
  pre_tool:
    - match: "bash|start_process"
      command: /usr/local/bin/policy
      args: [--profile, ci]
      env: [POLICY_TOKEN]
      timeout_seconds: 10
      on_error: allow
  post_tool:
    - command: /usr/local/bin/fmt-check
`)
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	pre, post := cfg.Hooks.PreTool, cfg.Hooks.PostTool
	if len(pre) != 1 || pre[0].Match != "bash|start_process" || pre[0].Command != "/usr/local/bin/policy" || len(pre[0].Args) != 2 ||
		pre[0].Env[0] != "POLICY_TOKEN" || pre[0].TimeoutSec == nil || *pre[0].TimeoutSec != 10 || pre[0].OnError != "allow" {
		t.Errorf("pre_tool = %+v", pre)
	}
	if len(post) != 1 || post[0].TimeoutSec != nil || post[0].Match != "" {
		t.Errorf("post_tool = %+v", post)
	}

	write(t, p, "hooks:\n  pre_tool:\n    - command: /bin/x\n      mach: bash\n")
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "mach") || !strings.Contains(err.Error(), p) {
		t.Errorf("unknown hook key: err = %v", err)
	}
}

func TestSandboxConfig(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) string {
		p := filepath.Join(dir, "c.yaml")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cfg, err := Load(write("sandbox: true\nsandbox_network: true\nsandbox_writable_paths: [" + dir + "]\n"))
	if err != nil || !cfg.Sandbox || !cfg.SandboxNetwork || len(cfg.SandboxWritablePaths) != 1 {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
	for _, body := range []string{
		"sandbox_writable_paths: [rel/dir]\n",
		"sandbox_writable_paths: [" + filepath.Join(dir, "missing") + "]\n",
	} {
		p := write(body)
		if _, err := Load(p); err == nil || !strings.Contains(err.Error(), p) || !strings.Contains(err.Error(), "sandbox_writable_paths") {
			t.Errorf("%q: err=%v", body, err)
		}
	}
	cfg, err = Load(write("sandbox: true\nsandbox_allowed_domains: [proxy.golang.org, \"*.npmjs.org\", \"api.example.com:8443\"]\n"))
	if err != nil || len(cfg.SandboxAllowedDomains) != 3 {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
	for _, entry := range []string{`"*"`, `"foo.*.com"`, `"host:0"`} {
		p := write("sandbox_allowed_domains: [ok.example, " + entry + "]\n")
		if _, err := Load(p); err == nil || !strings.Contains(err.Error(), p) || !strings.Contains(err.Error(), "sandbox_allowed_domains entry "+entry) {
			t.Errorf("%s: err=%v", entry, err)
		}
	}
	if _, err := Load(write("sandox: true\n")); err == nil || !strings.Contains(err.Error(), "sandox") {
		t.Errorf("misspelled key accepted: %v", err)
	}
}

func TestEnvironmentConfig(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) string {
		p := filepath.Join(dir, "c.yaml")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cfg, err := Load(write("environment: container\ncontainer:\n  runtime: podman\n  image: golang:1.23\n  run_args: [--network, none]\n"))
	if err != nil || cfg.Environment != "container" || cfg.Container.Runtime != "podman" || cfg.Container.Image != "golang:1.23" || len(cfg.Container.RunArgs) != 2 {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
	for _, body := range []string{"environment: vm\n", "container: {runtime: lxc}\n", "container: {imgae: x}\n", "enviroment: local\n"} {
		if _, err := Load(write(body)); err == nil {
			t.Errorf("%q accepted", body)
		}
	}
}

func TestCommandEnvConfig(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.yaml")
	write(t, p, "command_env:\n  inherit: core\n  exclude_secrets: true\n  exclude: [\"AWS_*\"]\n  pass: [GOPATH]\n  set:\n    CI: \"true\"\n")
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	want := CommandEnv{Inherit: "core", ExcludeSecrets: true, Exclude: []string{"AWS_*"}, Pass: []string{"GOPATH"}, Set: map[string]string{"CI": "true"}}
	if !reflect.DeepEqual(cfg.CommandEnv, want) || !cfg.CommandEnv.Configured() {
		t.Fatalf("got %+v", cfg.CommandEnv)
	}
	if Default().CommandEnv.Configured() {
		t.Error("default policy is configured")
	}
	for _, c := range []struct{ yaml, want string }{
		{"command_env: {inherit: bogus}", "command_env.inherit"},
		{"command_env: {unknown_key: 1}", "unknown_key"},
		{"command_env: {exclude: [\"\"]}", "command_env.exclude"},
		{"command_env: {pass: [\"A=B\"]}", "command_env.pass"},
		{"command_env: {pass: [\"[A\"]}", "malformed glob"},
		{"command_env: {set: {\"A=B\": x}}", "command_env.set"},
		{"command_env: {set: {\"A*\": x}}", "command_env.set"},
		{"command_env: {set: {CI: true}}", "command_env.set CI"},
		{"command_env: {set: {N: 1}}", "command_env.set N"},
		{"command_env: [all]", "command_env"},
	} {
		write(t, p, c.yaml+"\n")
		_, err := Load(p)
		var invalid *InvalidError
		if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), p) || !errors.As(err, &invalid) {
			t.Errorf("%s: err = %v, want InvalidError naming %q and the file", c.yaml, err, c.want)
		}
	}
}

func TestMaxOutputTokensConfig(t *testing.T) {
	d := t.TempDir()
	p := d + "/c.yaml"
	for body, bad := range map[string]bool{
		"max_output_tokens: 4096\nmax_truncations: 2\n": false,
		"max_output_tokens: -1\n":                       true,
		"max_output_tokens: 1.5\n":                      true,
		"max_truncations: 0\n":                          true,
	} {
		os.WriteFile(p, []byte(body), 0o644)
		_, err := Load(p)
		if (err != nil) != bad {
			t.Errorf("%q: err = %v", body, err)
		}
	}
	if Default().MaxTruncations != 3 || Default().MaxOutputTokens != 0 {
		t.Error("defaults")
	}
}

func TestSubagentsConfig(t *testing.T) {
	if d := Default().Subagents; d.Enabled || d.Model != "" || d.MaxSteps != 0 {
		t.Errorf("default subagents = %+v", d)
	}
	p := filepath.Join(t.TempDir(), "config.yaml")
	write(t, p, "subagents:\n  enabled: true\n  model: openai/gpt-5-mini\n  max_steps: 12\n")
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if want := (Subagents{Enabled: true, Model: "openai/gpt-5-mini", MaxSteps: 12}); cfg.Subagents != want {
		t.Errorf("subagents = %+v", cfg.Subagents)
	}
	for _, c := range []struct{ body, want string }{
		{"subagents:\n  enabled: true\n  tools: [bash]\n", "tools"},
		{"subagents:\n  max_steps: -2\n", "subagents.max_steps"},
	} {
		write(t, p, c.body)
		if _, err := Load(p); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: err = %v, want one naming %s", c.body, err, c.want)
		}
	}
}

func TestConfigFallbackModels(t *testing.T) {
	p := fakeUserDirs(t)
	write(t, p, "fallback_models: [anthropic/claude-sonnet-4-5, ollama/qwen3-coder]\n")
	cfg, err := load(t, "")
	if err != nil || len(cfg.FallbackModels) != 2 || cfg.FallbackModels[0] != "anthropic/claude-sonnet-4-5" || cfg.FallbackModels[1] != "ollama/qwen3-coder" {
		t.Errorf("load = %+v, %v", cfg.FallbackModels, err)
	}
	if len(Default().FallbackModels) != 0 {
		t.Errorf("Default().FallbackModels = %v, want none", Default().FallbackModels)
	}
}
