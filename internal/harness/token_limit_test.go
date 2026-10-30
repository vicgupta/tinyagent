package harness

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
)

func TestStageTokenLimit(t *testing.T) {
	w := loadTestWorkflow(t, "version: 1\ndefaults:\n  token_limit: 5000\nworkflows:\n  build:\n    stages:\n      - skill: plan-tasks\n        inputs: {spec: \"s\"}\n      - skill: plan-tasks\n        token_limit: 700\n        inputs: {spec: \"s\"}\n")
	r, _ := w.Route("", "build")
	var calls [][]string
	agent := func(_ context.Context, args []string, _ io.Writer) error {
		calls = append(calls, args)
		for i := range args {
			if args[i] == "-o" {
				os.WriteFile(args[i+1], []byte(`{"exit_status":"Submitted","submission":"SUCCESS CHECK: PASS"}`), 0o600)
			}
		}
		return nil
	}
	Run(context.Background(), Options{Route: r, Task: "t", Cwd: "/w", Dir: t.TempDir(), RunAgent: agent})
	if len(calls) != 2 {
		t.Fatalf("%d stage runs, want 2", len(calls))
	}
	if s := strings.Join(calls[0], " "); !strings.Contains(s, "--token-limit 5000 ") {
		t.Errorf("stage 1 args %q missing the default token limit", s)
	}
	if s := strings.Join(calls[1], " "); !strings.Contains(s, "--token-limit 700 ") {
		t.Errorf("stage 2 args %q missing its own token limit", s)
	}
}

func TestStageTokenLimitUnsetAndNegative(t *testing.T) {
	w := loadTestWorkflow(t, "version: 1\nworkflows:\n  build:\n    stages:\n      - skill: plan-tasks\n        inputs: {spec: \"s\"}\n")
	r, _ := w.Route("", "build")
	if r.Pipeline.Stages[0].TokenLimit != 0 {
		t.Fatalf("token limit defaults to %d, want 0 (off)", r.Pipeline.Stages[0].TokenLimit)
	}
	if err := checkSettings("defaults", Settings{TokenLimit: new(int)}); err != nil {
		t.Fatalf("token_limit 0 rejected: %v", err)
	}
	neg := -1
	if err := checkSettings("defaults", Settings{TokenLimit: &neg}); err == nil || !strings.Contains(err.Error(), "token_limit must be >= 0") {
		t.Fatalf("negative token_limit: %v", err)
	}
}
