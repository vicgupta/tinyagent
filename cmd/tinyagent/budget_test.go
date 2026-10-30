package main

import (
	"path/filepath"
	"testing"

	"github.com/vicgupta/tinyagent/internal/agent"
	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

func TestTokenLimitFlagOverridesConfig(t *testing.T) {
	var o options
	fs := newFlagSet(&o)
	if err := fs.Parse([]string{"--token-limit", "5000"}); err != nil {
		t.Fatal(err)
	}
	cfg := config.AgentConfig{TokenLimit: 100, StepLimit: 7}
	applyFlagOverrides(fs, &cfg, o.stepLimit, o.costLimit, o.timeLimit)
	if cfg.TokenLimit != 5000 || cfg.StepLimit != 7 {
		t.Errorf("token limit %d, step limit %d", cfg.TokenLimit, cfg.StepLimit)
	}

	fs = newFlagSet(&o)
	cfg = config.AgentConfig{TokenLimit: 100}
	applyFlagOverrides(fs, &cfg, 0, 0, 0)
	if cfg.TokenLimit != 100 {
		t.Errorf("unset flag changed the config's token_limit to %d", cfg.TokenLimit)
	}
}

func TestTokenLimitMustNotBeNegative(t *testing.T) {
	if msg := validateLimits(options{tokenLimit: -1, maxFormatErrors: 1}); msg != "--token-limit must be >= 0 (0 = unlimited)" {
		t.Errorf("validateLimits = %q", msg)
	}
	if msg := validateLimits(options{tokenLimit: 0, maxFormatErrors: 1}); msg != "" {
		t.Errorf("validateLimits(0) = %q", msg)
	}
}

func TestResumeCommandDoublesTokenLimit(t *testing.T) {
	dir := t.TempDir()
	run := filepath.Join(dir, "run.json")
	tr := trajectory.Trajectory{ExitStatus: agent.StatusTokenLimit}
	got := resumeCommand(&tr, run, "/src", config.AgentConfig{TokenLimit: 1000})
	want := "tinyagent --resume " + run + " --cwd /src --token-limit 2000 -o " + filepath.Join(dir, "run-2.json")
	if got != want {
		t.Errorf("resumeCommand = %q, want %q", got, want)
	}
}
