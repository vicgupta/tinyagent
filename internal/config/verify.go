package config

import (
	"fmt"
	"regexp"
	"strings"
)

// DefaultVerifyMaxReprompts is the default verify_on_stop.max_reprompts.
const DefaultVerifyMaxReprompts = 2

// MaxVerifyReprompts is the largest accepted verify_on_stop.max_reprompts.
const MaxVerifyReprompts = 10

// VerifyOnStop configures the opt-in verify-before-stop guard: when the
// model gives a final answer while files changed since the last successful
// verification, the run is sent back once per re-prompt to verify first.
// It is off by default; --verify-on-stop overrides Enabled.
type VerifyOnStop struct {
	Enabled bool `yaml:"enabled"`
	// Commands are bash commands that count as verification when they exit
	// 0: a run whose command is one of these, or starts with one followed
	// by a space, verifies. They are named in the re-prompt.
	// --verify-command replaces this list.
	Commands []string `yaml:"commands,omitempty"`
	// Patterns are Go regular expressions; a successful bash run whose
	// command matches any of them also verifies. When neither Commands nor
	// Patterns is set, DefaultVerifyPatterns apply.
	Patterns []string `yaml:"patterns,omitempty"`
	// Checks are computational sensors: fast, deterministic bash commands
	// (lint, type check, tests) that pass on exit 0. When the model gives a
	// final answer with changes pending, the harness runs them itself, in
	// the listed order (put the cheapest first), stopping at the first
	// failure. Each also counts as a verification command when the model
	// runs it. --verify-check replaces this list. Empty by default.
	Checks []string `yaml:"checks,omitempty"`
	// Review turns on the inferential tier: after every check in Checks
	// passes at stop, the model is asked once, without tools, to review
	// the changes the run made. Its findings are reported only (event,
	// stderr and trajectory); they never re-prompt the model or change the
	// run's status. Requires at least one check. --verify-review overrides
	// it. Off by default.
	Review bool `yaml:"review,omitempty"`
	// MaxReprompts caps how many times one run is sent back to verify
	// (1 to MaxVerifyReprompts; default DefaultVerifyMaxReprompts). After
	// the cap the run stops and is marked unverified.
	MaxReprompts int `yaml:"max_reprompts"`
}

// DefaultVerifyPatterns recognise common test and build commands, at the
// start of the command or after &&, ; or ||.
var DefaultVerifyPatterns = []string{
	`(^|&&|;|\|\|)\s*go\s+(test|build|vet)\b`,
	`(^|&&|;|\|\|)\s*cargo\s+(test|build|check|clippy)\b`,
	`(^|&&|;|\|\|)\s*(npm|pnpm|yarn|bun)\s+(run\s+)?(test|build|check|lint|typecheck)\b`,
	`(^|&&|;|\|\|)\s*make(\s|$)`,
	`(^|&&|;|\|\|)\s*(pytest|tox|rspec|ctest|tsc)\b`,
	`(^|&&|;|\|\|)\s*python3?\s+-m\s+(pytest|unittest)\b`,
	`(^|&&|;|\|\|)\s*(dotnet|swift|deno|mix)\s+(test|build)\b`,
	`(^|&&|;|\|\|)\s*(mvn|gradle|\./gradlew|\./mvnw)\s+.*\b(test|verify|build|check)\b`,
}

// Validate checks the commands, patterns and re-prompt cap.
func (v VerifyOnStop) Validate() error {
	for _, c := range v.Commands {
		if strings.TrimSpace(c) == "" {
			return fmt.Errorf("verify_on_stop.commands: empty command")
		}
	}
	for _, c := range v.Checks {
		if strings.TrimSpace(c) == "" {
			return fmt.Errorf("verify_on_stop.checks: empty command")
		}
	}
	if v.Review && len(v.Checks) == 0 {
		return fmt.Errorf("verify_on_stop.review needs at least one verify_on_stop.checks entry: the review runs only after every computational check passes")
	}
	for _, p := range v.Patterns {
		if _, err := regexp.Compile(p); err != nil {
			return fmt.Errorf("verify_on_stop.patterns: %q: %v", p, err)
		}
	}
	if v.MaxReprompts < 1 || v.MaxReprompts > MaxVerifyReprompts {
		return fmt.Errorf("verify_on_stop.max_reprompts must be a whole number from 1 to %d, got %d", MaxVerifyReprompts, v.MaxReprompts)
	}
	return nil
}
