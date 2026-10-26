package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListProfilesExplicitConfigSorted(t *testing.T) {
	checkEnv(t)
	cfg := writeConfig(t, "profiles:\n  review:\n    read_only: true\n  ci:\n    step_limit: 80\n  alpha: {}\n")
	code, stdout, stderr := runCLI(t, "--list-profiles", "-c", cfg)
	if code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, stderr)
	}
	if want := "alpha\nci\nreview\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

func TestListProfilesUserConfig(t *testing.T) {
	checkEnv(t)
	xdg := os.Getenv("XDG_CONFIG_HOME")
	p := filepath.Join(xdg, "tinyagent", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("profiles:\n  zebra: {}\n  apple: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runCLI(t, "--list-profiles")
	if code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, stderr)
	}
	if want := "apple\nzebra\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
}

func TestListProfilesEmptyStates(t *testing.T) {
	checkEnv(t)
	// No user config.
	code, stdout, stderr := runCLI(t, "--list-profiles")
	if code != 0 || stdout != "" {
		t.Errorf("no config: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	// No profiles: section.
	cfg := writeConfig(t, "model: openai/gpt-4o\n")
	code, stdout, stderr = runCLI(t, "--list-profiles", "-c", cfg)
	if code != 0 || stdout != "" {
		t.Errorf("no profiles section: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	// Empty profile mapping.
	cfg = writeConfig(t, "profiles: {}\n")
	code, stdout, stderr = runCLI(t, "--list-profiles", "-c", cfg)
	if code != 0 || stdout != "" {
		t.Errorf("empty profiles: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

func TestListProfilesInvalidFiles(t *testing.T) {
	checkEnv(t)
	missing := filepath.Join(t.TempDir(), "missing.yaml")
	code, stdout, stderr := runCLI(t, "--list-profiles", "-c", missing)
	if code != 2 || stdout != "" || !strings.Contains(stderr, missing) {
		t.Errorf("missing: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	malformed := writeConfig(t, "model: [not a string\n")
	code, stdout, stderr = runCLI(t, "--list-profiles", "-c", malformed)
	if code != 2 || stdout != "" || !strings.Contains(stderr, malformed) {
		t.Errorf("malformed: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	invalid := writeConfig(t, "profiles:\n  \"a b\": {}\n")
	code, stdout, stderr = runCLI(t, "--list-profiles", "-c", invalid)
	if code != 2 || stdout != "" || !strings.Contains(stderr, invalid) || !strings.Contains(stderr, "profile name") {
		t.Errorf("invalid name: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
	unknown := writeConfig(t, "profiles:\n  ci:\n    step_limt: 5\n")
	code, stdout, stderr = runCLI(t, "--list-profiles", "-c", unknown)
	if code != 2 || stdout != "" || !strings.Contains(stderr, "step_limt") {
		t.Errorf("unknown key: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

func TestListProfilesModeConflicts(t *testing.T) {
	checkEnv(t)
	for _, args := range [][]string{
		{"-t", "x"},
		{"--profile", "review"},
		{"--check"},
		{"--review"},
		{"--resume", "r.json"},
		{"--rewind", "r.json"},
		{"--acp"},
		{"--transcript", "r.json"},
		{"--completion", "bash"},
		{"--init-config"},
		{"--update"},
	} {
		code, stdout, stderr := runCLI(t, append([]string{"--list-profiles"}, args...)...)
		if code != 2 || stdout != "" || !strings.Contains(stderr, "--list-profiles") {
			t.Errorf("%v: exit %d, stdout %q, stderr %q", args, code, stdout, stderr)
		}
	}
}

func TestListProfilesVersionAndHelpPrecedence(t *testing.T) {
	checkEnv(t)
	code, stdout, _ := runCLI(t, "--list-profiles", "--version")
	if code != 0 || !strings.HasPrefix(stdout, "tinyagent ") {
		t.Errorf("--version: exit %d, stdout %q", code, stdout)
	}
	code, stdout, stderr := runWith(t, "--list-profiles", "--help")
	if code != 0 || !strings.Contains(stdout, "Usage: tinyagent") {
		t.Errorf("--help: exit %d, stdout %q, stderr %q", code, stdout, stderr)
	}
}

// TestListProfilesNoCredentialsNoSideEffects proves discovery needs no key,
// makes no request and starts no hooks or MCP servers: a config with hooks
// and an MCP server whose commands would touch a sentinel file still lists
// its profiles without running either.
func TestListProfilesNoCredentialsNoSideEffects(t *testing.T) {
	hits := checkEnv(t)
	dir := t.TempDir()
	sentinel := filepath.Join(dir, "ran")
	cfg := writeConfig(t, "hooks:\n  pre_tool:\n    - command: /bin/sh\n      args: [\"-c\", \"touch "+sentinel+"\"]\nmcp_servers:\n  - alias: s\n    command: /bin/sh\n    args: [\"-c\", \"touch "+sentinel+"\"]\n    tools: [t]\nprofiles:\n  ci:\n    step_limit: 80\n")
	code, stdout, stderr := runCLI(t, "--list-profiles", "-c", cfg, "--cwd", dir)
	if code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, stderr)
	}
	if stdout != "ci\n" {
		t.Errorf("stdout = %q, want %q", stdout, "ci\n")
	}
	if hits.Load() != 0 {
		t.Errorf("made %d requests", hits.Load())
	}
	if _, err := os.Stat(sentinel); !os.IsNotExist(err) {
		t.Errorf("a hook or MCP command ran (sentinel %s exists)", sentinel)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("--list-profiles wrote into --cwd: %v", entries)
	}
}
