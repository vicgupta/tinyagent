package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestStripUntrusted(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.yaml")
	yaml := `mcp_servers:
  - {alias: fs, command: [mcp-fs]}
hooks:
  pre_tool: [{command: [./h.sh]}]
command_tools:
  - {name: lint, description: lint, argv: [make, lint]}
tinygo: /opt/tinygo
claude_code: {path: /bin/claude}
codex_exec: {path: /bin/codex}
opencode: {path: /bin/opencode}
verify_on_stop: {checks: [make test]}
step_limit: 7
`
	if err := os.WriteFile(p, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Skipf("fixture doesn't match this config schema: %v", err)
	}
	got := StripUntrusted(&cfg)
	want := []string{"mcp_servers", "hooks", "command_tools", "tinygo", "claude_code.path", "codex_exec.path", "opencode.path", "verify_on_stop.checks"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("stripped %v, want %v", got, want)
	}
	if again := StripUntrusted(&cfg); again != nil {
		t.Errorf("second strip %v", again)
	}
	if cfg.StepLimit != 7 {
		t.Errorf("safe key dropped: step_limit %d", cfg.StepLimit)
	}
}

func TestLocate(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	if s := Locate("x.yaml", dir); s.Kind != SourceFlag || s.Path != "x.yaml" || s.Label() != "x.yaml (from -c)" {
		t.Errorf("-c: %+v", s)
	}
	if s := Locate("", dir); s.Kind != SourceNone || s.Path != "" || len(s.Notes) != 0 || s.Label() != "none (built-in defaults)" {
		t.Errorf("none: %+v", s)
	}
	user, _ := DefaultPath()
	os.MkdirAll(filepath.Dir(user), 0o700)
	os.WriteFile(user, []byte("{}\n"), 0o600)
	if s := Locate("", dir); s.Kind != SourceNone || len(s.Notes) != 1 {
		t.Errorf("user config only: %+v", s)
	}
	p := filepath.Join(dir, ProjectFile)
	os.MkdirAll(filepath.Dir(p), 0o700)
	os.WriteFile(p, []byte("{}\n"), 0o600)
	if s := Locate("", dir); s.Kind != SourceCwd || s.Path != p || len(s.Notes) != 0 || s.Label() != p+" (cwd)" {
		t.Errorf("cwd: %+v", s)
	}
}

// The old ./tinyagent.yaml is never read; it only adds a hint to move it,
// whether or not .tinyagent/tinyagent.yaml exists.
func TestLocateLegacyProjectFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	legacy := filepath.Join(dir, LegacyProjectFile)
	os.WriteFile(legacy, []byte("step_limit: 3\n"), 0o600)
	s := Locate("", dir)
	if s.Kind != SourceNone || s.Path != "" || len(s.Notes) != 1 || !strings.Contains(s.Notes[0], legacy+" is no longer read") || !strings.Contains(s.Notes[0], ".tinyagent/tinyagent.yaml") {
		t.Errorf("legacy only: %+v", s)
	}
	// -c skips the cwd lookup and its hint.
	if s := Locate("x.yaml", dir); len(s.Notes) != 0 {
		t.Errorf("-c: %+v", s)
	}
	p := filepath.Join(dir, ProjectFile)
	os.MkdirAll(filepath.Dir(p), 0o700)
	os.WriteFile(p, []byte("{}\n"), 0o600)
	if s := Locate("", dir); s.Kind != SourceCwd || s.Path != p || len(s.Notes) != 1 {
		t.Errorf("both: %+v", s)
	}
}

// A .tinyagent that is a plain file means no project config, not an error.
func TestLocateTinyagentNotADir(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".tinyagent"), []byte("x"), 0o600)
	if s := Locate("", dir); s.Kind != SourceNone || s.Path != "" {
		t.Errorf("got %+v", s)
	}
}
