package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/attempts"
)

func TestAttemptsEndToEnd(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("runs bash")
	}
	requests := newEvalOpenAI(t)
	repo := evalRepo(t)
	counter := filepath.Join(t.TempDir(), "count")
	// Fails for the first attempt, passes for the second.
	check := `n=$(cat ` + counter + ` 2>/dev/null || echo 0); n=$((n+1)); echo $n > ` + counter + `; echo "check run $n"; [ $n -ge 2 ]`
	out := filepath.Join(t.TempDir(), "run.json")
	code, stdout, stderr := runCLI(t, "--attempts", "3", "-m", "openai/gpt-4o", "--attempt-check", check,
		"-t", "MODE_PASS", "--cwd", repo, "--worktree=best", "-o", out, "--no-stream")
	if code != 0 {
		t.Fatalf("exit %d\n%s%s", code, stdout, stderr)
	}
	if *requests != 2 {
		t.Fatalf("%d model requests, want 2 (stop at first pass)", *requests)
	}
	for _, want := range []string{"up to 3x a single run", "attempt 1  openai/gpt-4o  Submitted  checks: FAIL", "winner: attempt 2 (branch tinyagent/best-a2)", "git -C "} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("stderr missing %q:\n%s", want, stderr)
		}
	}
	b, err := os.ReadFile(attempts.ReportPath(out))
	if err != nil {
		t.Fatal(err)
	}
	var rep attempts.Report
	if err := json.Unmarshal(b, &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Winner == nil || *rep.Winner != 2 || rep.StopReason != "passed" || len(rep.Attempts) != 2 {
		t.Fatalf("report: %s", b)
	}
	a2 := rep.Attempts[1]
	if a2.Branch != "tinyagent/best-a2" || a2.Checks[0].Output != "check run 2\n" || a2.Steps == 0 {
		t.Fatalf("attempt 2: %+v", a2)
	}
	if _, err := os.Stat(attempts.TrajectoryPath(out, 1)); err != nil {
		t.Fatalf("attempt 1 trajectory: %v", err)
	}
	for _, n := range []string{"best-a1", "best-a2"} {
		if _, err := os.Stat(filepath.Join(repo, worktreeDir, n)); err != nil {
			t.Fatalf("worktree %s not kept: %v", n, err)
		}
	}
	if _, err := os.Stat(filepath.Join(repo, worktreeDir, "best-a3")); err == nil {
		t.Fatal("attempt 3 should not have run")
	}

	// The worktrees now exist, so the same base name is refused up front.
	code, _, stderr = runCLI(t, "--attempts", "2", "--attempt-check", "true", "-t", "MODE_PASS", "--cwd", repo, "--worktree=best")
	if code != 2 || !strings.Contains(stderr, "already exists") {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
}

func TestAttemptsNoPass(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("runs bash")
	}
	newEvalOpenAI(t)
	repo := evalRepo(t)
	code, stdout, stderr := runCLI(t, "--attempts", "2", "--attempt-model", "openai/gpt-4o", "--attempt-model", "openai/gpt-4o-mini",
		"--attempt-check", "exit 7", "-t", "MODE_PASS", "--cwd", repo, "--no-stream")
	if code != 1 || !strings.Contains(stderr, "no attempt passed (exhausted)") || !strings.Contains(stderr, "openai/gpt-4o-mini") {
		t.Fatalf("exit %d\n%s%s", code, stdout, stderr)
	}
}

func TestAttemptsFlagErrors(t *testing.T) {
	newEvalOpenAI(t)
	repo := evalRepo(t)
	empty := t.TempDir()
	noCommit := t.TempDir()
	if out, err := exec.Command("git", "-C", noCommit, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--attempts", "5", "--attempt-check", "true", "-t", "x"}, "between 1 and 4"},
		{[]string{"--attempts", "0", "--attempt-check", "true", "-t", "x"}, "between 1 and 4"},
		{[]string{"--attempts", "2", "-t", "x"}, "at least one --attempt-check"},
		{[]string{"--attempts", "2", "--attempt-model", "a/b", "--attempt-check", "true", "-t", "x"}, "given 1 times"},
		{[]string{"--attempts", "1", "--attempt-model", "a/b", "-m", "c/d", "--attempt-check", "true", "-t", "x"}, "not both"},
		{[]string{"--attempts", "2", "--attempt-check", "true", "--attempt-check-timeout", "0", "-t", "x"}, "--attempt-check-timeout"},
		{[]string{"--attempts", "2", "--attempt-check", "true"}, "needs a task"},
		{[]string{"--attempts", "2", "--attempt-check", "true", "-t", "-"}, "stdin"},
		{[]string{"--attempts", "2", "--attempt-check", "true", "-t", "x", "--snapshots"}, "-snapshots"},
		{[]string{"--attempts", "2", "--attempt-check", "true", "-t", "x", "--resume", "r.json"}, "-resume"},
		{[]string{"--attempts", "2", "--attempt-check", "true", "-t", "x", "--cwd", repo, "--worktree=" + strings.Repeat("a", 62)}, "too long"},
		{[]string{"--attempts", "2", "--attempt-check", "true", "-t", "x", "--cwd", empty}, "not inside a Git work tree"},
		{[]string{"--attempts", "2", "--attempt-check", "true", "-t", "x", "--cwd", noCommit}, "no commits"},
	} {
		code, _, stderr := runCLI(t, c.args...)
		if code != 2 || !strings.Contains(stderr, c.want) {
			t.Errorf("%v: exit %d, want 2 with %q\n%s", c.args, code, c.want, stderr)
		}
	}
	if _, err := os.Stat(filepath.Join(repo, worktreeDir)); err == nil {
		t.Fatal("a usage error created a worktree")
	}
}
