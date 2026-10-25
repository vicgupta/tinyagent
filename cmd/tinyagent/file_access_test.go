package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/trajectory"
)

func TestFileAccessCLI(t *testing.T) {
	dir := t.TempDir()
	cfg := writeConfig(t, "file_access:\n  deny_read: [\".env\"]\n  deny_write: [go.sum, \"vendor/\"]\n")
	newEnvOpenAI(t, "sk-x")
	traj := filepath.Join(t.TempDir(), "run.json")
	code, stdout, stderr := runCLI(t, "-t", "x", "-m", "openai/gpt-4o", "--no-stream", "--cwd", dir, "-c", cfg, "-v", "-o", traj, "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stderr, "file_access: 1 deny_read and 2 deny_write rules") {
		t.Errorf("-v summary missing:\n%s", stderr)
	}
	b, _ := os.ReadFile(traj)
	var tr trajectory.Trajectory
	json.Unmarshal(b, &tr)
	if fa := tr.FileAccess; fa == nil || len(fa.DenyRead) != 1 || len(fa.DenyWrite) != 2 {
		t.Errorf("trajectory file_access = %+v", tr.FileAccess)
	}
	var started map[string]any
	json.Unmarshal([]byte(strings.SplitN(stdout, "\n", 2)[0]), &started)
	if _, ok := started["file_access"].(map[string]any); !ok {
		t.Errorf("run.started = %v", started)
	}

	// Without rules, nothing is printed or recorded.
	newEnvOpenAI(t, "sk-x")
	code, _, stderr = runCLI(t, "-t", "x", "-m", "openai/gpt-4o", "--no-stream", "--cwd", dir, "-v", "-o", traj)
	b, _ = os.ReadFile(traj)
	if code != 0 || strings.Contains(stderr, "file_access") || strings.Contains(string(b), "file_access") {
		t.Errorf("default run: exit %d, stderr %q", code, stderr)
	}
}

func TestFileAccessCLIErrors(t *testing.T) {
	dir := t.TempDir()
	newEnvOpenAI(t, "sk-x")
	code, _, stderr := runCLI(t, "-t", "x", "-m", "openai/gpt-4o", "--no-stream", "--cwd", dir, "-c", writeConfig(t, "file_access:\n  deny_read: [\"!x\"]\n"))
	if code != 2 || !strings.Contains(stderr, "file_access.deny_read") {
		t.Errorf("negation: exit %d, %s", code, stderr)
	}
	newEnvOpenAI(t, "sk-x")
	code, _, stderr = runCLI(t, "-t", "x", "-m", "openai/gpt-4o", "--no-stream", "--cwd", dir, "--tool-output-files", "-c", writeConfig(t, "file_access:\n  deny_read: [\".tinyagent/\"]\n"))
	if code != 2 || !strings.Contains(stderr, "hides the tool output directory") {
		t.Errorf("hidden output dir: exit %d, %s", code, stderr)
	}
}

// A config file inside --cwd is never read, so a repository cannot loosen
// or set its own rules.
func TestFileAccessRepoConfigIgnored(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{"config.yaml", ".tinyagent/config.yaml", "tinyagent/config.yaml", ".tinyagent.yaml"} {
		full := filepath.Join(dir, p)
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte("file_access:\n  deny_read: [\"*\"]\n"), 0o644)
	}
	newEnvOpenAI(t, "sk-x")
	traj := filepath.Join(t.TempDir(), "run.json")
	code, _, stderr := runCLI(t, "-t", "x", "-m", "openai/gpt-4o", "--no-stream", "--cwd", dir, "-v", "-o", traj)
	b, _ := os.ReadFile(traj)
	if code != 0 || strings.Contains(stderr, "file_access") || strings.Contains(string(b), "file_access") {
		t.Errorf("repo config was read: exit %d, %s", code, stderr)
	}
}
