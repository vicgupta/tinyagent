package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/agent"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

func TestPlanFileRequiresPlan(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte("model: openai/gpt-4o\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code := 0
	stderr := captureStderr(t, func() {
		code, _, _ = runWith(t, "-t", "x", "--cwd", dir, "-c", cfg, "--plan-file", filepath.Join(dir, "plan.md"))
	})
	if code == 0 || !strings.Contains(stderr, "--plan-file requires the plan tool") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if _, err := os.Stat(filepath.Join(dir, "plan.md")); err == nil {
		t.Fatal("plan file written")
	}
}

func TestResumePlanSettingMustMatch(t *testing.T) {
	tr, source, output, cfg := resumeFixture(t)
	code := 0
	stderr := captureStderr(t, func() {
		code, _, _ = runWith(t, "--resume", source, "--cwd", tr.Root, "-c", cfg, "-o", output, "--plan")
	})
	if code == 0 || !strings.Contains(stderr, "--plan conflicts") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	stderr = captureStderr(t, func() {
		code, _, _ = runWith(t, "--resume", source, "--cwd", tr.Root, "-c", cfg, "-o", output, "--plan-file", output+".md")
	})
	if code == 0 || !strings.Contains(stderr, "--plan-file requires the plan tool") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
}

func TestCheckpointSavedEventAndPlanExport(t *testing.T) {
	var events []string
	var saved map[string]any
	a := &agent.Agent{OnEvent: func(kind string, f map[string]any) {
		events = append(events, kind)
		if kind == "checkpoint.saved" {
			saved = f
		}
	}}
	planFile := filepath.Join(t.TempDir(), "plan.md")
	tr := &trajectory.Trajectory{Steps: 4, PendingCalls: []trajectory.PendingCall{{ID: "x", Name: "bash"}},
		Plan: &trajectory.Plan{Version: 2, UpdatedStep: 3, Items: []trajectory.PlanItem{{ID: "a", Text: "ship it", Status: "done"}, {ID: "b", Text: "test", Status: "in_progress"}}}}
	checkpointSaved(a, tr, "run.json", planFile)
	if len(events) != 1 || saved["step"] != 4 || saved["path"] != "run.json" || saved["pending"] != 1 {
		t.Fatalf("events %v %v", events, saved)
	}
	b, err := os.ReadFile(planFile)
	if err != nil || !strings.Contains(string(b), "- [x] a: ship it") || !strings.Contains(string(b), "Version 2") {
		t.Fatalf("plan file %q %v", b, err)
	}
}

func TestPlanFirstFlagConflictsWithPlanFalse(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfg, []byte("model: openai/gpt-4o\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code := 0
	stderr := captureStderr(t, func() {
		code, _, _ = runWith(t, "-t", "x", "--cwd", dir, "-c", cfg, "--plan-first", "--plan=false")
	})
	if code == 0 || !strings.Contains(stderr, "--plan-first needs the plan tool") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
}
