package main

import (
	"path/filepath"
	"testing"

	"github.com/vicgupta/tinyagent/internal/config"
)

func countArg(args []string, want string) int {
	n := 0
	for _, a := range args {
		if a == want {
			n++
		}
	}
	return n
}

func TestWorkflowVerifyOnStopDefault(t *testing.T) {
	cases := []struct {
		name  string
		extra []string
		bare  int // occurrences of the bare --verify-on-stop
		off   int // occurrences of --verify-on-stop=false
	}{
		{"default on", nil, 1, 0},
		{"opt out", []string{"--verify-on-stop=false"}, 0, 1},
		{"explicit on", []string{"--verify-on-stop"}, 1, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wf := writeWorkflowFiles(t)
			var calls [][]string
			fakeWorkflowAgent(t, "1. do it\nSUCCESS CHECK: PASS", &calls)
			args := append([]string{"--workflow", wf, "-t", "plan it", "-o", filepath.Join(t.TempDir(), "run.json"), "--cwd", t.TempDir()}, c.extra...)
			code, _, stderr := runCLI(t, args...)
			if code != 0 {
				t.Fatalf("exit %d, stderr:\n%s", code, stderr)
			}
			if len(calls) == 0 {
				t.Fatal("no stage ran")
			}
			for _, a := range calls {
				if got := countArg(a, "--verify-on-stop"); got != c.bare {
					t.Errorf("bare --verify-on-stop x%d, want %d: %q", got, c.bare, a)
				}
				if got := countArg(a, "--verify-on-stop=false"); got != c.off {
					t.Errorf("--verify-on-stop=false x%d, want %d: %q", got, c.off, a)
				}
			}
		})
	}
}

func TestVerifyOnStopOffByDefaultOutsideWorkflows(t *testing.T) {
	var o options
	fs := newFlagSet(&o)
	if err := fs.Parse([]string{"-t", "hello"}); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	if err := applyVerifyFlags(fs, &cfg, o.verify); err != nil {
		t.Fatal(err)
	}
	if cfg.VerifyOnStop.Enabled {
		t.Error("verify-on-stop is on by default for a non-workflow run")
	}
}
