package harness

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const goodPlan = "Plan:\n\n## Task 1: add the flag\nWire --name.\nAcceptance criteria:\n- `app --name x` prints hello x\nVerify: `FOO=1 go test ./...`\n\n**Task 2:** docs\n**Acceptance criteria:**\n1. README lists --name\nVerify: `./check.sh | grep ok`\nSUCCESS CHECK: PASS\n"

func TestSpecGate(t *testing.T) {
	cwd := t.TempDir()
	os.WriteFile(filepath.Join(cwd, "check.sh"), []byte("#!/bin/sh\n"), 0o755)
	if g := SpecGate(goodPlan, cwd); !g.Passed || g.Tasks != 2 {
		t.Fatalf("good plan rejected: %+v", g)
	}
	cases := map[string]struct{ plan, want string }{
		"no tasks":         {"just some prose\nSUCCESS CHECK: PASS", "tasks"},
		"no criteria":      {"Task 1: x\nVerify: `go test ./...`\n", "task 1: acceptance criteria"},
		"empty criteria":   {"Task 1: x\nAcceptance criteria:\nVerify: `go test ./...`\n", "task 1: acceptance criteria"},
		"no verify":        {"Task 1: x\nAcceptance criteria:\n- works\n", "task 1: verify command"},
		"no backticks":     {"Task 1: x\nAcceptance criteria:\n- works\nVerify: run the tests\n", "task 1: runnable verify command"},
		"missing binary":   {"Task 1: x\nAcceptance criteria:\n- works\nVerify: `no-such-binary-xyz --check`\n", "task 1: runnable verify command"},
		"missing script":   {"Task 1: x\nAcceptance criteria:\n- works\nVerify: `./nope.sh`\n", "task 1: runnable verify command"},
		"second task thin": {"Task 1: x\nAcceptance criteria:\n- works\nVerify: `go test`\nTask 2: y\n", "task 2: acceptance criteria"},
	}
	for name, c := range cases {
		g := SpecGate(c.plan, cwd)
		if g.Passed || g.Reason == "" || !strings.Contains(strings.Join(g.Missing, ","), c.want) {
			t.Errorf("%s: %+v, want missing %q", name, g, c.want)
		}
	}
}
