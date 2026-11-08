package config

import "fmt"

// Self-tuning defaults and hard caps (issue #390). A repository's own
// config can lower these, never raise them past the caps.
const (
	DefaultSelfTuningMinEvidence       = 2
	DefaultSelfTuningMaxTokens         = 2000
	MaxSelfTuningMaxTokens             = 20000
	DefaultSelfTuningMaxCostUSD        = 0.25
	MaxSelfTuningMaxCostUSD            = 5
	DefaultSelfTuningMaxAttempts       = 1
	MaxSelfTuningMaxAttempts           = 3
	DefaultSelfTuningValidationRuns    = 6
	MaxSelfTuningValidationRuns        = 20
	DefaultSelfTuningValidationCostUSD = 2
	MaxSelfTuningValidationCostUSD     = 50
	MinSelfTuningMinEvidence           = 2
	MaxSelfTuningMinEvidence           = 20
)

// SelfTuning configures tinyagent tune: proposing, validating and, after
// explicit human approval, applying one small skill or check improvement
// from past workflow runs. Off by default: with enabled false every tune
// command refuses before reading a run, calling a model or writing a file.
// Nothing runs in the background; each step is an explicit command.
type SelfTuning struct {
	Enabled bool `yaml:"enabled"`
	// Inference lets propose phrase the improvement with the auxiliary
	// model (task "tuning"); off means a deterministic proposal, no model
	// call and no cost. --tune-infer turns it on for one command.
	Inference bool `yaml:"inference,omitempty"`
	// MinEvidence is how many distinct failed runs with the same failure
	// signature a proposal needs (at least 2: one failure alone is never
	// enough).
	MinEvidence *int `yaml:"min_evidence,omitempty"`
	// MaxTokens caps the tokens (input + output) of one inference call.
	MaxTokens *int `yaml:"max_tokens,omitempty"`
	// MaxCostUSD caps the total cost of the inference calls of one
	// proposal.
	MaxCostUSD *float64 `yaml:"max_cost_usd,omitempty"`
	// MaxAttempts caps the inference calls of one proposal.
	MaxAttempts *int `yaml:"max_attempts,omitempty"`
	// ValidationRuns caps the workflow runs (baseline plus candidate) of
	// one validation.
	ValidationRuns *int `yaml:"validation_runs,omitempty"`
	// ValidationCostUSD stops a validation once its runs cost this much.
	ValidationCostUSD *float64 `yaml:"validation_cost_usd,omitempty"`
}

func intOr(p *int, d int) int {
	if p == nil {
		return d
	}
	return *p
}

func floatOr(p *float64, d float64) float64 {
	if p == nil {
		return d
	}
	return *p
}

// EffMinEvidence is the effective min_evidence.
func (s SelfTuning) EffMinEvidence() int {
	return intOr(s.MinEvidence, DefaultSelfTuningMinEvidence)
}

// EffMaxTokens is the effective max_tokens.
func (s SelfTuning) EffMaxTokens() int { return intOr(s.MaxTokens, DefaultSelfTuningMaxTokens) }

// EffMaxCostUSD is the effective max_cost_usd.
func (s SelfTuning) EffMaxCostUSD() float64 {
	return floatOr(s.MaxCostUSD, DefaultSelfTuningMaxCostUSD)
}

// EffMaxAttempts is the effective max_attempts.
func (s SelfTuning) EffMaxAttempts() int {
	return intOr(s.MaxAttempts, DefaultSelfTuningMaxAttempts)
}

// EffValidationRuns is the effective validation_runs.
func (s SelfTuning) EffValidationRuns() int {
	return intOr(s.ValidationRuns, DefaultSelfTuningValidationRuns)
}

// EffValidationCostUSD is the effective validation_cost_usd.
func (s SelfTuning) EffValidationCostUSD() float64 {
	return floatOr(s.ValidationCostUSD, DefaultSelfTuningValidationCostUSD)
}

// Validate checks self_tuning. The bounds are checked even when disabled
// so a typo is caught before someone turns it on.
func (s SelfTuning) Validate() error {
	ints := []struct {
		key      string
		v        *int
		min, max int
	}{
		{"min_evidence", s.MinEvidence, MinSelfTuningMinEvidence, MaxSelfTuningMinEvidence},
		{"max_tokens", s.MaxTokens, 1, MaxSelfTuningMaxTokens},
		{"max_attempts", s.MaxAttempts, 1, MaxSelfTuningMaxAttempts},
		{"validation_runs", s.ValidationRuns, 2, MaxSelfTuningValidationRuns},
	}
	for _, c := range ints {
		if c.v != nil && (*c.v < c.min || *c.v > c.max) {
			return fmt.Errorf("self_tuning.%s must be between %d and %d, got %d", c.key, c.min, c.max, *c.v)
		}
	}
	floats := []struct {
		key string
		v   *float64
		max float64
	}{
		{"max_cost_usd", s.MaxCostUSD, MaxSelfTuningMaxCostUSD},
		{"validation_cost_usd", s.ValidationCostUSD, MaxSelfTuningValidationCostUSD},
	}
	for _, c := range floats {
		if c.v != nil && (!(*c.v > 0) || *c.v > c.max) {
			return fmt.Errorf("self_tuning.%s must be > 0 and at most %g, got %g", c.key, c.max, *c.v)
		}
	}
	return nil
}
