package harness

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
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Workflow statuses.
const (
	StatusPassed      = "Passed"
	StatusFailed      = "Failed"
	StatusInterrupted = "Interrupted"
)

// MaxCheckOutput caps a check command's recorded output, and MaxFeedback
// the failure reason handed to a retry.
const (
	MaxCheckOutput = 8 << 10
	MaxFeedback    = 4 << 10
)

// RunAgent runs one child tinyagent with args, writing its output to log.
type RunAgent func(ctx context.Context, args []string, log io.Writer) error

// ExecAgent returns a RunAgent that runs exe as a child process; on
// cancellation the child gets an interrupt so it can save its trajectory.
func ExecAgent(exe string) RunAgent {
	return func(ctx context.Context, args []string, log io.Writer) error {
		cmd := exec.CommandContext(ctx, exe, args...)
		cmd.Stdin = nil
		cmd.Stdout, cmd.Stderr = log, log
		cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
		cmd.WaitDelay = 30 * time.Second
		return cmd.Run()
	}
}

// Options configure Run.
type Options struct {
	Route Route
	Task  string
	Cwd   string // absolute working directory shared by every stage
	// ExtraArgs are forwarded to every stage run before the harness's own.
	ExtraArgs []string
	// Dir receives each attempt's trajectory and log.
	Dir      string
	RunAgent RunAgent
	// Emit, if set, receives workflow.* and stage.* events.
	Emit func(kind string, fields map[string]any)
	// Progress, if set, receives one line per stage attempt.
	Progress io.Writer
}

// Check is the outcome of a stage's success check.
type Check struct {
	Method   string   `json:"method"` // "command" or "model"
	Passed   bool     `json:"passed"`
	Reason   string   `json:"reason,omitempty"`
	Argv     []string `json:"argv,omitempty"`
	ExitCode int      `json:"exit_code,omitempty"`
	TimedOut bool     `json:"timed_out,omitempty"`
	Output   string   `json:"output,omitempty"`
	// Skipped is set when a behavior stage found no behavioral criteria.
	Skipped bool `json:"skipped,omitempty"`
	// Evidence is each behavior case's observed output.
	Evidence []BehaviorResult `json:"evidence,omitempty"`
}

// Attempt is one run of one stage.
type Attempt struct {
	Stage      int     `json:"stage"` // 1-based
	Skill      string  `json:"skill"`
	Attempt    int     `json:"attempt"` // 1-based
	Input      string  `json:"input"`
	Output     string  `json:"output"`
	ExitStatus string  `json:"exit_status"`
	Check      Check   `json:"check"`
	Steps      int     `json:"steps"`
	CostUSD    float64 `json:"cost_usd"`
	Trajectory string  `json:"trajectory,omitempty"`
	Log        string  `json:"log,omitempty"`
	DurationMS int64   `json:"duration_ms"`
}

// Result is a whole workflow run; it is what --output writes.
type Result struct {
	SchemaVersion int       `json:"schema_version"`
	Kind          string    `json:"kind"` // "workflow"
	Workflow      string    `json:"workflow"`
	Route         Route     `json:"route"`
	Task          string    `json:"task"`
	Cwd           string    `json:"cwd"`
	Status        string    `json:"status"`
	FailedStage   string    `json:"failed_stage,omitempty"`
	Reason        string    `json:"reason,omitempty"`
	Output        string    `json:"output,omitempty"` // the last stage's output
	Stages        []Stage   `json:"stages"`
	Attempts      []Attempt `json:"attempts"`
	// Gates are the stage gate decisions, in order.
	Gates      []GateDecision `json:"gates,omitempty"`
	CostUSD    float64        `json:"cost_usd"`
	DurationMS int64          `json:"duration_ms"`
}

// ExitCode maps a result to the process exit code.
func (r *Result) ExitCode() int {
	switch r.Status {
	case StatusPassed:
		return 0
	case StatusInterrupted:
		return 130
	}
	return 1
}

// Run executes the routed stages in order with no pause between them. Stage
// k gets the task and stage k-1's output; a stage that fails its success
// check is retried up to its limit with the failure reason, and when
// retries run out the workflow stops without running later stages.
func Run(ctx context.Context, o Options) *Result {
	t0 := time.Now()
	p := o.Route.Pipeline
	res := &Result{SchemaVersion: 1, Kind: "workflow", Workflow: p.Name, Route: o.Route, Task: o.Task, Cwd: o.Cwd, Stages: p.Stages}
	emit := func(kind string, f map[string]any) {
		if o.Emit != nil {
			o.Emit(kind, f)
		}
	}
	emit("workflow.started", map[string]any{"workflow": p.Name, "route": o.Route, "stages": stageNames(p)})
	prev, prevName := "", ""
	for i, st := range p.Stages {
		var last Attempt
		passed := false
		feedback := ""
		n := 0 // attempts so far at this stage, across a re-plan
		// rounds is 1, or 2 when a spec gate rejects the first plan: the
		// re-plan gets the stage's full retry budget again.
		for round := 1; round <= 2; round++ {
			passed = false
			for a := 1; a <= st.Retries+1; a++ {
				if ctx.Err() != nil {
					break
				}
				n++
				input := Prompt(i+1, len(p.Stages), st, o.Task, prev, prevName, o.Cwd, feedback)
				emit("stage.started", map[string]any{"stage": i + 1, "skill": st.Skill.Name, "attempt": n, "input": input})
				last = runAttempt(ctx, o, st, i+1, n, input)
				res.Attempts = append(res.Attempts, last)
				res.CostUSD += last.CostUSD
				emit("stage.finished", attemptFields(last))
				if o.Progress != nil {
					verdict := "passed"
					if last.Check.Skipped {
						verdict = "skipped: " + oneLine(last.Check.Reason)
					} else if !last.Check.Passed {
						verdict = "failed: " + oneLine(last.Check.Reason)
					}
					fmt.Fprintf(o.Progress, "workflow %s: stage %d/%d %s attempt %d %s\n", p.Name, i+1, len(p.Stages), st.Skill.Name, n, verdict)
				}
				if last.Check.Passed {
					passed = true
					break
				}
				feedback = last.Check.Reason
			}
			if !passed || ctx.Err() != nil || st.Gate != GateSpec {
				break
			}
			g := SpecGate(last.Output, o.Cwd)
			d := GateDecision{Stage: i + 1, Skill: st.Skill.Name, Gate: st.Gate, Attempt: round, Passed: g.Passed, Reason: g.Reason, Missing: g.Missing}
			res.Gates = append(res.Gates, d)
			emit("stage.gate", map[string]any{"stage": d.Stage, "skill": d.Skill, "gate": d.Gate, "attempt": d.Attempt, "passed": d.Passed, "reason": d.Reason, "missing": d.Missing})
			if o.Progress != nil {
				verdict := "passed"
				if !g.Passed {
					verdict = "rejected: " + oneLine(g.Reason)
				}
				fmt.Fprintf(o.Progress, "workflow %s: stage %d/%d %s spec gate %s\n", p.Name, i+1, len(p.Stages), st.Skill.Name, verdict)
			}
			if g.Passed {
				break
			}
			passed = false
			last.Check.Reason = "spec gate: " + g.Reason
			feedback = "The spec gate rejected your plan: " + g.Reason + "\nEvery task needs a \"Task N: <title>\" line, an \"Acceptance criteria:\" list with at least one bullet, and a \"Verify: `<command>`\" line whose program exists."
		}
		if ctx.Err() != nil {
			res.Status, res.FailedStage, res.Reason = StatusInterrupted, st.Skill.Name, "interrupted"
			break
		}
		if !passed {
			res.Status, res.FailedStage, res.Reason = StatusFailed, st.Skill.Name, last.Check.Reason
			break
		}
		prev, prevName = last.Output, st.Skill.Name
	}
	if res.Status == "" {
		res.Status, res.Output = StatusPassed, prev
	}
	res.DurationMS = time.Since(t0).Milliseconds()
	f := map[string]any{"workflow": p.Name, "status": res.Status, "cost_usd": res.CostUSD, "duration_ms": res.DurationMS}
	if res.FailedStage != "" {
		f["failed_stage"], f["reason"] = res.FailedStage, res.Reason
	}
	emit("workflow.finished", f)
	return res
}

func stageNames(p *Pipeline) []string {
	var out []string
	for _, st := range p.Stages {
		out = append(out, st.Skill.Name)
	}
	return out
}

func attemptFields(a Attempt) map[string]any {
	b, _ := json.Marshal(a)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

type trajSummary struct {
	ExitStatus string  `json:"exit_status"`
	Submission string  `json:"submission"`
	Steps      int     `json:"steps"`
	Cost       float64 `json:"cost"`
}

func runAttempt(ctx context.Context, o Options, st Stage, idx, n int, input string) Attempt {
	t0 := time.Now()
	at := Attempt{Stage: idx, Skill: st.Skill.Name, Attempt: n, Input: input}
	defer func() { at.DurationMS = time.Since(t0).Milliseconds() }()
	base := fmt.Sprintf("%02d-%s-%d", idx, st.Skill.Name, n)
	at.Trajectory = filepath.Join(o.Dir, base+".json")
	at.Log = filepath.Join(o.Dir, base+".log")
	args := append([]string{}, o.ExtraArgs...)
	if st.Model != "" {
		args = append(args, "-m", st.Model)
	}
	if st.Profile != "" {
		args = append(args, "--profile", st.Profile)
	}
	if st.StepLimit > 0 {
		args = append(args, "--step-limit", strconv.Itoa(st.StepLimit))
	}
	if st.CostLimit > 0 {
		args = append(args, "--cost-limit", strconv.FormatFloat(st.CostLimit, 'g', -1, 64))
	}
	if st.TokenLimit > 0 {
		args = append(args, "--token-limit", strconv.Itoa(st.TokenLimit))
	}
	if st.TimeLimit > 0 {
		args = append(args, "--time-limit", strconv.Itoa(st.TimeLimit))
	}
	args = append(args, "--cwd", o.Cwd, "-o", at.Trajectory, "-t", input)
	if st.Behavior != "" {
		// A behavior file left by an earlier attempt or run must not decide this one.
		if err := os.Remove(behaviorPath(o.Cwd, st.Behavior)); err != nil && !errors.Is(err, os.ErrNotExist) {
			at.Check = Check{Method: "behavior", Reason: err.Error()}
			return at
		}
	}
	lf, err := os.OpenFile(at.Log, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		at.Check = Check{Method: "run", Reason: err.Error()}
		return at
	}
	runErr := o.RunAgent(ctx, args, lf)
	lf.Close()
	b, err := os.ReadFile(at.Trajectory)
	var ts trajSummary
	if err == nil {
		err = json.Unmarshal(b, &ts)
	}
	if err != nil {
		reason := "stage run wrote no trajectory"
		if runErr != nil {
			reason += ": " + runErr.Error()
		}
		at.Check = Check{Method: "run", Reason: reason}
		return at
	}
	at.ExitStatus, at.Output, at.Steps, at.CostUSD = ts.ExitStatus, ts.Submission, ts.Steps, ts.Cost
	if ts.ExitStatus != "Submitted" && ts.ExitStatus != "Completed" {
		at.Check = Check{Method: "run", Reason: "stage run ended " + ts.ExitStatus + ": " + oneLine(ts.Submission)}
		return at
	}
	switch {
	case st.Behavior != "":
		at.Check = runBehavior(ctx, o.Cwd, st.Behavior, st.CheckTimeout)
		if lf, err := os.OpenFile(at.Log, os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
			writeEvidence(lf, at.Check)
			lf.Close()
		}
	case len(st.Check) > 0:
		at.Check = runCheck(ctx, o.Cwd, st.Check, st.CheckTimeout)
	default:
		at.Check = modelVerdict(ts.Submission)
	}
	return at
}

// Prompt builds stage idx's task: the skill's instructions, its inputs and,
// on a retry, why the previous attempt failed.
func Prompt(idx, n int, st Stage, task, prev, prevName, cwd, feedback string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are running stage %d of %d (skill %q) of an automatic tinyagent workflow. Do only this skill's job; later stages handle the rest. No one will answer questions between stages, so don't ask any.\n\n", idx, n, st.Skill.Name)
	b.WriteString("## Skill\n\n")
	b.WriteString(st.Skill.Body)
	b.WriteString("\n\n## Inputs\n\n")
	known := map[string]string{"task": task, "spec": task, "repo_path": cwd, "cwd": cwd, "previous_output": prev}
	if prevName != "" {
		known[prevName] = prev
	}
	for k, v := range st.Inputs {
		known[k] = v
	}
	declared := append([]string{}, st.Skill.Inputs...)
	for _, k := range []string{"task", "previous_output"} {
		if !contains(declared, k) && (k != "previous_output" || prev != "") {
			declared = append(declared, k)
		}
	}
	for _, in := range declared {
		v, ok := known[in]
		if !ok || v == "" {
			v = "(not provided; infer it from the task and the previous stage's output)"
		}
		fmt.Fprintf(&b, "### %s\n\n%s\n\n", in, v)
	}
	b.WriteString("## Finishing\n\n")
	if st.Behavior != "" {
		fmt.Fprintf(&b, "Write the behavior cases to `%s` (relative to %s), as YAML:\n\n%s\nThe harness then runs each case's `run` argv in %s without a shell and the stage passes only if every case's observed exit code and output meet its `expect`. Expected strings must come from the task's acceptance criteria, not from what the program happens to print. If the task has no behavioral acceptance criteria, write no file (or `cases: []`) and the stage is skipped.", st.Behavior, cwd, behaviorExample, cwd)
		if st.Skill.SuccessCheck != "" {
			fmt.Fprintf(&b, " Success check: %s", st.Skill.SuccessCheck)
		}
		b.WriteString("\nEnd with your stage output: what the next stage needs to know.\n")
	} else if len(st.Check) > 0 {
		fmt.Fprintf(&b, "When you finish, the harness runs `%s` in %s; the stage passes only if it exits 0.", strings.Join(st.Check, " "), cwd)
		if st.Skill.SuccessCheck != "" {
			fmt.Fprintf(&b, " Success check: %s", st.Skill.SuccessCheck)
		}
		b.WriteString("\nEnd with your stage output: what the next stage needs to know.\n")
	} else {
		fmt.Fprintf(&b, "Success check: %s\nEnd with your stage output (what the next stage needs to know), then a final line that is exactly `SUCCESS CHECK: PASS` if the check holds, or `SUCCESS CHECK: FAIL - <reason>` if it doesn't.\n", st.Skill.SuccessCheck)
	}
	if feedback != "" {
		fmt.Fprintf(&b, "\n## Previous attempt failed\n\n%s\n", truncate(feedback, MaxFeedback))
	}
	return b.String()
}

const behaviorExample = "```yaml\nversion: 1\ncases:\n  - name: prints-greeting\n    criterion: the CLI prints hello for --name world\n    run: [go, run, ./cmd/app, --name, world]\n    timeout: 60            # seconds, optional\n    expect:\n      exit_code: 0         # default 0\n      stdout_contains: [\"hello world\"]\n      stdout_not_contains: [panic]\n      stdout_matches: [\"^hello\"]\n      stderr_contains: []\n```\n"

var reVerdict = regexp.MustCompile(`(?im)^[ \t>*` + "`" + `]*SUCCESS CHECK:[ \t]*(PASS|FAIL)\b[ \t:*` + "`" + `-]*(.*?)[ \t\r]*$`)

// modelVerdict reads the last SUCCESS CHECK line of a stage's output.
func modelVerdict(out string) Check {
	c := Check{Method: "model"}
	m := reVerdict.FindAllStringSubmatch(out, -1)
	if len(m) == 0 {
		c.Reason = "the stage output has no SUCCESS CHECK: PASS or FAIL line"
		return c
	}
	last := m[len(m)-1]
	if strings.EqualFold(last[1], "PASS") {
		c.Passed = true
		return c
	}
	c.Reason = strings.Trim(last[2], "`* ")
	if c.Reason == "" {
		c.Reason = "the model reported the success check failed"
	}
	return c
}

func runCheck(ctx context.Context, dir string, argv []string, timeout int) Check {
	c := Check{Method: "command", Argv: argv}
	cctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	var out bytes.Buffer
	cmd := exec.CommandContext(cctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = &out, &out
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	c.Output = tail(out.String(), MaxCheckOutput)
	var ee *exec.ExitError
	switch {
	case err == nil:
		c.Passed = true
		return c
	case errors.Is(cctx.Err(), context.DeadlineExceeded):
		c.ExitCode, c.TimedOut = -1, true
		c.Reason = fmt.Sprintf("`%s` timed out after %ds", strings.Join(argv, " "), timeout)
		return c
	case errors.As(err, &ee) && ee.ExitCode() > 0:
		c.ExitCode = ee.ExitCode()
	default:
		c.ExitCode = -1
		if c.Output == "" {
			c.Output = err.Error()
		}
	}
	c.Reason = fmt.Sprintf("`%s` exited %d:\n%s", strings.Join(argv, " "), c.ExitCode, tail(c.Output, MaxFeedback-200))
	return c
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return truncate(s, 200)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
