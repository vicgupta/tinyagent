package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/attempts"
)

// TestAttemptsPrevalidatesModels checks that every attempt's model is
// validated before the first attempt runs: no model request is made and no
// worktree is created when a later attempt's model can't run.
func TestAttemptsPrevalidatesModels(t *testing.T) {
	requests := newEvalOpenAI(t)
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("GW_KEY", "")
	repo := evalRepo(t)
	gw := writeConfig(t, "providers:\n  - id: gw\n    api: chat\n    base_url: http://127.0.0.1:1\n    api_key_env: GW_KEY\n")
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--attempt-model", "openai/gpt-4o", "--attempt-model", "anthropic/claude-sonnet-4-5"},
			`attempt 2 model "anthropic/claude-sonnet-4-5": ANTHROPIC_API_KEY is not set`},
		{[]string{"--attempt-model", "nope/x", "--attempt-model", "openai/gpt-4o"}, `attempt 1 model "nope/x"`},
		{[]string{"-c", gw, "--attempt-model", "openai/gpt-4o", "--attempt-model", "gw/m"},
			`attempt 2 model "gw/m": provider "gw": api_key_env GW_KEY is unset or empty`},
		{[]string{"-m", "anthropic/claude-sonnet-4-5"}, `attempt 1 model "anthropic/claude-sonnet-4-5": ANTHROPIC_API_KEY is not set`},
	} {
		args := append([]string{"--attempts", "2", "--attempt-check", "true", "-t", "MODE_PASS", "--cwd", repo, "--no-stream"}, c.args...)
		code, _, stderr := runCLI(t, args...)
		if code != 2 || !strings.Contains(stderr, c.want) {
			t.Errorf("%v: exit %d, want 2 with %q\n%s", c.args, code, c.want, stderr)
		}
		if strings.Contains(stderr, "attempts: up to") {
			t.Errorf("%v: attempts started before the model check:\n%s", c.args, stderr)
		}
	}
	if *requests != 0 {
		t.Fatalf("%d model requests, want 0", *requests)
	}
	if _, err := os.Stat(filepath.Join(repo, worktreeDir)); err == nil {
		t.Fatal("a failed model check created a worktree")
	}
}

// TestAttemptsUseConfigModel checks that without -m or --attempt-model every
// attempt uses the model a single run would: the config's model.
func TestAttemptsUseConfigModel(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("runs bash")
	}
	newEvalOpenAI(t)
	repo := evalRepo(t)
	cfg := writeConfig(t, "model: openai/gpt-4o-mini\n")
	out := filepath.Join(t.TempDir(), "run.json")
	code, stdout, stderr := runCLI(t, "-c", cfg, "--attempts", "1", "--attempt-check", "true",
		"-t", "MODE_PASS", "--cwd", repo, "-o", out, "--no-stream")
	if code != 0 {
		t.Fatalf("exit %d\n%s%s", code, stdout, stderr)
	}
	b, err := os.ReadFile(attempts.ReportPath(out))
	if err != nil {
		t.Fatal(err)
	}
	var rep attempts.Report
	if err := json.Unmarshal(b, &rep); err != nil {
		t.Fatal(err)
	}
	if len(rep.Attempts) != 1 || rep.Attempts[0].Model != "openai/gpt-4o-mini" {
		t.Fatalf("report: %s", b)
	}
}

// TestAttemptsDirtyWarningOnce checks that the uncommitted-changes warning
// is printed once for the invocation, not once per attempt.
func TestAttemptsDirtyWarningOnce(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("runs bash")
	}
	newEvalOpenAI(t)
	repo := evalRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := runCLI(t, "--attempts", "2", "--attempt-check", "exit 7",
		"-t", "MODE_PASS", "--cwd", repo, "--no-stream")
	if code != 1 {
		t.Fatalf("exit %d\n%s%s", code, stdout, stderr)
	}
	if n := strings.Count(stderr, "uncommitted changes"); n != 1 {
		t.Fatalf("dirty warning printed %d times:\n%s", n, stderr)
	}
	if !strings.Contains(stderr, dirtyWorktreeWarning+" (this applies to every attempt)") {
		t.Fatalf("stderr lacks the invocation warning:\n%s", stderr)
	}
	if !strings.Contains(stderr, "worktree: ") {
		t.Fatalf("children's other stderr lines were dropped:\n%s", stderr)
	}

	// A clean checkout gets no warning.
	repo = evalRepo(t)
	code, _, stderr = runCLI(t, "--attempts", "1", "--attempt-check", "true", "-t", "MODE_PASS", "--cwd", repo, "--no-stream")
	if code != 0 || strings.Contains(stderr, "uncommitted changes") {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
}

func TestLineDropper(t *testing.T) {
	line := "warn: x\n"
	for _, c := range []struct{ in, want string }{
		{"warn: x\n", ""},
		{"a\nwarn: x\nb\n", "a\nb\n"},
		{"warn: y\nwarn: x\n", "warn: y\n"},
		{"say warn: x\n", "say warn: x\n"},
		{"warn: x", "warn: x"},
		{"warn\n", "warn\n"},
		{"warn: x\nwarn: x\nend", "end"},
		{"", ""},
	} {
		// Whole, then one byte at a time.
		for _, step := range []int{len(c.in) + 1, 1} {
			var buf bytes.Buffer
			d := &lineDropper{w: &buf, line: line, atStart: true}
			for i := 0; i < len(c.in); i += step {
				j := min(i+step, len(c.in))
				if n, err := d.Write([]byte(c.in[i:j])); err != nil || n != j-i {
					t.Fatalf("Write: %d, %v", n, err)
				}
			}
			if err := d.Flush(); err != nil {
				t.Fatal(err)
			}
			if buf.String() != c.want {
				t.Errorf("%q (step %d): got %q, want %q", c.in, step, buf.String(), c.want)
			}
		}
	}
}
