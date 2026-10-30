package eval

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Outcomes recorded per trial.
const (
	OutcomePassed      = "passed"
	OutcomeFailedCheck = "failed_check"
	OutcomeLimit       = "limit"
	OutcomeNeedsInput  = "NeedsInput"
	OutcomeError       = "error"
	OutcomeSkipped     = "skipped"
)

// MaxCheckOutput caps the check output kept in results.jsonl (the tail is kept).
const MaxCheckOutput = 4096

// SchemaVersion of results.jsonl records and report.json.
const SchemaVersion = 1

// Options configures one eval run.
type Options struct {
	Suite       *Suite
	SuitePath   string
	OutDir      string
	Keep        bool    // keep worktrees after each trial
	CostLimit   float64 // suite-wide USD budget; 0 = none
	TimeLimit   time.Duration
	MinPassRate float64  // < 0 = no gate
	ExtraArgs   []string // flags forwarded to every child run
	Version     string
	Stderr      io.Writer
	// RunAgent runs one child tinyagent with args, writing its output to log.
	RunAgent func(ctx context.Context, args []string, log io.Writer) error
}

// ExecAgent returns a RunAgent that runs exe as a child process. On context
// cancellation the child receives an interrupt so it can save its trajectory.
func ExecAgent(exe string) func(context.Context, []string, io.Writer) error {
	return func(ctx context.Context, args []string, log io.Writer) error {
		cmd := exec.CommandContext(ctx, exe, args...)
		cmd.Stdin = nil
		cmd.Stdout, cmd.Stderr = log, log
		cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
		cmd.WaitDelay = 30 * time.Second
		return cmd.Run()
	}
}

// CheckResult is one check's outcome in a result record.
type CheckResult struct {
	Argv       []string `json:"argv"`
	ExitCode   int      `json:"exit_code"`
	DurationMS int64    `json:"duration_ms"`
	TimedOut   bool     `json:"timed_out,omitempty"`
	Output     string   `json:"output,omitempty"`
}

// Record is one line of results.jsonl.
type Record struct {
	SchemaVersion    int           `json:"schema_version"`
	TrialID          string        `json:"trial_id"`
	TaskID           string        `json:"task_id"`
	Trial            int           `json:"trial"`
	Model            string        `json:"model"`
	Provider         string        `json:"provider,omitempty"`
	ActiveModel      string        `json:"active_model,omitempty"`
	FallbackModel    string        `json:"fallback_model,omitempty"`
	Profile          string        `json:"profile"`
	TinyagentVersion string        `json:"tinyagent_version"`
	BaseCommit       string        `json:"base_commit"`
	Outcome          string        `json:"outcome"`
	ExitStatus       string        `json:"exit_status,omitempty"`
	Checks           []CheckResult `json:"checks"`
	CostUSD          float64       `json:"cost_usd"`
	InputTokens      int           `json:"input_tokens"`
	OutputTokens     int           `json:"output_tokens"`
	Steps            int           `json:"steps"`
	WallTimeMS       int64         `json:"wall_time_ms"`
	Trajectory       string        `json:"trajectory,omitempty"`
	Log              string        `json:"log,omitempty"`
	Worktree         string        `json:"worktree,omitempty"`
	Error            string        `json:"error,omitempty"`
	SkipReason       string        `json:"skip_reason,omitempty"`
}

type trajSummary struct {
	ExitStatus   string  `json:"exit_status"`
	Steps        int     `json:"steps"`
	Cost         float64 `json:"cost"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	ActiveModel  string  `json:"active_model"`
}

// Run executes the suite sequentially and writes results.jsonl, report.json
// and report.md to OutDir. It returns the exit code: 0 on success, 1 when the
// pass-rate gate fails or the harness fails, 130 when interrupted.
func Run(ctx context.Context, o Options) int {
	trials := o.Suite.Expand()
	for _, d := range []string{o.OutDir, filepath.Join(o.OutDir, "trajectories"), filepath.Join(o.OutDir, "logs")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			fmt.Fprintf(o.Stderr, "tinyagent: eval: %v\n", err)
			return 1
		}
	}
	resPath := filepath.Join(o.OutDir, "results.jsonl")
	rf, err := os.OpenFile(resPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		fmt.Fprintf(o.Stderr, "tinyagent: eval: %v\n", err)
		return 1
	}
	defer rf.Close()
	enc := json.NewEncoder(rf)

	start := time.Now()
	var spent float64
	var recs []Record
	stopReason := ""
	for i, tr := range trials {
		var rec Record
		if stopReason == "" {
			switch {
			case ctx.Err() != nil:
				stopReason = "interrupted"
			case o.CostLimit > 0 && spent >= o.CostLimit:
				stopReason = fmt.Sprintf("suite cost limit $%.4f reached", o.CostLimit)
			case o.TimeLimit > 0 && time.Since(start) >= o.TimeLimit:
				stopReason = fmt.Sprintf("suite time limit %s reached", o.TimeLimit)
			}
		}
		if stopReason != "" {
			rec = baseRecord(o, tr)
			rec.Outcome, rec.SkipReason = OutcomeSkipped, stopReason
		} else {
			rec = runTrial(ctx, o, tr)
			spent += rec.CostUSD
		}
		recs = append(recs, rec)
		if err := enc.Encode(rec); err != nil {
			fmt.Fprintf(o.Stderr, "tinyagent: eval: write %s: %v\n", resPath, err)
			return 1
		}
		if rec.Outcome != OutcomeSkipped {
			fmt.Fprintf(o.Stderr, "[%d/%d] %s: %s (%s) $%.4f %d steps %.1fs\n", i+1, len(trials), tr.ID, rec.Outcome, orDash(rec.ExitStatus), rec.CostUSD, rec.Steps, float64(rec.WallTimeMS)/1000)
		}
	}
	if stopReason != "" {
		fmt.Fprintf(o.Stderr, "tinyagent: eval: stopped early: %s; remaining trials recorded as skipped\n", stopReason)
	}

	rep := BuildReport(o, recs)
	if err := writeReports(o.OutDir, rep); err != nil {
		fmt.Fprintf(o.Stderr, "tinyagent: eval: %v\n", err)
		return 1
	}
	fmt.Fprint(o.Stderr, summaryTable(rep))
	fmt.Fprintf(o.Stderr, "results: %s\n", resPath)
	if ctx.Err() != nil {
		return 130
	}
	if len(rep.BelowThreshold) > 0 {
		fmt.Fprintf(o.Stderr, "tinyagent: eval: pass rate below %.2f for: %s\n", o.MinPassRate, strings.Join(rep.BelowThreshold, ", "))
		return 1
	}
	return 0
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func baseRecord(o Options, tr Trial) Record {
	r := Record{
		SchemaVersion: SchemaVersion, TrialID: tr.ID, TaskID: tr.Task.ID, Trial: tr.N,
		Model: tr.Model, Profile: tr.Profile, TinyagentVersion: o.Version,
		BaseCommit: tr.Task.baseCommit, Checks: []CheckResult{},
	}
	if p, _, ok := strings.Cut(tr.Model, "/"); ok {
		r.Provider = p
	}
	return r
}

func runTrial(ctx context.Context, o Options, tr Trial) (rec Record) {
	rec = baseRecord(o, tr)
	t0 := time.Now()
	defer func() { rec.WallTimeMS = time.Since(t0).Milliseconds() }()

	wt, cleanup, err := addWorktree(tr.Task.repoAbs, tr.Task.baseCommit, tr.ID)
	if err != nil {
		rec.Outcome, rec.Error = OutcomeError, err.Error()
		return rec
	}
	if o.Keep {
		rec.Worktree = wt
	} else {
		defer cleanup()
	}

	traj, _ := filepath.Abs(filepath.Join(o.OutDir, "trajectories", tr.ID+".json"))
	logPath, _ := filepath.Abs(filepath.Join(o.OutDir, "logs", tr.ID+".log"))
	args := append([]string{}, o.ExtraArgs...)
	args = append(args, "-m", tr.Model, "--cwd", wt, "-o", traj)
	if tr.Profile != "default" || o.Suite.defaultProfile {
		args = append(args, "--profile", tr.Profile)
	}
	l := tr.Task.Limits
	if l.StepLimit > 0 {
		args = append(args, "--step-limit", strconv.Itoa(l.StepLimit))
	}
	if l.CostLimit > 0 {
		args = append(args, "--cost-limit", strconv.FormatFloat(l.CostLimit, 'g', -1, 64))
	}
	if l.TokenLimit > 0 {
		args = append(args, "--token-limit", strconv.Itoa(l.TokenLimit))
	}
	if l.TimeLimit > 0 {
		args = append(args, "--time-limit", strconv.Itoa(l.TimeLimit))
	}
	args = append(args, "-t", tr.Task.prompt)

	lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		rec.Outcome, rec.Error = OutcomeError, err.Error()
		return rec
	}
	runErr := o.RunAgent(ctx, args, lf)
	lf.Close()
	rec.Log = logPath

	b, err := os.ReadFile(traj)
	if err != nil {
		rec.Outcome = OutcomeError
		rec.Error = "no trajectory written"
		if runErr != nil {
			rec.Error += ": " + runErr.Error()
		}
		return rec
	}
	var ts trajSummary
	if err := json.Unmarshal(b, &ts); err != nil {
		rec.Outcome, rec.Error = OutcomeError, "read trajectory: "+err.Error()
		return rec
	}
	rec.Trajectory = traj
	rec.ExitStatus, rec.Steps, rec.CostUSD = ts.ExitStatus, ts.Steps, ts.Cost
	rec.InputTokens, rec.OutputTokens = ts.InputTokens, ts.OutputTokens
	rec.ActiveModel = ts.ActiveModel
	if ts.ActiveModel != "" && ts.ActiveModel != tr.Model {
		rec.FallbackModel = ts.ActiveModel
	}

	switch ts.ExitStatus {
	case "Submitted", "Completed":
		rec.Outcome = OutcomePassed
	case "StepLimitExceeded", "CostLimitExceeded", "TokenLimitExceeded", "TimeLimitExceeded":
		rec.Outcome = OutcomeLimit
		if !tr.Task.checksOnLimit() {
			return rec
		}
	case "NeedsInput":
		rec.Outcome = OutcomeNeedsInput
		return rec
	default:
		rec.Outcome = OutcomeError
		if runErr != nil {
			rec.Error = runErr.Error()
		}
		return rec
	}
	if ctx.Err() != nil {
		return rec
	}
	allPass := true
	for _, c := range tr.Task.Checks {
		cr := runCheck(ctx, wt, c)
		rec.Checks = append(rec.Checks, cr)
		if cr.ExitCode != 0 {
			allPass = false
		}
	}
	if rec.Outcome == OutcomePassed && !allPass {
		rec.Outcome = OutcomeFailedCheck
	}
	return rec
}

// addWorktree checks out base, detached, in a fresh temporary directory.
func addWorktree(repo, base, id string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "tinyagent-eval-")
	if err != nil {
		return "", nil, err
	}
	wt := filepath.Join(dir, id)
	cmd := exec.Command("git", "-C", repo, "worktree", "add", "--detach", wt, base)
	if out, err := cmd.CombinedOutput(); err != nil {
		os.RemoveAll(dir)
		return "", nil, fmt.Errorf("git worktree add: %v: %s", err, strings.TrimSpace(string(out)))
	}
	cleanup := func() {
		exec.Command("git", "-C", repo, "worktree", "remove", "--force", wt).Run()
		os.RemoveAll(dir)
		exec.Command("git", "-C", repo, "worktree", "prune").Run()
	}
	return wt, cleanup, nil
}

type tailBuffer struct {
	buf bytes.Buffer
}

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf.Write(p)
	if t.buf.Len() > 2*MaxCheckOutput {
		b := append([]byte(nil), t.buf.Bytes()[t.buf.Len()-MaxCheckOutput:]...)
		t.buf.Reset()
		t.buf.Write(b)
	}
	return len(p), nil
}

func (t *tailBuffer) String() string {
	b := t.buf.Bytes()
	if len(b) > MaxCheckOutput {
		return "…" + string(b[len(b)-MaxCheckOutput:])
	}
	return string(b)
}

func runCheck(ctx context.Context, dir string, c Check) CheckResult {
	cr := CheckResult{Argv: c.Argv}
	cctx, cancel := context.WithTimeout(ctx, time.Duration(c.TimeoutSeconds)*time.Second)
	defer cancel()
	var out tailBuffer
	cmd := exec.CommandContext(cctx, c.Argv[0], c.Argv[1:]...)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.WaitDelay = 5 * time.Second
	t0 := time.Now()
	err := cmd.Run()
	cr.DurationMS = time.Since(t0).Milliseconds()
	cr.Output = out.String()
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.Is(cctx.Err(), context.DeadlineExceeded):
		cr.ExitCode, cr.TimedOut = -1, true
	case errors.As(err, &ee) && ee.ExitCode() > 0:
		cr.ExitCode = ee.ExitCode()
	default:
		cr.ExitCode = -1
		if cr.Output == "" {
			cr.Output = err.Error()
		}
	}
	return cr
}
