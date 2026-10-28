package main

import (
	"flag"
	"fmt"
	"strings"
	"time"

	"github.com/vicgupta/tinyagent/internal/attempts"
	"github.com/vicgupta/tinyagent/internal/config"
)

// attemptPlan is a validated --attempts invocation: what runAttempts runs
// and what --check reports.
type attemptPlan struct {
	models    []string
	base      string // worktree base name; attempt i uses <base>-a<i>
	generated bool   // base was generated, not given with --worktree
	top       string // repository top-level directory
	checks    []attempts.Check
}

// errAttemptUsage marks a usage error from planAttempts; it exits 2.
type errAttemptUsage struct{ msg string }

func (e errAttemptUsage) Error() string { return e.msg }

// planAttempts validates an --attempts invocation the way a run does,
// before any worktree is created or any model request is made: conflicting
// flags, the attempt count, per-attempt models and their credentials, the
// checks and their timeout, the worktree name, and that every attempt's
// worktree path and branch are free. With forCheck, the task isn't
// required (--check never runs one) and --check itself isn't a conflict.
// It only reads the repository; it never writes anything.
func planAttempts(o *options, fs *flag.FlagSet, cfg config.AgentConfig, forCheck bool) (attemptPlan, error) {
	a := o.attempts
	usage := func(format string, v ...any) error {
		return errAttemptUsage{fmt.Sprintf(format, v...)}
	}
	for _, f := range attemptConflicts {
		if forCheck && f == "check" {
			continue
		}
		if fs.Lookup(f) != nil && flagWasSet(fs, f) {
			return attemptPlan{}, usage("--attempts can't be combined with -%s", dashName(f))
		}
	}
	if a.n < 1 || a.n > attempts.MaxAttempts {
		return attemptPlan{}, usage("--attempts must be between 1 and %d", attempts.MaxAttempts)
	}
	if len(a.models) > 0 && len(a.models) != a.n {
		return attemptPlan{}, usage("--attempt-model was given %d times but --attempts is %d: give one per attempt, or none to use -m for every attempt", len(a.models), a.n)
	}
	if len(a.models) > 0 && flagWasSet(fs, "m", "model") {
		return attemptPlan{}, usage("use either -m or --attempt-model with --attempts, not both")
	}
	if len(a.checks) == 0 && len(cfg.Attempts.Checks) == 0 {
		return attemptPlan{}, usage("--attempts needs at least one --attempt-check command (or attempts.checks in the config file) to decide which attempt passes")
	}
	if len(a.checks) == 0 && flagWasSet(fs, "attempt-check-timeout") {
		return attemptPlan{}, usage("--attempt-check-timeout only applies to --attempt-check commands; set timeout_seconds on each attempts.checks entry instead")
	}
	if a.checkTimeout < 1 || a.checkTimeout > attempts.MaxCheckTimeout {
		return attemptPlan{}, usage("--attempt-check-timeout must be between 1 and %d seconds", attempts.MaxCheckTimeout)
	}
	if !forCheck {
		if o.task == "" {
			return attemptPlan{}, usage("--attempts needs a task (-t)")
		}
		if o.task == "-" {
			return attemptPlan{}, usage("--attempts can't read the task from stdin; pass it with -t")
		}
	}
	models := []string(a.models)
	if len(models) == 0 {
		// The model a single run would use: -m, else $AGENT_MODEL, else
		// the config's model, else the built-in default.
		spec := resolveModel(fs, o.modelName, cfg.Model)
		models = make([]string, a.n)
		for i := range models {
			models[i] = spec
		}
	}
	for i, m := range models {
		if strings.HasPrefix(m, "-") {
			return attemptPlan{}, usage("--attempt-model %q (attempt %d) is not a model spec", m, i+1)
		}
	}
	openaiAPI := cfg.OpenAIAPI
	if flagWasSet(fs, "openai-api") {
		openaiAPI = o.openaiAPI
	}
	if err := checkAttemptModels(models, cfg, openaiAPI); err != nil {
		return attemptPlan{}, usage("%v", err)
	}

	base, generated := o.worktree.name, false
	if base == "" {
		base, generated = generateWorktreeName(time.Now()), true
	}
	if err := validWorktreeName(base); err != nil {
		return attemptPlan{}, usage("%v", err)
	}
	if len(base) > 61 {
		return attemptPlan{}, usage("--worktree name %q is too long for --attempts (at most 61 characters; -a<N> is appended)", base)
	}
	top, err := preflightAttemptWorktrees(o.cwd, base, a.n)
	if err != nil {
		return attemptPlan{}, usage("%v", err)
	}
	return attemptPlan{models: models, base: base, generated: generated, top: top,
		checks: attemptChecks(a, cfg.Attempts)}, nil
}
