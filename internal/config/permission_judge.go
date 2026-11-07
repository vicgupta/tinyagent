package config

import "fmt"

// DefaultPermissionJudgeMinConfidence is the default
// permissions.judge.min_confidence: the judge may auto-allow only at or
// above this confidence.
const DefaultPermissionJudgeMinConfidence = 0.85

// DefaultPermissionJudgeTimeoutSec is the default
// permissions.judge.timeout_seconds for one judge model call.
const DefaultPermissionJudgeTimeoutSec = 10

// MaxPermissionJudgeTimeoutSec is the largest accepted
// permissions.judge.timeout_seconds.
const MaxPermissionJudgeTimeoutSec = 60

// PermissionJudge is the optional AI tier that classifies a tool call
// when the deterministic policy would otherwise ask the user. It is off
// by default; when enabled it may only auto-allow on high confidence and
// never silently denies — low confidence, risky, or a model error falls
// back to the normal human prompt. Explicit deny rules and lockdown are
// never consulted by the judge (they win earlier).
type PermissionJudge struct {
	Enabled bool `yaml:"enabled"`
	// MinConfidence is the minimum confidence in [0, 1] required to
	// auto-allow. Nil means DefaultPermissionJudgeMinConfidence.
	MinConfidence *float64 `yaml:"min_confidence,omitempty"`
	// TimeoutSec caps one judge model call. Nil means
	// DefaultPermissionJudgeTimeoutSec; 0 is rejected.
	TimeoutSec *int `yaml:"timeout_seconds,omitempty"`
}

// Enabled reports whether the judge tier is on.
func (j PermissionJudge) On() bool { return j.Enabled }

// ConfidenceThreshold is the effective min_confidence.
func (j PermissionJudge) ConfidenceThreshold() float64 {
	if j.MinConfidence == nil {
		return DefaultPermissionJudgeMinConfidence
	}
	return *j.MinConfidence
}

// TimeoutSeconds is the effective timeout_seconds.
func (j PermissionJudge) TimeoutSeconds() int {
	if j.TimeoutSec == nil {
		return DefaultPermissionJudgeTimeoutSec
	}
	return *j.TimeoutSec
}

// Validate checks permissions.judge. Empty (disabled) is fine.
func (j PermissionJudge) Validate() error {
	if j.MinConfidence != nil {
		c := *j.MinConfidence
		if c < 0 || c > 1 {
			return fmt.Errorf("permissions.judge.min_confidence must be between 0 and 1, got %g", c)
		}
	}
	if j.TimeoutSec != nil {
		t := *j.TimeoutSec
		if t < 1 || t > MaxPermissionJudgeTimeoutSec {
			return fmt.Errorf("permissions.judge.timeout_seconds must be between 1 and %d, got %d", MaxPermissionJudgeTimeoutSec, t)
		}
	}
	return nil
}
