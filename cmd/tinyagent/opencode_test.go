//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeOpenCodeOnPath puts a fake opencode first on PATH.
func fakeOpenCodeOnPath(t *testing.T) {
	t.Helper()
	bin := t.TempDir()
	script := `#!/bin/sh
if [ "$1" = "--version" ]; then echo 9.9.9; exit 0; fi
echo '{"type":"text","sessionID":"ses_cli","part":{"type":"text","messageID":"m1","text":"opencode says done"}}'
echo '{"type":"step_finish","sessionID":"ses_cli","part":{"type":"step-finish","messageID":"m1","cost":0.1,"tokens":{"input":5,"output":2,"reasoning":0,"cache":{"read":0,"write":0}}}}'
`
	if err := os.WriteFile(filepath.Join(bin, "opencode"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestCheckShowsOpenCodeVersion(t *testing.T) {
	fakeOpenCodeOnPath(t)
	code, stdout, stderr := runCLI(t, "--check", "-m", "opencode/anthropic/claude-sonnet-4-5", "--cwd", t.TempDir())
	if code != 0 || !strings.Contains(stdout+stderr, "opencode, runs ") || !strings.Contains(stdout+stderr, "9.9.9") {
		t.Fatalf("code %d stdout %q stderr %q", code, stdout, stderr)
	}
}

func TestCheckOpenCodeMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	code, _, stderr := runCLI(t, "--check", "-m", "opencode/default")
	if code == 0 || !strings.Contains(stderr, "not found on PATH") || strings.Count(stderr, "hint:") != 1 || !strings.Contains(stderr, "opencode.ai") {
		t.Fatalf("code %d stderr %q", code, stderr)
	}
}

func TestOpenCodeRunJSONEvents(t *testing.T) {
	fakeOpenCodeOnPath(t)
	code, stdout, stderr := runCLI(t, "-t", "say done", "-m", "opencode/default", "--cwd", t.TempDir(), "--json")
	if code != 0 || !strings.Contains(stdout, "opencode says done") || !strings.Contains(stdout, `"type"`) {
		t.Fatalf("code %d stdout %q stderr %q", code, stdout, stderr)
	}
}
