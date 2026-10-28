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

// TestAttemptsConfigRunAllKeepNone takes checks, stop_on_pass and keep from
// the config: all three attempts run, attempt 2 (the first to pass) wins,
// and the other two worktrees and branches are removed.
func TestAttemptsConfigRunAllKeepNone(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("runs bash")
	}
	requests := newEvalOpenAI(t)
	repo := evalRepo(t)
	counter := filepath.Join(t.TempDir(), "count")
	check := `n=$(cat ` + counter + ` 2>/dev/null || echo 0); n=$((n+1)); echo $n > ` + counter + `; [ $n -ge 2 ]`
	b, _ := json.Marshal(check)
	cfg := writeConfig(t, "attempts:\n  checks:\n    - command: "+string(b)+"\n      timeout_seconds: 60\n  stop_on_pass: false\n  keep: none\n")
	// An unrelated branch must survive cleanup.
	if out, err := exec.Command("git", "-C", repo, "branch", "tinyagent/other").CombinedOutput(); err != nil {
		t.Fatalf("git branch: %v %s", err, out)
	}
	out := filepath.Join(t.TempDir(), "run.json")
	code, stdout, stderr := runCLI(t, "-c", cfg, "--attempts", "3", "-m", "openai/gpt-4o",
		"-t", "MODE_PASS", "--cwd", repo, "--worktree=cfg", "-o", out, "--no-stream")
	if code != 0 {
		t.Fatalf("exit %d\n%s%s", code, stdout, stderr)
	}
	if *requests != 3 {
		t.Fatalf("%d model requests, want 3 (stop_on_pass: false)", *requests)
	}
	if !strings.Contains(stderr, "stop_on_pass false, keep none") || !strings.Contains(stderr, "winner: attempt 2") {
		t.Fatalf("stderr:\n%s", stderr)
	}
	rb, err := os.ReadFile(attempts.ReportPath(out))
	if err != nil {
		t.Fatal(err)
	}
	var rep attempts.Report
	if err := json.Unmarshal(rb, &rep); err != nil {
		t.Fatal(err)
	}
	if rep.Winner == nil || *rep.Winner != 2 || rep.StopReason != attempts.StopCompleted || rep.Keep != "none" || rep.StopOnPass {
		t.Fatalf("report: %s", rb)
	}
	if !rep.Attempts[0].Removed || rep.Attempts[1].Removed || !rep.Attempts[2].Removed {
		t.Fatalf("removed flags: %s", rb)
	}
	for n, want := range map[string]bool{"cfg-a1": false, "cfg-a2": true, "cfg-a3": false} {
		_, err := os.Stat(filepath.Join(repo, worktreeDir, n))
		if (err == nil) != want {
			t.Fatalf("worktree %s exists=%t, want %t", n, err == nil, want)
		}
		_, err = git("-C", repo, "rev-parse", "--verify", "--quiet", "refs/heads/tinyagent/"+n)
		if (err == nil) != want {
			t.Fatalf("branch tinyagent/%s exists=%t, want %t", n, err == nil, want)
		}
	}
	if _, err := git("-C", repo, "rev-parse", "--verify", "--quiet", "refs/heads/tinyagent/other"); err != nil {
		t.Fatal("cleanup removed an unrelated branch")
	}
}

func TestAttemptsConfigErrors(t *testing.T) {
	newEvalOpenAI(t)
	repo := evalRepo(t)
	withChecks := writeConfig(t, "attempts:\n  checks:\n    - command: \"true\"\n")
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"-c", writeConfig(t, "attempts:\n  keep: maybe\n"), "--attempts", "2", "--attempt-check", "true", "-t", "x", "--cwd", repo}, "attempts.keep"},
		{[]string{"-c", writeConfig(t, "attempts:\n  stop_on_pas: false\n"), "--attempts", "2", "--attempt-check", "true", "-t", "x", "--cwd", repo}, "stop_on_pas"},
		{[]string{"-c", withChecks, "--attempts", "2", "--attempt-check-timeout", "5", "-t", "x", "--cwd", repo}, "timeout_seconds"},
		{[]string{"-c", writeConfig(t, "model: openai/gpt-4o\n"), "--attempts", "2", "-t", "x", "--cwd", repo}, "attempts.checks"},
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
