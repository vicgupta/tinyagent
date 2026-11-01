package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cwdEnv is checkEnv plus provider keys, and returns a fresh --cwd dir.
func cwdEnv(t *testing.T) string {
	t.Helper()
	checkEnv(t)
	for _, k := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "OPENROUTER_API_KEY"} {
		t.Setenv(k, "k")
	}
	return t.TempDir()
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// userConfigDir returns <os.UserConfigDir>/tinyagent, the way the code
// derives it. Call it after HOME and XDG_CONFIG_HOME point at temp dirs
// (checkEnv does that): on macOS os.UserConfigDir ignores XDG_CONFIG_HOME
// and uses $HOME/Library/Application Support.
func userConfigDir(t *testing.T) string {
	t.Helper()
	dir, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "tinyagent")
}

func userConfigPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(userConfigDir(t), "config.yaml")
}

func TestCwdConfigPrecedence(t *testing.T) {
	dir := cwdEnv(t)
	cfg := filepath.Join(dir, "tinyagent.yaml")
	writeFile(t, cfg, "model: anthropic/claude-sonnet-4-5\nstep_limit: 40\nfallback_models: [openrouter/a/b, openrouter/c/d]\nask_user: {enabled: true, max_questions: 2}\n")

	code, stdout, stderr := runCLI(t, "--check", "--cwd", dir)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	for line, want := range map[string]string{
		"config":   cfg + " (cwd)",
		"model":    "anthropic/claude-sonnet-4-5 (from config)",
		"fallback": "openrouter/a/b, openrouter/c/d (from config)",
		"ask_user": "on, at most 2 questions per run (from config)",
	} {
		if got := reportLine(t, stdout, line); got != want {
			t.Errorf("%s: file beats defaults: got %q, want %q", line, got, want)
		}
	}
	if got := reportLine(t, stdout, "limits"); !strings.HasPrefix(got, "steps 40 (from config)") {
		t.Errorf("limits %q", got)
	}

	// CLI flags beat the file; a list flag replaces the file's list; an
	// explicit --flag=false turns off a file's true.
	code, stdout, stderr = runCLI(t, "--check", "--cwd", dir, "-m", "openai/gpt-4o", "--step-limit", "5", "--fallback-model", "openrouter/x/y", "--ask-user=false")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	for line, want := range map[string]string{
		"model":    "openai/gpt-4o (from -m)",
		"fallback": "openrouter/x/y (from --fallback-model)",
		"ask_user": "off (from --ask-user)",
	} {
		if got := reportLine(t, stdout, line); got != want {
			t.Errorf("%s: flag beats file: got %q, want %q", line, got, want)
		}
	}
	if got := reportLine(t, stdout, "limits"); !strings.HasPrefix(got, "steps 5 (from --step-limit)") {
		t.Errorf("limits %q", got)
	}

	// $AGENT_MODEL beats the file's model:.
	t.Setenv("AGENT_MODEL", "openrouter/e/f")
	_, stdout, _ = runCLI(t, "--check", "--cwd", dir)
	if got := reportLine(t, stdout, "model"); got != "openrouter/e/f (from $AGENT_MODEL)" {
		t.Errorf("env model: %q", got)
	}
}

// The file comes from --cwd, not the process directory, and only from that
// exact directory: a subdirectory doesn't see its parent's file.
func TestCwdConfigStrictLookup(t *testing.T) {
	dir := cwdEnv(t)
	writeFile(t, filepath.Join(dir, "tinyagent.yaml"), "model: anthropic/claude-sonnet-4-5\n")
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	_, stdout, _ := runCLI(t, "--check", "--cwd", sub)
	if got := reportLine(t, stdout, "config"); got != "none (built-in defaults)" {
		t.Errorf("subdirectory walked up: config %q", got)
	}
	// The process directory (cmd/tinyagent) has no file; the test runs there.
	t.Chdir(dir)
	_, stdout, _ = runCLI(t, "--check", "--cwd", sub)
	if got := reportLine(t, stdout, "config"); got != "none (built-in defaults)" {
		t.Errorf("process directory used instead of --cwd: config %q", got)
	}
	_, stdout, _ = runCLI(t, "--check")
	if got := reportLine(t, stdout, "config"); got != "tinyagent.yaml (cwd)" {
		t.Errorf("default --cwd .: config %q", got)
	}
}

func TestCwdConfigInvalidNoFallback(t *testing.T) {
	dir := cwdEnv(t)
	writeFile(t, userConfigPath(t), "model: anthropic/claude-sonnet-4-5\n")
	writeFile(t, filepath.Join(dir, "tinyagent.yaml"), "no_such_key: 1\n")
	code, _, stderr := runCLI(t, "--check", "--cwd", dir)
	if code != 2 || !strings.Contains(stderr, "no_such_key") {
		t.Errorf("invalid cwd file: exit %d, stderr %q", code, stderr)
	}
}

// The old per-user config is never read; a note points at init --migrate.
func TestUserConfigNoLongerRead(t *testing.T) {
	dir := cwdEnv(t)
	user := userConfigPath(t)
	writeFile(t, user, "model: openrouter/old/model\n")
	code, stdout, stderr := runCLI(t, "--check", "--cwd", dir)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if got := reportLine(t, stdout, "config"); got != "none (built-in defaults)" {
		t.Errorf("config %q", got)
	}
	if strings.Contains(stdout, "openrouter/old/model") {
		t.Errorf("user config was read:\n%s", stdout)
	}
	code, _, stderr = runCLI(t, "--list-profiles", "--cwd", dir)
	if want := "note: user config " + user + " is no longer read; run tinyagent init --migrate"; code != 0 || !strings.Contains(stderr, want) {
		t.Errorf("exit %d, stderr %q, want %q", code, stderr, want)
	}
	// With a project file present there is no note.
	writeFile(t, filepath.Join(dir, "tinyagent.yaml"), "step_limit: 3\n")
	if _, _, stderr = runCLI(t, "--list-profiles", "--cwd", dir); strings.Contains(stderr, "no longer read") {
		t.Errorf("note with a project file: %q", stderr)
	}
}

const untrustedYAML = `command_tools:
  - name: lint
    description: run the linter
    argv: [make, lint]
`

func TestCwdConfigTrust(t *testing.T) {
	dir := cwdEnv(t)
	writeFile(t, filepath.Join(dir, "tinyagent.yaml"), untrustedYAML)

	code, stdout, stderr := runCLI(t, "--check", "--cwd", dir)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if strings.Contains(stdout, "command tools") {
		t.Errorf("untrusted command_tools loaded:\n%s", stdout)
	}
	_, _, stderr = runCLI(t, "--list-plugins", "--cwd", dir)
	if !strings.Contains(stderr, "warning: ignoring command_tools from") || !strings.Contains(stderr, "--trust-config") {
		t.Errorf("no warning: %q", stderr)
	}

	_, stdout, _ = runCLI(t, "--check", "--cwd", dir, "--trust-config")
	if got := reportLine(t, stdout, "tools"); got != "1 command tools (lint)" {
		t.Errorf("--trust-config: tools %q", got)
	}
	// A -c file is trusted as before, even when it is the same file.
	_, stdout, _ = runCLI(t, "--check", "--cwd", dir, "-c", filepath.Join(dir, "tinyagent.yaml"))
	if got := reportLine(t, stdout, "tools"); got != "1 command tools (lint)" {
		t.Errorf("-c: tools %q", got)
	}
}

func TestInitSubcommand(t *testing.T) {
	cwdEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "tinyagent.yaml")
	defer func(f func() bool) { stdinIsTerminal = f }(stdinIsTerminal)
	stdinIsTerminal = func() bool { return true }

	// Bare init (at a terminal, with --cwd) writes the file instead of help.
	code, out, stderr := runCLI(t, "init", "--cwd", dir)
	if code != 0 || strings.TrimSpace(out) != path {
		t.Fatalf("init: exit %d, stdout %q, stderr %q", code, out, stderr)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("stat %v, mode %v", err, fi)
	}
	// Refuses to overwrite without --force.
	if code, _, stderr = runCLI(t, "init", "--cwd", dir); code != 2 || !strings.Contains(stderr, "--force") {
		t.Errorf("second init: exit %d, %q", code, stderr)
	}
	os.WriteFile(path, []byte("step_limit: 1\n"), 0o644)
	if code, _, stderr = runCLI(t, "init", "--force", "--cwd", dir); code != 0 {
		t.Fatalf("init --force: exit %d, %q", code, stderr)
	}
	b, _ := os.ReadFile(path)
	fi, _ = os.Stat(path)
	if string(b) != starterConfig || fi.Mode().Perm() != 0o600 {
		t.Errorf("init --force didn't replace the file (mode %v)", fi.Mode())
	}

	// Symlinks are refused, with and without --force, and left alone.
	ldir := t.TempDir()
	target := filepath.Join(t.TempDir(), "target.yaml")
	writeFile(t, target, "step_limit: 1\n")
	link := filepath.Join(ldir, "tinyagent.yaml")
	if err := os.Symlink(target, link); err != nil {
		t.Skip(err)
	}
	for _, args := range [][]string{{"init", "--cwd", ldir}, {"init", "--force", "--cwd", ldir}} {
		if code, _, stderr = runCLI(t, args...); code != 2 {
			t.Errorf("%v on a symlink: exit %d, %q", args, code, stderr)
		}
	}
	if b, _ := os.ReadFile(target); string(b) != "step_limit: 1\n" {
		t.Errorf("symlink target changed: %q", b)
	}
	if fi, _ := os.Lstat(link); fi.Mode()&os.ModeSymlink == 0 {
		t.Error("symlink replaced")
	}

	// --init-force without init is a usage error.
	if code, _, _ = runCLI(t, "--init-force"); code != 2 {
		t.Errorf("--init-force alone: exit %d", code)
	}
}

func TestInitMigrate(t *testing.T) {
	cwdEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "tinyagent.yaml")
	// No user config: an error.
	if code, _, stderr := runCLI(t, "init", "--migrate", "--cwd", dir); code != 2 || !strings.Contains(stderr, "no user config") {
		t.Errorf("migrate without a user config: exit %d, %q", code, stderr)
	}
	user := userConfigPath(t)
	const old = "model: anthropic/claude-sonnet-4-5\nstep_limit: 9\n"
	writeFile(t, user, old)
	if code, _, stderr := runCLI(t, "init", "--migrate", "--cwd", dir); code != 0 {
		t.Fatalf("migrate: exit %d, %q", code, stderr)
	}
	if b, _ := os.ReadFile(path); string(b) != old {
		t.Errorf("migrated %q", b)
	}
	if b, err := os.ReadFile(user); err != nil || string(b) != old {
		t.Errorf("original changed or deleted: %q %v", b, err)
	}
	// Refuses an existing target unless --force.
	writeFile(t, user, "step_limit: 2\n")
	if code, _, _ := runCLI(t, "init", "--migrate", "--cwd", dir); code != 2 {
		t.Errorf("migrate over an existing file: exit %d", code)
	}
	if code, _, stderr := runCLI(t, "init", "--migrate", "--force", "--cwd", dir); code != 0 {
		t.Fatalf("migrate --force: exit %d, %q", code, stderr)
	}
	if b, _ := os.ReadFile(path); string(b) != "step_limit: 2\n" {
		t.Errorf("migrate --force wrote %q", b)
	}
	// The migrated file is now the config.
	_, stdout, _ := runCLI(t, "--check", "--cwd", dir)
	if got := reportLine(t, stdout, "config"); got != path+" (cwd)" {
		t.Errorf("config %q", got)
	}
}
