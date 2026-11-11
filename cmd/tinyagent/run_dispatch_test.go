package main

import (
	"flag"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/vicgupta/tinyagent/internal/agent"
	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// Unit tests for the run setup helpers split out in issue #416.

// parsed returns options and a flag set after parsing args.
func parsed(t *testing.T, args ...string) (*options, *flag.FlagSet) {
	t.Helper()
	o := &options{}
	fs := newFlagSet(o)
	fs.SetOutput(io.Discard)
	if err := fs.Parse(args); err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	return o, fs
}

func TestRunFlagError(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{nil, ""},
		{[]string{"--env", "vm"}, `--env "vm" must be local or container`},
		{[]string{"--sandbox-backend", "jail"}, `--sandbox-backend "jail" must be auto, landlock or bwrap`},
		{[]string{"--command-env", ""}, `--command-env "" must be all, core or none`},
		{[]string{"--eval-keep"}, "--eval-out, --eval-keep and --eval-min-pass-rate apply only to --eval"},
		{[]string{"--workflow-name", "x"}, "--workflow-name applies only to --workflow"},
		{[]string{"--workflow-observe"}, "--workflow-observe applies only to --workflow"},
		{[]string{"--triage-out", "x"}, "--triage-out and --triage-stale-days apply only to --triage"},
		// The first failing check wins.
		{[]string{"--env", "vm", "--workflow-name", "x"}, `--env "vm" must be local or container`},
	}
	for _, c := range cases {
		o, fs := parsed(t, c.args...)
		if got := runFlagError(o, fs); got != c.want {
			t.Errorf("%v: got %q, want %q", c.args, got, c.want)
		}
	}
}

func TestDispatchersPassThroughPlainRun(t *testing.T) {
	o, fs := parsed(t, "-t", "do it")
	for name, d := range map[string]func(*options, *flag.FlagSet) (int, bool){
		"maintenance": dispatchMaintenance, "conflicts": checkModeConflicts,
		"flags": validateRunFlags, "modes": dispatchModes,
		"setup": dispatchSetupCommands, "rewind/resume": dispatchRewindResume,
	} {
		if code, done := d(o, fs); done {
			t.Errorf("%s handled a plain -t run (code %d)", name, code)
		}
	}
}

func TestDispatchRewindResumeConflicts(t *testing.T) {
	stderr := os.Stderr
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer devnull.Close()
	os.Stderr = devnull
	defer func() { os.Stderr = stderr }()
	for _, args := range [][]string{
		{"--rewind", "a.json", "--resume", "b.json"},
		{"--rewind", "a.json", "--profile", "p"},
		{"--to", "start"},
		{"--no-agents-md", "--scoped-agents-md"},
		{"--resume", "a.json", "--output-schema", "s.json"},
		{"--worktree", "--resume", "a.json"},
	} {
		o, fs := parsed(t, args...)
		if code, done := dispatchRewindResume(o, fs); !done || code != 2 {
			t.Errorf("%v: code %d done %v, want 2 true", args, code, done)
		}
	}
}

func TestApplyPlanFlags(t *testing.T) {
	o, fs := parsed(t, "--plan-first")
	cfg := config.Default()
	if err := applyPlanFlags(fs, *o, &cfg); err != nil || !cfg.Plan.Enabled || !cfg.Plan.RequireFirst {
		t.Errorf("--plan-first: err %v, plan %+v", err, cfg.Plan)
	}
	o, fs = parsed(t, "--plan-first", "--plan=false")
	cfg = config.Default()
	if err := applyPlanFlags(fs, *o, &cfg); err == nil {
		t.Error("--plan-first --plan=false accepted")
	}
	o, fs = parsed(t, "--plan-file", "plan.md")
	cfg = config.Default()
	if err := applyPlanFlags(fs, *o, &cfg); err == nil {
		t.Error("--plan-file without the plan tool accepted")
	}
}

func TestApplyToolFlags(t *testing.T) {
	o, fs := parsed(t, "--images", "--apply-patch", "--subagents", "--ask-user")
	cfg := config.Default()
	applyToolFlags(fs, *o, &cfg)
	if !cfg.Images.Enabled || !cfg.ApplyPatch || !cfg.Subagents.Enabled || !cfg.AskUser.Enabled {
		t.Errorf("flags not applied: %+v", cfg)
	}
	o, fs = parsed(t)
	cfg = config.Default()
	cfg.ApplyPatch = true
	applyToolFlags(fs, *o, &cfg)
	if !cfg.ApplyPatch {
		t.Error("an unset flag overrode the config")
	}
}

func TestCheckpointCallback(t *testing.T) {
	a := &agent.Agent{}
	if cb := checkpointCallback(a, "", ""); cb != nil {
		t.Error("callback without -o or --plan-file")
	}
	out := filepath.Join(t.TempDir(), "run.json")
	cb := checkpointCallback(a, out, "")
	if cb == nil {
		t.Fatal("no callback with -o")
	}
	if err := cb(&trajectory.Trajectory{Task: "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Errorf("checkpoint not saved: %v", err)
	}
	if cb := checkpointCallback(a, "", filepath.Join(t.TempDir(), "plan.md")); cb == nil {
		t.Error("no callback with --plan-file")
	}
}

func TestStructuredExitCode(t *testing.T) {
	if got := structuredExitCode(&trajectory.Trajectory{ExitStatus: agent.StatusOutputTruncated}, true); got != exitOutputTruncated {
		t.Errorf("truncated = %d", got)
	}
	if got := structuredExitCode(&trajectory.Trajectory{ExitStatus: agent.StatusCompleted}, true); got != 1 {
		t.Errorf("completed without result = %d", got)
	}
	if got := structuredExitCode(&trajectory.Trajectory{ExitStatus: agent.StatusSubmitted, StructuredOutput: []byte(`{}`)}, true); got != 0 {
		t.Errorf("submitted = %d", got)
	}
}

func TestTextSink(t *testing.T) {
	r := &oneShot{o: &options{noStream: true}, progress: io.Discard}
	if r.textSink(&textDeltas{}) != nil {
		t.Error("--no-stream has a text sink")
	}
	r.o = &options{}
	if r.textSink(&textDeltas{}) == nil {
		t.Error("streaming run has no text sink")
	}
}
