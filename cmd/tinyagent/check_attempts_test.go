package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckAttemptsPlan(t *testing.T) {
	hits := checkEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "k")
	repo := evalRepo(t)
	top, err := canonicalRoot(repo)
	if err != nil {
		t.Fatal(err)
	}
	const secretCheck = "go test ./... # token-sentinel-7788"
	base := []string{"--check", "-m", "anthropic/claude-sonnet-4-5", "--cwd", repo}

	// No attempts flags and no config section: no line.
	code, stdout, stderr := runCLI(t, base...)
	if code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, stderr)
	}
	if strings.Contains(stdout, "attempts:") {
		t.Errorf("attempts line without --attempts or config:\n%s", stdout)
	}

	// Flags: the plan, with check count and timeout but never the command.
	code, stdout, stderr = runCLI(t, append(base, "--attempts", "3", "--attempt-check", secretCheck,
		"--attempt-check-timeout", "90", "--worktree=best")...)
	if code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, stderr)
	}
	want := "up to 3 runs (anthropic/claude-sonnet-4-5, anthropic/claude-sonnet-4-5, anthropic/claude-sonnet-4-5; from -m), " +
		"1 check (from --attempt-check, timeout 90s), stop_on_pass true, keep all, worktrees " +
		filepath.Join(top, worktreeDir, "best") + "-a<N>; cost, token and time limits are shared by all attempts, which can cost up to 3x a single run"
	if got := reportLine(t, stdout, "attempts"); got != want {
		t.Errorf("attempts line\n got %q\nwant %q", got, want)
	}
	if strings.Contains(stdout+stderr, "token-sentinel-7788") {
		t.Errorf("--check printed a check command:\n%s%s", stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(repo, worktreeDir)); err == nil {
		t.Error("--check created a worktree")
	}

	// Config checks, stop_on_pass, keep and per-attempt models; a generated
	// worktree name is shown as <generated>.
	t.Setenv("OPENAI_API_KEY", "k2")
	cfg := writeConfig(t, "attempts:\n  checks:\n    - command: \"true\"\n    - command: \"make test\"\n      timeout_seconds: 30\n  stop_on_pass: false\n  keep: failed\n")
	code, stdout, stderr = runCLI(t, "--check", "-c", cfg, "--cwd", repo, "--attempts", "2",
		"--attempt-model", "anthropic/claude-sonnet-4-5", "--attempt-model", "openai/gpt-4o")
	if code != 0 {
		t.Fatalf("config: exit %d; stderr:\n%s", code, stderr)
	}
	want = "up to 2 runs (anthropic/claude-sonnet-4-5, openai/gpt-4o; from --attempt-model), " +
		"2 checks (from config, timeout 600s, 30s), stop_on_pass false, keep failed, worktrees " +
		filepath.Join(top, worktreeDir, "<generated>") + "-a<N>; cost, token and time limits are shared by all attempts, which can cost up to 2x a single run"
	if got := reportLine(t, stdout, "attempts"); got != want {
		t.Errorf("config attempts line\n got %q\nwant %q", got, want)
	}

	// Config section without --attempts: reported as off.
	code, stdout, stderr = runCLI(t, append(base, "-c", cfg)...)
	if code != 0 {
		t.Fatalf("config only: exit %d; stderr:\n%s", code, stderr)
	}
	if got, want := reportLine(t, stdout, "attempts"), "off (config sets 2 checks, stop_on_pass false, keep failed; used only with --attempts)"; got != want {
		t.Errorf("config-only line %q, want %q", got, want)
	}

	if hits.Load() != 0 {
		t.Errorf("--check made %d requests", hits.Load())
	}
}

func TestCheckAttemptsErrors(t *testing.T) {
	checkEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "k")
	repo := evalRepo(t)
	empty := t.TempDir()
	base := []string{"--check", "-m", "anthropic/claude-sonnet-4-5", "--cwd", repo}
	if err := os.MkdirAll(filepath.Join(repo, worktreeDir, "taken-a2"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--attempts", "5", "--attempt-check", "true"}, "between 1 and 4"},
		{[]string{"--attempts", "2"}, "at least one --attempt-check"},
		{[]string{"--attempts", "2", "--attempt-check", "true", "--attempt-model", "anthropic/claude-sonnet-4-5"}, "given 1 times"},
		{[]string{"--attempts", "2", "--attempt-check", "true", "--attempt-check-timeout", "0"}, "--attempt-check-timeout"},
		{[]string{"--attempts", "2", "--attempt-check", "true", "--worktree=taken"}, "already exists"},
		{[]string{"--attempts", "2", "--attempt-check", "true", "-t", "x"}, "--task"},
		{[]string{"--attempts", "1", "--attempt-check", "true", "--cwd", empty}, "not inside a Git work tree"},
	} {
		code, _, stderr := runCLI(t, append(append([]string{}, base...), c.args...)...)
		if code != 2 || !strings.Contains(stderr, c.want) {
			t.Errorf("%v: exit %d, want 2 with %q\n%s", c.args, code, c.want, stderr)
		}
	}
	// An attempt model without credentials fails the way a run would.
	code, _, stderr := runCLI(t, "--check", "--cwd", repo, "--attempts", "2", "--attempt-check", "true",
		"--attempt-model", "anthropic/claude-sonnet-4-5", "--attempt-model", "openrouter/x/y")
	if code != 2 || !strings.Contains(stderr, "attempt 2 model") {
		t.Errorf("missing attempt credentials: exit %d\n%s", code, stderr)
	}
	// With --attempt-model, the default model's credentials aren't needed
	// and the model line names attempt 1's model.
	code, stdout, stderr := runCLI(t, "--check", "--cwd", repo, "--attempts", "1", "--attempt-check", "true",
		"--attempt-model", "anthropic/claude-sonnet-4-5")
	if code != 0 || reportLine(t, stdout, "model") != "anthropic/claude-sonnet-4-5 (attempt 1, from --attempt-model)" {
		t.Errorf("--attempt-model without default credentials: exit %d\n%s%s", code, stdout, stderr)
	}
	// Without --attempts, --worktree is still rejected by --check.
	if code, _, stderr := runCLI(t, append(base, "--worktree=x")...); code != 2 || !strings.Contains(stderr, "--worktree") {
		t.Errorf("--check --worktree: exit %d\n%s", code, stderr)
	}
}
