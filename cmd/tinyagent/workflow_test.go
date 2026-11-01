package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/harness"
)

const wfSkill = "# Skill: plan-tasks\n> Intent: Break a spec into small ordered tasks.\n\n**Inputs**\n- spec\n\n**Success check:** every task has a verification step.\n"

func writeWorkflowFiles(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "skills"), 0o755)
	os.WriteFile(filepath.Join(dir, "skills", "plan-tasks.md"), []byte(wfSkill), 0o644)
	p := filepath.Join(dir, "wf.yaml")
	os.WriteFile(p, []byte("version: 1\nworkflows:\n  plan:\n    intent: plan a feature\n    stages:\n      - skill: plan-tasks\n"), 0o644)
	return p
}

func fakeWorkflowAgent(t *testing.T, submission string, calls *[][]string) {
	t.Helper()
	old := runWorkflowAgent
	t.Cleanup(func() { runWorkflowAgent = old })
	runWorkflowAgent = func() (harness.RunAgent, error) {
		return func(_ context.Context, args []string, _ io.Writer) error {
			*calls = append(*calls, args)
			for i := range args {
				if args[i] == "-o" {
					b, _ := json.Marshal(map[string]any{"exit_status": "Submitted", "submission": submission})
					return os.WriteFile(args[i+1], b, 0o600)
				}
			}
			t.Fatal("no -o")
			return nil
		}, nil
	}
}

func TestWorkflowCLI(t *testing.T) {
	wf := writeWorkflowFiles(t)
	var calls [][]string
	fakeWorkflowAgent(t, "1. do it\nSUCCESS CHECK: PASS", &calls)
	out := filepath.Join(t.TempDir(), "run.json")
	code, stdout, stderr := runCLI(t, "--workflow", wf, "-t", "plan the export feature", "-o", out, "--cwd", t.TempDir(), "--sandbox")
	if code != 0 {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "1. do it") {
		t.Errorf("stdout = %q", stdout)
	}
	if len(calls) != 1 || !strings.Contains(strings.Join(calls[0], " "), "--sandbox") {
		t.Fatalf("calls = %q", calls)
	}
	for _, a := range calls[0] {
		if a == "--workflow" || a == wf {
			t.Errorf("forwarded harness flag: %q", calls[0])
		}
	}
	var res harness.Result
	b, err := os.ReadFile(out)
	if err != nil || json.Unmarshal(b, &res) != nil {
		t.Fatalf("read -o: %v", err)
	}
	if res.Status != harness.StatusPassed || res.Route.How != "intent" || res.Attempts[0].Trajectory == "" {
		t.Errorf("result = %+v", res)
	}
	if _, err := os.Stat(res.Attempts[0].Trajectory); err != nil {
		t.Errorf("stage trajectory not kept: %v", err)
	}
}

func TestWorkflowCLIFailureAndJSON(t *testing.T) {
	wf := writeWorkflowFiles(t)
	var calls [][]string
	fakeWorkflowAgent(t, "SUCCESS CHECK: FAIL - nothing planned", &calls)
	out := filepath.Join(t.TempDir(), "run.json")
	code, stdout, stderr := runCLI(t, "--workflow", wf, "--workflow-name", "plan-tasks", "-t", "x", "--json", "-o", out, "--cwd", t.TempDir())
	if code != harness.ExitBlocked {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr)
	}
	if len(calls) != 2 {
		t.Errorf("calls = %d, want 2 (default one retry)", len(calls))
	}
	if !strings.Contains(stderr, "workflow plan-tasks Blocked at stage plan-tasks: nothing planned") || !strings.Contains(stderr, "re-run the same tinyagent --workflow command") {
		t.Errorf("stderr = %q", stderr)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 6 || !strings.Contains(lines[0], `"workflow.started"`) || !strings.Contains(lines[5], `"workflow.finished"`) || !strings.Contains(lines[5], `"status":"Blocked"`) {
		t.Errorf("events:\n%s", stdout)
	}
	var res harness.Result
	b, err := os.ReadFile(out)
	if err != nil || json.Unmarshal(b, &res) != nil {
		t.Fatalf("read -o: %v", err)
	}
	if res.Status != harness.StatusBlocked || res.FailedStage != "plan-tasks" || res.Reason != "nothing planned" {
		t.Errorf("result = %+v", res)
	}
}

func TestWorkflowCLIUsageErrors(t *testing.T) {
	wf := writeWorkflowFiles(t)
	var calls [][]string
	fakeWorkflowAgent(t, "", &calls)
	bad := filepath.Join(t.TempDir(), "bad.yaml")
	os.WriteFile(bad, []byte("version: 9\n"), 0o644)
	for _, args := range [][]string{
		{"--workflow", wf},
		{"--workflow", wf, "-t", "x", "--resume", "abc"},
		{"--workflow", wf, "-t", "bake a cake"},
		{"--workflow", wf, "-t", "x", "--workflow-name", "nope"},
		{"--workflow", bad, "-t", "x"},
		{"--workflow-name", "plan", "-t", "x"},
	} {
		if code, _, _ := runCLI(t, args...); code != 2 {
			t.Errorf("%q: exit %d, want 2", args, code)
		}
	}
	if len(calls) != 0 {
		t.Errorf("model run despite usage error: %q", calls)
	}
}

func TestWorkflowObserve(t *testing.T) {
	wf := writeWorkflowFiles(t)
	var calls [][]string
	fakeWorkflowAgent(t, "SUCCESS CHECK: FAIL - nothing planned", &calls)
	cwd := t.TempDir()
	code, stdout, stderr := runCLI(t, "--workflow", wf, "--workflow-name", "plan-tasks", "-t", "x", "--json", "--workflow-observe", "--cwd", cwd)
	if code != harness.ExitBlocked {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr)
	}
	for _, a := range calls[0] {
		if a == "--workflow-observe" {
			t.Errorf("forwarded --workflow-observe: %q", calls[0])
		}
	}
	b, err := os.ReadFile(filepath.Join(cwd, ".tinyagent", "observer", "proposals.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	var p map[string]any
	if len(lines) != 1 || json.Unmarshal([]byte(lines[0]), &p) != nil {
		t.Fatalf("proposals:\n%s", b)
	}
	if p["stage"] != "plan-tasks" || !strings.HasPrefix(p["suggested_command"].(string), "tinyagent ratchet add ") {
		t.Errorf("proposal = %v", p)
	}
	if !strings.Contains(stdout, `"observer.proposed"`) || !strings.Contains(stderr, "tinyagent ratchet add ") {
		t.Errorf("stdout:\n%s\nstderr:\n%s", stdout, stderr)
	}

	// The same failure again: the ledger count goes up and it's flagged.
	code, stdout, stderr = runCLI(t, "--workflow", wf, "--workflow-name", "plan-tasks", "-t", "x", "--json", "--workflow-observe", "--cwd", cwd)
	if code != harness.ExitBlocked || !strings.Contains(stdout, `"observer.recurrence"`) || !strings.Contains(stdout, `"count":2`) || !strings.Contains(stderr, "gate did not hold") {
		t.Errorf("exit %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}
	if b2, _ := os.ReadFile(filepath.Join(cwd, ".tinyagent", "observer", "proposals.jsonl")); string(b2) != string(b) {
		t.Errorf("recurrence wrote another proposal:\n%s", b2)
	}
}

func TestWorkflowObservePassedAndOff(t *testing.T) {
	wf := writeWorkflowFiles(t)
	var calls [][]string
	fakeWorkflowAgent(t, "1. do it\nSUCCESS CHECK: PASS", &calls)
	cwd := t.TempDir()
	if code, _, stderr := runCLI(t, "--workflow", wf, "-t", "plan the export feature", "--workflow-observe", "--cwd", cwd); code != 0 {
		t.Fatalf("exit %d, stderr:\n%s", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(cwd, ".tinyagent")); !os.IsNotExist(err) {
		t.Errorf("passing run wrote observer files: %v", err)
	}

	fakeWorkflowAgent(t, "SUCCESS CHECK: FAIL - nothing planned", &calls)
	if code, _, _ := runCLI(t, "--workflow", wf, "--workflow-name", "plan-tasks", "-t", "x", "--cwd", cwd); code != harness.ExitBlocked {
		t.Fatalf("exit %d", code)
	}
	if _, err := os.Stat(filepath.Join(cwd, ".tinyagent")); !os.IsNotExist(err) {
		t.Errorf("run without --workflow-observe wrote observer files: %v", err)
	}
	if code, _, _ := runCLI(t, "--workflow-observe", "-t", "x"); code != 2 {
		t.Errorf("--workflow-observe without --workflow: exit %d, want 2", code)
	}
}
