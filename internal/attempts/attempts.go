// Package attempts runs one task up to N times (best-of-N), each attempt
// as a child tinyagent run in its own Git worktree, grades every finished
// attempt with user-configured check commands, and picks a verified
// winner: the lowest-numbered attempt whose run ended Submitted or
// Completed and whose checks all exited 0.
//
// Attempts run sequentially and, by default, stop at the first pass
// (RunAll runs every attempt). tinyagent never commits, merges or pushes an
// attempt. Every worktree is kept unless Keep asks to remove non-winners
// once an attempt wins.
package attempts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// SchemaVersion of the attempts report.
const SchemaVersion = 1

// MaxAttempts is the largest --attempts value.
const MaxAttempts = 4

// DefaultCheckTimeout and MaxCheckTimeout bound one check, in seconds.
const (
	DefaultCheckTimeout = 600
	MaxCheckTimeout     = 3600
)

// MaxCheckOutput caps the check output kept in the report (the tail is kept).
const MaxCheckOutput = 8 << 10

// Stop reasons recorded in the report.
const (
	StopPassed    = "passed"
	StopExhausted = "exhausted"
	StopBudget    = "budget exhausted"
	StopInterrupt = "interrupted"
	StopError     = "error"
	// StopCompleted: RunAll ran every attempt and at least one passed.
	StopCompleted = "completed"
)

// Keep modes: which non-winning worktrees survive once an attempt wins.
const (
	KeepAll    = "all"
	KeepFailed = "failed"
	KeepNone   = "none"
)

// Check is one verification command, run with bash -c in the attempt's
// working directory.
type Check struct {
	Command        string `json:"command"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

// Options configures one best-of-N invocation.
type Options struct {
	// Models holds one model spec per attempt; its length is N.
	Models []string
	Checks []Check
	// BaseName names the worktrees: attempt i uses <BaseName>-a<i>.
	BaseName string
	// Output is the -o path. Attempt i's trajectory goes to <stem>.a<i><ext>
	// and the report to <stem>.attempts<ext>. Empty means trajectories go to
	// a temporary directory and no report file is written.
	Output    string
	CostLimit float64       // invocation-wide USD budget; 0 = none
	TimeLimit time.Duration // invocation-wide wall-clock budget; 0 = none
	// ExtraArgs are forwarded to every child run (the task among them).
	ExtraArgs []string
	Stderr    io.Writer
	// Stdout receives the winner's stdout (its submission) at the end.
	Stdout io.Writer
	// Redact masks secrets in check output; nil leaves it unchanged.
	Redact func(string) string
	// RunAgent runs one child tinyagent with args. Its stdout is buffered
	// per attempt; its stderr is passed through.
	RunAgent func(ctx context.Context, args []string, stdout, stderr io.Writer) error
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// RunAll runs every attempt instead of stopping at the first pass. The
	// lowest-numbered passing attempt still wins.
	RunAll bool
	// Keep is KeepAll (or empty), KeepFailed or KeepNone. With a winner,
	// KeepFailed removes passing non-winners and KeepNone removes every
	// non-winner through Remove. Nothing is removed without a winner.
	Keep string
	// Remove deletes one attempt's worktree and branch. Nil disables
	// cleanup.
	Remove func(a Attempt) error
	// Events, when non-nil, receives JSONL events (--json): each child
	// run's stdout events tagged with "attempt", plus attempt.started,
	// attempt.check, attempt.result and one final attempts.result. The
	// winner's stdout is then not replayed to Stdout.
	Events io.Writer
	// CheckEnv, when non-nil, prepares where one attempt's checks run
	// (the run's sandbox, container or command_env) for the attempt's
	// working directory, and returns a release function. An error fails
	// every check of that attempt (fail closed); nil runs checks on the
	// host with tinyagent's environment.
	CheckEnv func(ctx context.Context, root string) (CheckRunner, func(), error)
}

// CheckRunner builds an unstarted `bash -c command` in root. Cancelling ctx
// must kill the command and everything it spawned.
type CheckRunner interface {
	Command(ctx context.Context, root, command string) *exec.Cmd
}

// ExecAgent returns a RunAgent that runs exe as a child process. On context
// cancellation the child gets an interrupt so it can end Interrupted and
// save its trajectory.
func ExecAgent(exe string) func(context.Context, []string, io.Writer, io.Writer) error {
	return func(ctx context.Context, args []string, stdout, stderr io.Writer) error {
		cmd := exec.CommandContext(ctx, exe, args...)
		cmd.Stdin = nil
		cmd.Stdout, cmd.Stderr = stdout, stderr
		cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
		cmd.WaitDelay = 30 * time.Second
		return cmd.Run()
	}
}

// CheckResult is one check's outcome for one attempt.
type CheckResult struct {
	Command    string `json:"command"`
	ExitCode   int    `json:"exit_code"`
	DurationMS int64  `json:"duration_ms"`
	TimedOut   bool   `json:"timed_out"`
	Skipped    bool   `json:"skipped"`
	Output     string `json:"output"`
}

// Attempt is one attempt's entry in the report.
type Attempt struct {
	Attempt      int           `json:"attempt"`
	Model        string        `json:"model"`
	Status       string        `json:"status"`
	Passed       bool          `json:"passed"`
	Steps        int           `json:"steps"`
	InputTokens  int           `json:"input_tokens"`
	OutputTokens int           `json:"output_tokens"`
	CostUSD      float64       `json:"cost_usd"`
	WallTimeMS   int64         `json:"wall_time_ms"`
	Worktree     string        `json:"worktree"`
	Branch       string        `json:"branch"`
	FilesChanged int           `json:"files_changed"`
	Trajectory   string        `json:"trajectory,omitempty"`
	Checks       []CheckResult `json:"checks"`
	Error        string        `json:"error,omitempty"`
	// Removed is true when Keep cleanup deleted the worktree and branch.
	Removed bool `json:"removed,omitempty"`

	root   string
	stdout string
}

// Report is the attempts report (<stem>.attempts.json).
type Report struct {
	SchemaVersion int       `json:"schema_version"`
	Requested     int       `json:"attempts_requested"`
	Winner        *int      `json:"winner"`
	WinnerBranch  string    `json:"winner_branch,omitempty"`
	StopReason    string    `json:"stop_reason"`
	StopOnPass    bool      `json:"stop_on_pass"`
	Keep          string    `json:"keep"`
	Attempts      []Attempt `json:"attempts"`
	TotalCostUSD  float64   `json:"total_cost_usd"`
	TotalWallMS   int64     `json:"total_wall_time_ms"`
}

// TrajectoryPath returns attempt i's trajectory path for the -o path out.
func TrajectoryPath(out string, i int) string {
	return withSuffix(out, ".a"+strconv.Itoa(i))
}

// ReportPath returns the attempts report path for the -o path out.
func ReportPath(out string) string { return withSuffix(out, ".attempts") }

func withSuffix(out, suffix string) string {
	ext := filepath.Ext(out)
	if ext == "" {
		ext = ".json"
	}
	return strings.TrimSuffix(out, filepath.Ext(out)) + suffix + ext
}

type trajSummary struct {
	ExitStatus   string  `json:"exit_status"`
	Steps        int     `json:"steps"`
	Cost         float64 `json:"cost"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	Root         string  `json:"root"`
	Worktree     *struct {
		Path       string `json:"path"`
		Branch     string `json:"branch"`
		BaseCommit string `json:"base_commit"`
	} `json:"worktree"`
}

// Run executes the attempts and returns the report and the exit code: 0
// when an attempt passed, 1 otherwise.
func Run(ctx context.Context, o Options) (Report, int) {
	now := o.Now
	if now == nil {
		now = time.Now
	}
	keep := o.Keep
	if keep == "" {
		keep = KeepAll
	}
	rep := Report{SchemaVersion: SchemaVersion, Requested: len(o.Models), Attempts: []Attempt{},
		StopOnPass: !o.RunAll, Keep: keep}
	start := now()
	var ev *eventWriter
	if o.Events != nil {
		ev = &eventWriter{out: o.Events}
	}
	trajDir := ""
	if o.Output == "" {
		d, err := os.MkdirTemp("", "tinyagent-attempts-")
		if err != nil {
			fmt.Fprintf(o.Stderr, "tinyagent: attempts: %v\n", err)
			rep.StopReason = StopError
			ev.emit(EventAttemptsResult, attemptsResultFields(rep, ""))
			return rep, 1
		}
		trajDir = d
		defer os.RemoveAll(d)
	}
	for i, m := range o.Models {
		n := i + 1
		var remCost float64
		var remTime time.Duration
		if o.CostLimit > 0 {
			if remCost = o.CostLimit - rep.TotalCostUSD; remCost <= 0 {
				rep.StopReason = StopBudget
				break
			}
		}
		if o.TimeLimit > 0 {
			if remTime = o.TimeLimit - now().Sub(start); remTime < time.Second {
				rep.StopReason = StopBudget
				break
			}
		}
		if ctx.Err() != nil {
			rep.StopReason = StopInterrupt
			break
		}
		traj := ""
		if trajDir != "" {
			traj = filepath.Join(trajDir, "a"+strconv.Itoa(n)+".json")
		} else {
			traj, _ = filepath.Abs(TrajectoryPath(o.Output, n))
		}
		a := runAttempt(ctx, o, ev, n, m, traj, remCost, remTime, now)
		if trajDir != "" {
			a.Trajectory = ""
		}
		rep.Attempts = append(rep.Attempts, a)
		rep.TotalCostUSD += a.CostUSD
		fmt.Fprintln(o.Stderr, attemptLine(a))
		ev.emit(EventAttemptResult, attemptResultFields(a))
		switch {
		case a.Passed:
			if rep.Winner == nil {
				w := n
				rep.Winner, rep.WinnerBranch = &w, a.Branch
			}
			if !o.RunAll {
				rep.StopReason = StopPassed
			}
		case a.Status == "Interrupted" || ctx.Err() != nil:
			rep.StopReason = StopInterrupt
		case a.Status == "CostLimitExceeded" && o.CostLimit > 0, a.Status == "TimeLimitExceeded" && o.TimeLimit > 0:
			rep.StopReason = StopBudget
		case a.Error != "" && a.Status == "":
			rep.StopReason = StopError
		}
		if rep.StopReason != "" {
			break
		}
	}
	if rep.StopReason == "" {
		rep.StopReason = StopExhausted
		if rep.Winner != nil {
			rep.StopReason = StopCompleted
		}
	}
	cleanup(&rep, o)
	rep.TotalWallMS = now().Sub(start).Milliseconds()

	if rep.Winner != nil {
		w := rep.Attempts[*rep.Winner-1]
		fmt.Fprintf(o.Stderr, "winner: attempt %d (branch %s); review with: git -C %s diff HEAD\n", w.Attempt, w.Branch, w.Worktree)
		if o.Stdout != nil && o.Events == nil {
			io.WriteString(o.Stdout, w.stdout)
		}
	} else {
		fmt.Fprintf(o.Stderr, "no attempt passed (%s); worktrees kept for inspection\n", rep.StopReason)
	}
	reportPath := ""
	if o.Output != "" {
		p := ReportPath(o.Output)
		b, _ := json.MarshalIndent(rep, "", "  ")
		if err := os.WriteFile(p, append(b, '\n'), 0o600); err != nil {
			fmt.Fprintf(o.Stderr, "tinyagent: attempts: write %s: %v\n", p, err)
			ev.emit(EventAttemptsResult, attemptsResultFields(rep, ""))
			return rep, 1
		}
		fmt.Fprintf(o.Stderr, "attempts report: %s\n", p)
		reportPath, _ = filepath.Abs(p)
	}
	ev.emit(EventAttemptsResult, attemptsResultFields(rep, reportPath))
	if ev != nil && ev.err != nil {
		fmt.Fprintf(o.Stderr, "tinyagent: attempts: write --json events: %v\n", ev.err)
		return rep, 1
	}
	if rep.Winner != nil {
		return rep, 0
	}
	return rep, 1
}

func attemptLine(a Attempt) string {
	checks := "checks: PASS"
	switch {
	case a.Error != "" && a.Status == "":
		checks = "error: " + a.Error
	case len(a.Checks) > 0 && a.Checks[0].Skipped:
		checks = "checks: skipped"
	case !a.Passed:
		for _, c := range a.Checks {
			if c.ExitCode != 0 || c.TimedOut {
				why := "exit " + strconv.Itoa(c.ExitCode)
				if c.TimedOut {
					why = "timed out"
				}
				checks = fmt.Sprintf("checks: FAIL (%s %s)", c.Command, why)
				break
			}
		}
	}
	status := a.Status
	if status == "" {
		status = "-"
	}
	wt := a.Worktree
	if wt == "" {
		wt = "-"
	}
	return fmt.Sprintf("attempt %d  %s  %s  %s  $%.4f  %s", a.Attempt, a.Model, status, checks, a.CostUSD, wt)
}

func runAttempt(ctx context.Context, o Options, ev *eventWriter, n int, m, traj string, remCost float64, remTime time.Duration, now func() time.Time) (a Attempt) {
	a = Attempt{Attempt: n, Model: m, Trajectory: traj, Checks: []CheckResult{}}
	t0 := now()
	defer func() { a.WallTimeMS = now().Sub(t0).Milliseconds() }()

	args := append([]string{}, o.ExtraArgs...)
	args = append(args, "-m", m, "--worktree="+o.BaseName+"-a"+strconv.Itoa(n), "-o", traj)
	if remCost > 0 {
		args = append(args, "--cost-limit", strconv.FormatFloat(remCost, 'g', -1, 64))
	}
	if remTime > 0 {
		args = append(args, "--time-limit", strconv.Itoa(int(math.Floor(remTime.Seconds()))))
	}
	fmt.Fprintf(o.Stderr, "attempt %d/%d: %s\n", n, len(o.Models), m)
	ev.emit(EventAttemptStarted, attemptStartedFields(n, len(o.Models), m, o.BaseName+"-a"+strconv.Itoa(n)))
	var out strings.Builder
	var childOut io.Writer = &out
	var tag *tagWriter
	if o.Events != nil {
		tag = &tagWriter{out: o.Events, attempt: n}
		childOut = tag
	}
	runErr := o.RunAgent(ctx, args, childOut, o.Stderr)
	if tag != nil {
		if err := tag.Flush(); err != nil && ev.err == nil {
			ev.err = err
		}
	}
	a.stdout = out.String()

	b, err := os.ReadFile(traj)
	if err != nil {
		a.Error = "no trajectory written"
		if runErr != nil {
			a.Error += ": " + runErr.Error()
		}
		return a
	}
	var ts trajSummary
	if err := json.Unmarshal(b, &ts); err != nil {
		a.Error = "read trajectory: " + err.Error()
		return a
	}
	a.Status, a.Steps, a.CostUSD = ts.ExitStatus, ts.Steps, ts.Cost
	a.InputTokens, a.OutputTokens = ts.InputTokens, ts.OutputTokens
	a.root = ts.Root
	if ts.Worktree != nil {
		a.Worktree, a.Branch = ts.Worktree.Path, ts.Worktree.Branch
		a.FilesChanged = filesChanged(ts.Worktree.Path)
	}
	if a.root == "" {
		a.root = a.Worktree
	}

	ok := ts.ExitStatus == "Submitted" || ts.ExitStatus == "Completed"
	if !ok || ctx.Err() != nil || a.root == "" {
		for _, c := range o.Checks {
			cr := CheckResult{Command: c.Command, ExitCode: -1, Skipped: true}
			a.Checks = append(a.Checks, cr)
			ev.emit(EventAttemptCheck, checkFields(n, cr, o.Redact))
		}
		if ok && a.root == "" && ctx.Err() == nil {
			a.Error = "trajectory has no working directory"
		}
		return a
	}
	var runner CheckRunner
	if o.CheckEnv != nil {
		r, release, err := o.CheckEnv(ctx, a.root)
		if err != nil {
			msg := "check environment: " + err.Error()
			if o.Redact != nil {
				msg = o.Redact(msg)
			}
			for _, c := range o.Checks {
				cr := CheckResult{Command: c.Command, ExitCode: -1, Output: capTail(msg)}
				a.Checks = append(a.Checks, cr)
				ev.emit(EventAttemptCheck, checkFields(n, cr, o.Redact))
			}
			a.Error = msg
			return a
		}
		if release != nil {
			defer release()
		}
		runner = r
	}
	pass := true
	for _, c := range o.Checks {
		cr := runCheck(ctx, runner, a.root, c, o.Redact)
		a.Checks = append(a.Checks, cr)
		ev.emit(EventAttemptCheck, checkFields(n, cr, o.Redact))
		if cr.ExitCode != 0 || cr.TimedOut {
			pass = false
		}
	}
	a.Passed = pass && ctx.Err() == nil
	return a
}

// filesChanged counts the paths git reports as changed or untracked in the
// worktree; -1 when git fails.
func filesChanged(dir string) int {
	cmd := exec.Command("git", "-C", dir, "status", "--porcelain", "--untracked-files=all")
	cmd.Env = gitEnv()
	b, err := cmd.Output()
	if err != nil {
		return -1
	}
	s := strings.TrimRight(string(b), "\n")
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

func gitEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		switch name {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY":
			continue
		}
		env = append(env, kv)
	}
	return env
}

type tailBuffer struct{ b []byte }

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > 4*MaxCheckOutput {
		t.b = append([]byte(nil), t.b[len(t.b)-2*MaxCheckOutput:]...)
	}
	return len(p), nil
}

// capTail keeps the last MaxCheckOutput bytes of s, marking a cut with "…".
func capTail(s string) string {
	if len(s) <= MaxCheckOutput {
		return s
	}
	return "…" + s[len(s)-MaxCheckOutput:]
}

func runCheck(ctx context.Context, runner CheckRunner, dir string, c Check, redact func(string) string) CheckResult {
	cr := CheckResult{Command: c.Command}
	timeout := c.TimeoutSeconds
	if timeout <= 0 {
		timeout = DefaultCheckTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	var out tailBuffer
	var cmd *exec.Cmd
	if runner != nil {
		cmd = runner.Command(cctx, dir, c.Command)
	} else {
		cmd = exec.CommandContext(cctx, "bash", "-c", c.Command)
		cmd.Dir = dir
		setProcessGroup(cmd)
	}
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.WaitDelay = 5 * time.Second
	t0 := time.Now()
	err := cmd.Run()
	cr.DurationMS = time.Since(t0).Milliseconds()
	s := string(out.b)
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.Is(cctx.Err(), context.DeadlineExceeded):
		cr.ExitCode, cr.TimedOut = -1, true
	case errors.As(err, &ee) && ee.ExitCode() > 0:
		cr.ExitCode = ee.ExitCode()
	default:
		cr.ExitCode = -1
		if s == "" {
			s = err.Error()
		}
	}
	if redact != nil {
		s = redact(s)
	}
	cr.Output = capTail(s)
	return cr
}

// removable reports whether keep mode keep removes non-winning attempt a.
func removable(keep string, a Attempt) bool {
	switch keep {
	case KeepNone:
		return true
	case KeepFailed:
		return a.Passed
	}
	return false
}

// cleanup removes the non-winning worktrees rep.Keep asks for. It only runs
// when an attempt won, never touches the winner, and only removes
// attempts that recorded a worktree. A failure is reported and the
// attempt is left in place.
func cleanup(rep *Report, o Options) {
	if rep.Winner == nil || o.Remove == nil || rep.Keep == KeepAll {
		return
	}
	for i := range rep.Attempts {
		a := &rep.Attempts[i]
		if a.Attempt == *rep.Winner || a.Worktree == "" || !removable(rep.Keep, *a) {
			continue
		}
		if err := o.Remove(*a); err != nil {
			fmt.Fprintf(o.Stderr, "tinyagent: attempts: keep %s: could not remove attempt %d (%s): %v\n", rep.Keep, a.Attempt, a.Worktree, err)
			continue
		}
		a.Removed = true
		fmt.Fprintf(o.Stderr, "removed attempt %d worktree %s and branch %s (keep: %s)\n", a.Attempt, a.Worktree, a.Branch, rep.Keep)
	}
}

// RemoveWorktree deletes the worktree at path and its branch from the
// repository at repoRoot, discarding the worktree's uncommitted changes.
func RemoveWorktree(repoRoot, path, branch string) error {
	run := func(args ...string) error {
		cmd := exec.Command("git", append([]string{"-C", repoRoot}, args...)...)
		cmd.Env = gitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	if err := run("worktree", "remove", "--force", path); err != nil {
		return err
	}
	if branch == "" {
		return nil
	}
	return run("branch", "-D", branch)
}
