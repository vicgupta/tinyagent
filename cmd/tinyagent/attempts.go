package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/vicgupta/tinyagent/internal/attempts"
	"github.com/vicgupta/tinyagent/internal/config"
)

// attemptFlags are the best-of-N flags (--attempts and friends).
type attemptFlags struct {
	n            int
	models       stringList
	checks       stringList
	checkTimeout int
}

// stringList is a repeatable string flag.
type stringList []string

func (l *stringList) String() string { return strings.Join(*l, ",") }
func (l *stringList) Set(v string) error {
	if strings.TrimSpace(v) == "" {
		return errors.New("empty value")
	}
	*l = append(*l, v)
	return nil
}

func registerAttemptFlags(fs *flag.FlagSet, a *attemptFlags) {
	fs.IntVar(&a.n, "attempts", 1, "run the task up to N times (max 4), each in its own worktree, and keep the first attempt whose --attempt-check commands pass")
	fs.Var(&a.models, "attempt-model", "with --attempts: model spec for the next attempt (repeat once per attempt; default: -m for every attempt)")
	fs.Var(&a.checks, "attempt-check", "with --attempts: shell command that must exit 0 in an attempt's worktree for it to pass (repeatable; replaces attempts.checks from the config)")
	fs.IntVar(&a.checkTimeout, "attempt-check-timeout", attempts.DefaultCheckTimeout, "with --attempts: seconds before one --attempt-check command is killed (1-3600)")
}

// attemptsGroup lists the --attempts flags in --help --verbose.
var attemptsGroup = struct {
	name  string
	flags [][3]string
}{"Attempts", [][3]string{{"", "attempts", "n"}, {"", "attempt-model", "spec"}, {"", "attempt-check", "cmd"}, {"", "attempt-check-timeout", "seconds"}}}

// attemptsActive reports whether any best-of-N flag was given.
func attemptsActive(fs *flag.FlagSet) bool {
	return flagWasSet(fs, "attempts", "attempt-model", "attempt-check", "attempt-check-timeout")
}

// attemptConflicts can't be combined with --attempts: each attempt is a
// fresh run in its own worktree. --check is listed for runs; with
// --attempts it prints the plan instead (see checkAttempts). --json is
// forwarded to every attempt, whose events are tagged with "attempt".
var attemptConflicts = []string{"resume", "rewind", "to", "transcript", "acp", "review", "review-base",
	"review-commit", "read-only", "ask-user", "snapshots", "eval", "workflow", "check",
	"list-profiles", "init-config", "update", "version", "follow-up"}

// attemptOwn are flags --attempts consumes itself; every other flag set on
// the command line is forwarded to each attempt's child run.
var attemptOwn = []string{"attempts", "attempt-model", "attempt-check", "attempt-check-timeout",
	"m", "model", "o", "output", "worktree", "cost-limit", "token-limit", "time-limit"}

func runAttempts(o *options, fs *flag.FlagSet, args []string, stdout, stderr io.Writer) int {
	a := o.attempts
	usage := func(format string, v ...any) int {
		fmt.Fprintf(fs.Output(), "error: "+format+"\n", v...)
		return 2
	}
	cfg, err := config.LoadProfile(config.Find(o.configPath), o.profile)
	if err != nil {
		return usage("%v", err)
	}
	applySandboxFlags(fs, &cfg, *o)
	if msg := envConflict(cfg); msg != "" {
		return usage("%s", msg)
	}
	plan, err := planAttempts(o, fs, cfg, false)
	if err != nil {
		return usage("%v", err)
	}
	models, base, top := plan.models, plan.base, plan.top
	// Each attempt's child run would print the dirty-checkout warning;
	// print it once here and drop it from the children's stderr.
	dirty, _ := git("-C", top, "status", "--porcelain", "--", ".", ":(exclude).tinyagent")
	red, _ := newSecretRedactor(cfg, os.Environ(), io.Discard, false)

	exe, err := os.Executable()
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	checks := plan.checks
	shown := make([]string, len(models))
	for i, m := range models {
		shown[i] = m
		if m == "" {
			shown[i] = "(default model)"
		}
	}
	if dirty != "" {
		fmt.Fprintln(stderr, dirtyWorktreeWarning+" (this applies to every attempt)")
	}
	fmt.Fprintf(stderr, "attempts: up to %d runs (%s), each in worktree %s-a<N>; this can cost up to %dx a single run\n",
		a.n, strings.Join(shown, ", "), base, a.n)
	if !cfg.Attempts.StopsOnPass() || cfg.Attempts.KeepMode() != config.AttemptsKeepAll {
		fmt.Fprintf(stderr, "attempts: stop_on_pass %t, keep %s\n", cfg.Attempts.StopsOnPass(), cfg.Attempts.KeepMode())
	}

	if place := attemptCheckPlace(cfg); place != "host" {
		fmt.Fprintf(stderr, "attempts: checks run in the %s, like the attempts' commands\n", place)
	}

	link, err := startAttemptsLink(fs, *o, cfg, len(models), stderr)
	if err != nil {
		return usage("%v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	run := attempts.ExecAgent(exe)
	var events io.Writer
	if o.jsonOutput {
		events = stdout
	}
	rep, code := attempts.Run(ctx, attempts.Options{
		Events:     events,
		AttemptsID: link.id, TraceParent: link.traceParent(),
		CheckEnv: attemptCheckEnv(fs, *o, cfg, stderr),
		Models:   models, Checks: checks, BaseName: base, Output: o.output,
		CostLimit: o.costLimit, TokenLimit: o.tokenLimit, TimeLimit: time.Duration(o.timeLimit) * time.Second,
		ExtraArgs: forwardArgs(fs, args, attemptOwn), Stderr: stderr, Stdout: stdout,
		Redact: red.Redact, RunAll: !cfg.Attempts.StopsOnPass(), Keep: cfg.Attempts.KeepMode(),
		Remove: func(at attempts.Attempt) error { return removeAttemptWorktree(top, base, at) },
		RunAgent: func(ctx context.Context, args []string, out, errw io.Writer) error {
			// An empty model means the child's own default: drop "-m ''".
			for i := 0; i+1 < len(args); i++ {
				if args[i] == "-m" && args[i+1] == "" {
					args = append(args[:i:i], args[i+2:]...)
					break
				}
			}
			f := &lineDropper{w: errw, line: dirtyWorktreeWarning + "\n", atStart: true}
			err := run(ctx, link.childArgs(args), out, f)
			if ferr := f.Flush(); err == nil {
				err = ferr
			}
			return err
		},
	})
	link.finish(rep)
	return code
}

// attemptChecks returns the checks for this invocation: the --attempt-check
// commands with --attempt-check-timeout when any were given (they replace
// the config list), else attempts.checks from the config.
func attemptChecks(a attemptFlags, ca config.Attempts) []attempts.Check {
	if len(a.checks) > 0 {
		checks := make([]attempts.Check, len(a.checks))
		for i, c := range a.checks {
			checks[i] = attempts.Check{Command: c, TimeoutSeconds: a.checkTimeout}
		}
		return checks
	}
	checks := make([]attempts.Check, len(ca.Checks))
	for i, c := range ca.Checks {
		t := c.TimeoutSeconds
		if t == 0 {
			t = config.DefaultAttemptCheckTimeout
		}
		checks[i] = attempts.Check{Command: c.Command, TimeoutSeconds: t}
	}
	return checks
}

// removeAttemptWorktree removes a non-winning attempt's worktree and branch
// for attempts.keep, but only when they are exactly the ones this
// invocation created (<top>/.tinyagent/worktrees/<base>-a<N> on branch
// tinyagent/<base>-a<N>).
func removeAttemptWorktree(top, base string, at attempts.Attempt) error {
	name := base + "-a" + strconv.Itoa(at.Attempt)
	wantPath := filepath.Join(top, worktreeDir, name)
	wantBranch := worktreeBranchPrefix + name
	got, err := canonicalRoot(at.Worktree)
	if err != nil {
		return err
	}
	if filepath.Clean(got) != filepath.Clean(wantPath) || at.Branch != wantBranch {
		return fmt.Errorf("worktree %s (branch %s) is not %s on %s; left in place", at.Worktree, at.Branch, wantPath, wantBranch)
	}
	return attempts.RemoveWorktree(top, wantPath, wantBranch)
}

// preflightAttemptWorktrees checks, before any attempt runs, that cwd is in
// a Git repository with a commit and that every attempt's worktree path and
// branch are free. It returns the repository's top-level directory.
func preflightAttemptWorktrees(cwd, base string, n int) (string, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", errors.New("--attempts requires git on PATH")
	}
	root, err := canonicalRoot(cwd)
	if err != nil {
		return "", fmt.Errorf("--cwd %s: %w", cwd, err)
	}
	top, err := git("-C", root, "rev-parse", "--show-toplevel")
	if err != nil || top == "" {
		return "", fmt.Errorf("--attempts: --cwd %s is not inside a Git work tree", cwd)
	}
	if top, err = canonicalRoot(top); err != nil {
		return "", fmt.Errorf("--attempts: %w", err)
	}
	if c, err := git("-C", top, "rev-parse", "--verify", "--quiet", "HEAD^{commit}"); err != nil || c == "" {
		return "", fmt.Errorf("--attempts: the repository at %s has no commits", top)
	}
	for i := 1; i <= n; i++ {
		name := base + "-a" + strconv.Itoa(i)
		p := filepath.Join(top, worktreeDir, name)
		if _, err := os.Lstat(p); err == nil {
			return "", fmt.Errorf("--attempts: %s already exists; choose another --worktree name", p)
		}
		if _, err := git("-C", top, "rev-parse", "--verify", "--quiet", "refs/heads/"+worktreeBranchPrefix+name); err == nil {
			return "", fmt.Errorf("--attempts: branch %s%s already exists; choose another --worktree name", worktreeBranchPrefix, name)
		}
	}
	return top, nil
}
