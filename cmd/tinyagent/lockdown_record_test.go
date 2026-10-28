package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vicgupta/tinyagent/internal/trajectory"
)

func lockdownEvents(t *testing.T, stdout string) map[string][]map[string]any {
	t.Helper()
	out := map[string][]map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("event %q: %v", line, err)
		}
		k, _ := ev["type"].(string)
		out[k] = append(out[k], ev)
	}
	return out
}

func TestLockdownTaintSticksAcrossResume(t *testing.T) {
	dir, requests := lockdownEnv(t)
	first := filepath.Join(t.TempDir(), "run.json")
	var code int
	stdout, stderr := captureStdoutStderr(t, func() {
		code, _, _ = runWith(t, "-t", "issue body", "-m", "openai/gpt-4o", "--no-stream", "--json", "--cwd", dir, "--lockdown", "--untrusted-task", "-o", first)
	})
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	ev := lockdownEvents(t, stdout)
	info, _ := ev["run.started"][0]["lockdown"].(map[string]any)
	if info["enabled"] != true || info["tainted"] != true {
		t.Errorf("run.started lockdown = %v", info)
	}
	if res := ev["result"]; len(res) != 1 || res[0]["tainted"] != true || res[0]["lockdown_denials"] != float64(0) {
		t.Errorf("result = %v", res)
	}
	tr, err := trajectory.Load(first)
	if err != nil {
		t.Fatal(err)
	}
	if !tr.LockdownEnabled || !tr.Tainted || len(tr.TaintSources) != 1 || tr.TaintSources[0].Source != "untrusted_task" {
		t.Fatalf("trajectory lockdown = %v %v %+v", tr.LockdownEnabled, tr.Tainted, tr.TaintSources)
	}

	// A follow-up without lockdown is refused before any model request.
	before := requests.Load()
	stderr = captureStderr(t, func() {
		code, _, _ = runWith(t, "--resume", first, "-t", "more", "-m", "openai/gpt-4o", "--no-stream", "--cwd", dir, "-o", filepath.Join(t.TempDir(), "x.json"))
	})
	if code != 2 || requests.Load() != before || !strings.Contains(stderr, "checkpoint is tainted by untrusted content (untrusted_task)") {
		t.Errorf("unlocked resume: exit %d, requests %d, stderr %q", code, requests.Load()-before, stderr)
	}

	// With lockdown the run starts tainted from the checkpoint.
	second := filepath.Join(t.TempDir(), "run2.json")
	stdout, stderr = captureStdoutStderr(t, func() {
		code, _, _ = runWith(t, "--resume", first, "-t", "more", "-m", "openai/gpt-4o", "--no-stream", "--json", "--cwd", dir, "--lockdown", "-o", second)
	})
	if code != 0 || requests.Load() != before+1 {
		t.Fatalf("locked resume: exit %d, stderr %q", code, stderr)
	}
	if !strings.Contains(stderr, "lockdown: run starts tainted (untrusted_task, from the checkpoint)") {
		t.Errorf("stderr = %q", stderr)
	}
	ev = lockdownEvents(t, stdout)
	if taints := ev["taint"]; len(taints) != 1 || taints[0]["resumed"] != true || taints[0]["source"] != "untrusted_task" {
		t.Errorf("taint events = %v", taints)
	}
	tr2, err := trajectory.Load(second)
	if err != nil {
		t.Fatal(err)
	}
	if !tr2.Tainted || len(tr2.TaintSources) != 1 || !tr2.TaintSources[0].Resumed {
		t.Errorf("resumed trajectory = %v %+v", tr2.Tainted, tr2.TaintSources)
	}
}

func TestResumeUntaintedCheckpointWithoutLockdown(t *testing.T) {
	dir, requests := lockdownEnv(t)
	first := filepath.Join(t.TempDir(), "run.json")
	var code int
	captureStderr(t, func() {
		code, _, _ = runWith(t, "-t", "x", "-m", "openai/gpt-4o", "--no-stream", "--cwd", dir, "--lockdown", "-o", first)
	})
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if b, _ := os.ReadFile(first); strings.Contains(string(b), `"tainted"`) {
		t.Error("untainted checkpoint marked tainted")
	}
	captureStderr(t, func() {
		code, _, _ = runWith(t, "--resume", first, "-t", "more", "-m", "openai/gpt-4o", "--no-stream", "--cwd", dir, "-o", filepath.Join(t.TempDir(), "x.json"))
	})
	if code != 0 || requests.Load() != 2 {
		t.Errorf("exit %d, requests %d", code, requests.Load())
	}
}

func TestLockdownSummary(t *testing.T) {
	tr := &trajectory.Trajectory{ExitStatus: "Submitted", Steps: 3}
	if s := summaryLine(tr, time.Second); strings.Contains(s, "tainted") {
		t.Errorf("untainted summary %q", s)
	}
	tr.LockdownEnabled, tr.Tainted = true, true
	tr.TaintSources = []trajectory.TaintSource{{Source: "web_fetch", Step: 2}, {Source: "mcp", Step: 3}}
	tr.LockdownDenials = []trajectory.LockdownDenial{{Tool: "bash"}, {Tool: "web_fetch"}}
	if s := summaryLine(tr, time.Second); !strings.Contains(s, ", tainted by web_fetch at step 2 (2 calls refused)") {
		t.Errorf("summary %q", s)
	}
	tr.TaintSources = []trajectory.TaintSource{{Source: "untrusted_task"}}
	tr.LockdownDenials = nil
	if got := lockdownSummary(tr); got != ", tainted by untrusted_task" {
		t.Errorf("lockdownSummary = %q", got)
	}
}
