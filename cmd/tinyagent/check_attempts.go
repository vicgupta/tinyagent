package main

import (
	"flag"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/vicgupta/tinyagent/internal/config"
)

// checkAttempts returns the --check attempts line (issue #172), or "" when
// no --attempts flag was given and the config has no attempts section.
// With --attempts flags it validates the invocation the way a run would
// (exit 2 on the same errors) and prints the plan: how many attempts with
// which models, how many checks and their timeouts, stop_on_pass, keep and
// the worktree paths. Check command text is never printed, since it may
// carry tokens; only the count and timeouts are.
func checkAttempts(fs *flag.FlagSet, o *options, cfg config.AgentConfig) (string, error) {
	if !attemptsActive(fs) {
		ca := cfg.Attempts
		if len(ca.Checks) == 0 && ca.StopOnPass == nil && ca.Keep == "" {
			return "", nil
		}
		return fmt.Sprintf("off (config sets %s, stop_on_pass %t, keep %s; used only with --attempts)",
			countNoun(len(ca.Checks), "check"), ca.StopsOnPass(), ca.KeepMode()), nil
	}
	p, err := planAttempts(o, fs, cfg, true)
	if err != nil {
		return "", err
	}
	shown := make([]string, len(p.models))
	for i, m := range p.models {
		shown[i] = m
		if m == "" {
			shown[i] = "(default model)"
		}
	}
	modelSrc := "from -m"
	if flagWasSet(fs, "attempt-model") {
		modelSrc = "from --attempt-model"
	} else if !flagWasSet(fs, "m", "model") {
		modelSrc = modelSource(fs, cfg.Model)
	}
	checkSrc := "from config"
	if flagWasSet(fs, "attempt-check") {
		checkSrc = "from --attempt-check"
	}
	timeouts := make([]string, len(p.checks))
	for i, c := range p.checks {
		timeouts[i] = fmt.Sprintf("%ds", c.TimeoutSeconds)
	}
	name := p.base
	if p.generated {
		name = "<generated>"
	}
	n := len(p.models)
	return fmt.Sprintf("up to %d runs (%s; %s), %s (%s, timeout %s), stop_on_pass %t, keep %s, worktrees %s-a<N>; cost, token and time limits are shared by all attempts, which can cost up to %dx a single run",
		n, strings.Join(shown, ", "), modelSrc,
		countNoun(len(p.checks), "check"), checkSrc, strings.Join(timeouts, ", "),
		cfg.Attempts.StopsOnPass(), cfg.Attempts.KeepMode(),
		filepath.Join(p.top, worktreeDir, name), n), nil
}

// countNoun renders n and noun, adding "s" unless n is 1.
func countNoun(n int, noun string) string {
	return fmt.Sprintf("%d %s", n, plural(n, noun, noun+"s"))
}
