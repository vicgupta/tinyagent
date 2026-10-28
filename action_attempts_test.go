package tinyagent_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitWorkspace returns a Git repository with one commit.
func gitWorkspace(t *testing.T) string {
	t.Helper()
	ws := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "init"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = ws
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git: %v\n%s", err, out)
		}
	}
	return ws
}

func TestActionAttempts(t *testing.T) {
	bin := buildTinyagent(t)
	ws := gitWorkspace(t)
	pwned := t.TempDir()
	task := "fix it $(touch " + pwned + "/a)\nsecond line"
	stub := newStubModel(t, submitReply("first try"))
	r := runAction(t, actionEnv{workspace: ws, env: stub.env()}, map[string]string{
		"tinyagent-path": bin, "task": task, "model": "openai/gpt-4o", "args": "--no-stream",
		"attempts": "2", "attempt-checks": "true\n\ntest -d .", "job-summary": "true",
	})
	if r.failed != "" {
		t.Fatalf("step %s failed:\n%s", r.failed, r.log)
	}
	if entries, _ := os.ReadDir(pwned); len(entries) != 0 {
		t.Fatalf("the task ran shell commands: %v", entries)
	}
	if reqs := stub.requests(); len(reqs) != 1 || !strings.Contains(reqs[0], "second line") {
		t.Fatalf("requests %v", reqs)
	}
	want := map[string]string{"status": "Submitted", "submission": "first try", "steps": "1", "exit-code": "0", "attempts-winner": "1"}
	for k, v := range want {
		if r.outputs[k] != v {
			t.Errorf("output %s = %q, want %q", k, r.outputs[k], v)
		}
	}
	if !strings.HasPrefix(r.outputs["attempts-winner-branch"], "tinyagent/") || !strings.HasSuffix(r.outputs["attempts-winner-branch"], "-a1") {
		t.Errorf("winner branch %q", r.outputs["attempts-winner-branch"])
	}
	if r.outputs["cost-usd"] == "" {
		t.Error("cost-usd unset")
	}
	for _, k := range []string{"trajectory", "attempts-report", "events"} {
		if st, err := os.Stat(r.outputs[k]); err != nil || st.Size() == 0 {
			t.Errorf("output %s = %q: %v", k, r.outputs[k], err)
		}
	}
	if filepath.Base(r.outputs["trajectory"]) != "trajectory.a1.json" {
		t.Errorf("trajectory %q is not the winner's", r.outputs["trajectory"])
	}
	if !strings.Contains(r.summary, "first try") {
		t.Errorf("job summary lacks the winner's transcript:\n%.500s", r.summary)
	}
}

func TestActionAttemptsNoWinner(t *testing.T) {
	bin := buildTinyagent(t)
	ws := gitWorkspace(t)
	stub := newStubModel(t, submitReply("one"), submitReply("two"))
	r := runAction(t, actionEnv{workspace: ws, env: stub.env()}, map[string]string{
		"tinyagent-path": bin, "task": "x", "model": "openai/gpt-4o", "args": "--no-stream",
		"attempts": "2", "attempt-checks": "false", "fail-on-error": "false",
	})
	if r.failed != "" || r.outputs["exit-code"] != "1" {
		t.Fatalf("failed %q, outputs %v\n%s", r.failed, r.outputs, r.log)
	}
	for _, k := range []string{"status", "submission", "attempts-winner", "attempts-winner-branch", "trajectory"} {
		if r.outputs[k] != "" {
			t.Errorf("output %s = %q, want empty", k, r.outputs[k])
		}
	}
	if len(stub.requests()) != 2 || r.outputs["attempts-report"] == "" {
		t.Fatalf("%d requests, outputs %v", len(stub.requests()), r.outputs)
	}
}

func TestActionAttemptsInputErrors(t *testing.T) {
	bin := buildTinyagent(t)
	for name, inputs := range map[string]map[string]string{
		"not a number":         {"task": "x", "attempts": "two"},
		"with review":          {"attempts": "2", "review-base": "HEAD"},
		"with read-only":       {"task": "x", "attempts": "2", "read-only": "true"},
		"checks need attempts": {"task": "x", "attempt-checks": "true"},
		"no task":              {"attempts": "2"},
	} {
		t.Run(name, func(t *testing.T) {
			stub := newStubModel(t)
			inputs["tinyagent-path"] = bin
			r := runAction(t, actionEnv{env: stub.env()}, inputs)
			if r.failed == "" || len(stub.requests()) != 0 || !strings.Contains(r.log, "::error::tinyagent action:") {
				t.Fatalf("failed %q, %d requests\n%s", r.failed, len(stub.requests()), r.log)
			}
		})
	}
}
