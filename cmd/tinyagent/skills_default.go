package main

import (
	"flag"
	"os"
	"strings"

	"github.com/vicgupta/tinyagent/internal/attempts"
	"github.com/vicgupta/tinyagent/internal/config"
)

// Skill discovery sources reported by --check (#359).
const (
	skillsSourceFlag        = "flag"
	skillsSourceConfig      = "config"
	skillsSourceInteractive = "interactive default"
	skillsSourceHardened    = "hardened default"
)

// skillsMode is what the skills default depends on: whether the run is
// interactive and, if it is hardened, why.
type skillsMode struct {
	// Interactive is a chat session or a run with stdin and stdout both
	// terminals (the TTY check chat already uses).
	Interactive bool
	// Hardened names why the run never picks skills up implicitly ("" when
	// it is not hardened): a CI variable, --eval or --attempts, lockdown or
	// --read-only.
	Hardened string
}

// skillsInputs are the explicit skills settings a run was given.
type skillsInputs struct {
	FlagSet, FlagOn bool   // --skills / --no-skills
	FlagName        string // the flag that set it, for the source text
	Config          *bool  // skills: in the config; nil when unset
	ConfigFromCwd   bool   // the config is ./tinyagent.yaml found in --cwd
	TrustConfig     bool   // --trust-config
}

// skillsDecision is the resolved skills setting and why.
type skillsDecision struct {
	Enabled bool   `json:"enabled"`
	Source  string `json:"source"` // one of the skillsSource constants
	Detail  string `json:"detail"` // e.g. "--no-skills", "CI is set"
}

// String renders d for --check and -v, e.g. "on (interactive default)".
func (d skillsDecision) String() string {
	s := "off"
	if d.Enabled {
		s = "on"
	}
	why := d.Source
	if d.Detail != "" {
		why += ": " + d.Detail
	}
	return s + " (" + why + ")"
}

// resolveSkills is the one place the Agent Skills default is decided
// (#359). Precedence, highest first: --skills/--no-skills, an explicit
// skills: true|false in the config, then the mode default (on in
// interactive runs, off in non-interactive or hardened ones). A hardened
// run ignores skills: true from ./tinyagent.yaml in the cwd unless
// --trust-config is set: a cloned repository must not put its own skills
// into the system prompt of a CI or review run.
func resolveSkills(in skillsInputs, m skillsMode) skillsDecision {
	if in.FlagSet {
		return skillsDecision{Enabled: in.FlagOn, Source: skillsSourceFlag, Detail: in.FlagName}
	}
	hardened := m.Hardened != "" || !m.Interactive
	reason := m.Hardened
	if reason == "" && !m.Interactive {
		reason = "not an interactive terminal"
	}
	if in.Config != nil {
		if *in.Config && hardened && in.ConfigFromCwd && !in.TrustConfig {
			return skillsDecision{Source: skillsSourceHardened, Detail: reason + "; skills: true in ./tinyagent.yaml ignored without --trust-config"}
		}
		return skillsDecision{Enabled: *in.Config, Source: skillsSourceConfig}
	}
	if hardened {
		return skillsDecision{Source: skillsSourceHardened, Detail: reason}
	}
	return skillsDecision{Enabled: true, Source: skillsSourceInteractive}
}

// hardenedReason names the first hardened condition that applies to a run,
// or "": the GitHub Action or CI, an --eval or --attempts child, lockdown or
// --read-only.
func hardenedReason(eval, attemptsRun, lockdown, readOnly bool) string {
	switch {
	case envOn("GITHUB_ACTIONS"):
		return "GITHUB_ACTIONS is set"
	case envOn("CI"):
		return "CI is set"
	case eval:
		return "--eval"
	case attemptsRun || os.Getenv(attempts.EnvAttempt) != "":
		return "--attempts"
	case lockdown:
		return "lockdown"
	case readOnly:
		return "--read-only"
	}
	return ""
}

// envOn reports whether the variable is set to something other than
// empty, "0" or "false".
func envOn(name string) bool {
	v := strings.TrimSpace(os.Getenv(name))
	return v != "" && v != "0" && !strings.EqualFold(v, "false")
}

// runIsInteractive reports whether a one-shot run is interactive: stdin
// and stdout are both terminals, the same check chat uses.
var runIsInteractive = func() bool { return chatTerminal() }

// skillsInputsFor collects the explicit skills settings from the flags and
// the loaded config. The flags must already have passed skillsFlag.
func skillsInputsFor(fs *flag.FlagSet, o options, cfg config.AgentConfig, src config.Source) skillsInputs {
	on, set, _ := skillsFlag(fs, o)
	name := "--skills"
	if flagWasSet(fs, "no-skills") {
		name = "--no-skills"
		if flagWasSet(fs, "skills") && o.skills {
			name = "--skills"
		}
	}
	return skillsInputs{FlagSet: set, FlagOn: on, FlagName: name, Config: cfg.Skills,
		ConfigFromCwd: src.Kind == config.SourceCwd, TrustConfig: o.trustConfig}
}

// applySkillsDefault resolves the skills setting into cfg.Skills and
// returns the decision.
func applySkillsDefault(fs *flag.FlagSet, o options, cfg *config.AgentConfig, src config.Source, m skillsMode) skillsDecision {
	d := resolveSkills(skillsInputsFor(fs, o, *cfg, src), m)
	cfg.Skills = config.BoolPtr(d.Enabled)
	return d
}
