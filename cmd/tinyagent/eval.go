package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/eval"
	"github.com/vicgupta/tinyagent/internal/model"
)

// evalConflicts are flags that make no sense with --eval: the suite supplies
// tasks, models, profiles, working directories and trajectory paths.
var evalConflicts = []string{"t", "task", "m", "model", "profile", "o", "output", "cwd", "resume",
	"rewind", "to", "transcript", "review", "review-base", "review-commit", "worktree", "acp",
	"check", "json", "image", "follow-up", "list-profiles", "init-config", "update", "version"}

// evalOwn are flags --eval consumes itself; every other flag set on the
// command line is forwarded to each trial's child run.
var evalOwn = []string{"eval", "eval-out", "eval-keep", "eval-min-pass-rate", "dry-run", "cost-limit", "time-limit"}

func runEval(o *options, fs *flag.FlagSet, args []string, stderr io.Writer) int {
	for _, f := range evalConflicts {
		if fs.Lookup(f) != nil && flagWasSet(fs, f) {
			fmt.Fprintf(fs.Output(), "error: --eval can't be combined with -%s: the suite file supplies it\n", dashName(f))
			return 2
		}
	}
	if !o.dryRun && o.evalOut == "" {
		fmt.Fprintln(fs.Output(), "error: --eval needs --eval-out dir (or --dry-run)")
		return 2
	}
	if flagWasSet(fs, "eval-min-pass-rate") && !(o.evalMinPassRate >= 0 && o.evalMinPassRate <= 1) {
		fmt.Fprintln(fs.Output(), "error: --eval-min-pass-rate must be between 0 and 1")
		return 2
	}
	names, err := config.ProfileNames(locateConfig(o, o.cwd, stderr).Path)
	if err != nil {
		fmt.Fprintln(fs.Output(), "error:", err)
		return 2
	}
	suite, err := eval.Load(o.eval, func(p string) bool { return slices.Contains(names, p) })
	if err != nil {
		fmt.Fprintln(fs.Output(), "error:", err)
		return 2
	}
	if o.dryRun {
		eval.DryRun(os.Stdout, suite, eval.Limits{StepLimit: o.stepLimit}, estimateTrial)
		if o.costLimit > 0 {
			fmt.Fprintf(os.Stdout, "suite budget: --cost-limit $%.4f stops the run once reached\n", o.costLimit)
		}
		return 0
	}
	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	min := -1.0
	if flagWasSet(fs, "eval-min-pass-rate") {
		min = o.evalMinPassRate
	}
	return eval.Run(ctx, eval.Options{
		Suite: suite, SuitePath: o.eval, OutDir: o.evalOut, Keep: o.evalKeep,
		CostLimit: o.costLimit, TimeLimit: time.Duration(o.timeLimit) * time.Second,
		MinPassRate: min, ExtraArgs: forwardArgs(fs, args, evalOwn), Version: currentVersion(),
		Stderr: stderr, RunAgent: eval.ExecAgent(exe),
	})
}

func dashName(f string) string {
	if len(f) == 1 {
		return f
	}
	return "-" + f
}

// forwardArgs returns args without the flags named in drop (and their values).
func forwardArgs(fs *flag.FlagSet, args, drop []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" || !strings.HasPrefix(a, "-") || a == "-" {
			break
		}
		name := strings.TrimLeft(a, "-")
		hasValue := false
		if n, _, ok := strings.Cut(name, "="); ok {
			name, hasValue = n, true
		}
		takesValue := false
		if f := fs.Lookup(name); f != nil && !hasValue {
			bf, isBool := f.Value.(interface{ IsBoolFlag() bool })
			takesValue = !isBool || !bf.IsBoolFlag()
		}
		n := 1
		if takesValue && i+1 < len(args) {
			n = 2
		}
		if !slices.Contains(drop, name) {
			out = append(out, args[i:i+n]...)
		}
		i += n - 1
	}
	return out
}

// estimateTrial bounds one trial's cost: the task's cost limit, or every
// step filling the context window plus 8192 output tokens, whichever is less.
func estimateTrial(spec string, l eval.Limits) (float64, bool, string) {
	in, out, priced := model.PricePerMTok(spec)
	bound, ok := 0.0, false
	if priced && l.StepLimit > 0 {
		if w := model.ContextWindow(spec); w > 0 {
			bound, ok = float64(l.StepLimit)*(float64(w)*in+8192*out)/1e6, true
		}
	}
	if l.CostLimit > 0 && (!ok || l.CostLimit < bound) {
		bound, ok = l.CostLimit, true
	}
	if ok {
		return bound, true, ""
	}
	switch {
	case !priced:
		return 0, false, "unpriced model and no cost_limit"
	case l.StepLimit == 0:
		return 0, false, "no step_limit or cost_limit"
	default:
		return 0, false, "unknown context window and no cost_limit"
	}
}
