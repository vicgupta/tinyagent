package config

import (
	"errors"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Attempt keep modes: which non-winning attempt worktrees --attempts keeps
// once a winner is picked. The winner is always kept.
const (
	AttemptsKeepAll    = "all"
	AttemptsKeepFailed = "failed"
	AttemptsKeepNone   = "none"
)

// Attempt check timeout bounds, in seconds (matching --attempt-check-timeout).
const (
	DefaultAttemptCheckTimeout = 600
	MaxAttemptCheckTimeout     = 3600
)

// Attempts configures best-of-N runs (--attempts, issue #172). Like hooks
// it comes only from the user config, -c or a profile, never from the
// repository. It has no effect without --attempts.
type Attempts struct {
	// Checks are the verification commands an attempt must pass. Each runs
	// with bash -c in the attempt's worktree. --attempt-check replaces the
	// whole list.
	Checks []AttemptCheck `yaml:"checks,omitempty"`
	// StopOnPass stops after the first passing attempt (the default). False
	// runs all N attempts; the lowest-numbered passing attempt still wins.
	StopOnPass *bool `yaml:"stop_on_pass,omitempty"`
	// Keep chooses which non-winning worktrees and branches are kept once
	// an attempt wins: all (the default), failed (remove passing
	// non-winners) or none. Nothing is removed when no attempt wins.
	Keep string `yaml:"keep,omitempty"`
}

// AttemptCheck is one attempts.checks entry.
type AttemptCheck struct {
	Command string `yaml:"command"`
	// TimeoutSeconds kills the check after this many seconds; 0 means the
	// default (600). Range 1-3600.
	TimeoutSeconds int `yaml:"timeout_seconds,omitempty"`
}

// StopsOnPass reports the effective stop_on_pass setting.
func (a Attempts) StopsOnPass() bool { return a.StopOnPass == nil || *a.StopOnPass }

// KeepMode returns the effective keep mode.
func (a Attempts) KeepMode() string {
	if a.Keep == "" {
		return AttemptsKeepAll
	}
	return a.Keep
}

// Validate checks the attempts section.
func (a Attempts) Validate() error {
	for i, c := range a.Checks {
		if strings.TrimSpace(c.Command) == "" {
			return fmt.Errorf("attempts.checks[%d].command must not be empty", i)
		}
		if c.TimeoutSeconds < 0 || c.TimeoutSeconds > MaxAttemptCheckTimeout {
			return fmt.Errorf("attempts.checks[%d].timeout_seconds must be between 1 and %d, got %d", i, MaxAttemptCheckTimeout, c.TimeoutSeconds)
		}
	}
	switch a.Keep {
	case "", AttemptsKeepAll, AttemptsKeepFailed, AttemptsKeepNone:
	default:
		return fmt.Errorf("attempts.keep %q must be all, failed or none", a.Keep)
	}
	return nil
}

// checkAttempts decodes an attempts section strictly, so a typo exits 2
// like any other invalid config instead of failing as a parse error.
func checkAttempts(n *yaml.Node) error {
	if err := checkStrict(n, "attempts", &Attempts{}); err != nil {
		return errors.New(yamlLine.ReplaceAllString(err.Error(), ""))
	}
	return nil
}
