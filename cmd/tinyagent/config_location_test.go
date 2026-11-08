package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// #391: the project config lives in .tinyagent/tinyagent.yaml. The old
// ./tinyagent.yaml is a hard cut: never loaded, only a hint to move it.
func TestLegacyProjectConfigNotRead(t *testing.T) {
	dir := cwdEnv(t)
	legacy := filepath.Join(dir, "tinyagent.yaml")
	writeFile(t, legacy, "model: openrouter/old/model\nstep_limit: 3\n")
	code, stdout, stderr := runCLI(t, "--check", "--cwd", dir)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if got := reportLine(t, stdout, "config"); got != "none (built-in defaults)" {
		t.Errorf("config %q", got)
	}
	if strings.Contains(stdout, "openrouter/old/model") {
		t.Errorf("legacy file was read:\n%s", stdout)
	}
	// Commands that load the config print the hint (--check keeps stderr quiet).
	_, _, stderr = runCLI(t, "--list-profiles", "--cwd", dir)
	want := "hint: project config now lives in ./.tinyagent/tinyagent.yaml; " + legacy + " is no longer read"
	if !strings.Contains(stderr, want) {
		t.Errorf("stderr %q, want %q", stderr, want)
	}

	// With both present the new file is used and the hint stays.
	cfg := filepath.Join(dir, ".tinyagent", "tinyagent.yaml")
	writeFile(t, cfg, "model: anthropic/claude-sonnet-4-5\n")
	_, stdout, _ = runCLI(t, "--check", "--cwd", dir)
	if got := reportLine(t, stdout, "config"); got != cfg+" (cwd)" {
		t.Errorf("config %q", got)
	}
	if got := reportLine(t, stdout, "model"); got != "anthropic/claude-sonnet-4-5 (from config)" {
		t.Errorf("model %q", got)
	}
	_, _, stderr = runCLI(t, "--list-profiles", "--cwd", dir)
	if !strings.Contains(stderr, want) {
		t.Errorf("both files: stderr %q", stderr)
	}

	// -c names the file: no cwd lookup, no hint.
	_, _, stderr = runCLI(t, "--list-profiles", "--cwd", dir, "-c", cfg)
	if strings.Contains(stderr, "no longer read") {
		t.Errorf("-c: stderr %q", stderr)
	}
}

// init writes .tinyagent/tinyagent.yaml (creating .tinyagent/), leaves an
// old ./tinyagent.yaml alone and hints to move it.
func TestInitWritesTinyagentDir(t *testing.T) {
	cwdEnv(t)
	dir := t.TempDir()
	legacy := filepath.Join(dir, "tinyagent.yaml")
	writeFile(t, legacy, "step_limit: 3\n")
	code, out, stderr := runCLI(t, "init", "--cwd", dir)
	want := filepath.Join(dir, ".tinyagent", "tinyagent.yaml")
	if code != 0 || strings.TrimSpace(out) != want {
		t.Fatalf("init: exit %d, stdout %q, stderr %q", code, out, stderr)
	}
	if b, err := os.ReadFile(want); err != nil || string(b) != starterConfig {
		t.Errorf("starter not written: %v", err)
	}
	if b, _ := os.ReadFile(legacy); string(b) != "step_limit: 3\n" {
		t.Errorf("legacy file changed: %q", b)
	}
	if !strings.Contains(stderr, legacy+" is no longer read") {
		t.Errorf("no move hint: %q", stderr)
	}
	// An existing .tinyagent/ (memory, runs, ...) is reused.
	dir2 := t.TempDir()
	writeFile(t, filepath.Join(dir2, ".tinyagent", "memory", "x.md"), "x\n")
	if code, _, stderr := runCLI(t, "init", "--cwd", dir2); code != 0 || strings.Contains(stderr, "no longer read") {
		t.Errorf("init with existing .tinyagent: exit %d, %q", code, stderr)
	}
}
