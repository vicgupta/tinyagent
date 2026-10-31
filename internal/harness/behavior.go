package harness

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Behavior-check bounds.
const (
	// MaxBehaviorCases bounds the cases in one behavior file.
	MaxBehaviorCases = 20
	// MaxBehaviorFileBytes caps a behavior file.
	MaxBehaviorFileBytes = 64 << 10
	// DefaultBehaviorTimeout bounds one case's command, in seconds; a case
	// may ask for more, up to the stage's check_timeout.
	DefaultBehaviorTimeout = 60
	// MaxBehaviorOutput caps each stream of observed output kept as evidence.
	MaxBehaviorOutput = 4 << 10
)

// BehaviorFile is what a verify-behavior stage writes: the task's
// behavioral acceptance criteria as commands to run and the output each must
// produce. No cases means the task has no behavioral criteria.
type BehaviorFile struct {
	Version int            `yaml:"version"`
	Cases   []BehaviorCase `yaml:"cases"`
}

// BehaviorCase runs one command and checks what it actually printed.
type BehaviorCase struct {
	Name      string         `yaml:"name"`
	Criterion string         `yaml:"criterion"`
	Run       []string       `yaml:"run"`
	Stdin     string         `yaml:"stdin"`
	Timeout   int            `yaml:"timeout"`
	Expect    BehaviorExpect `yaml:"expect"`
}

// BehaviorExpect is the observable outcome a case requires. Exit code
// defaults to 0.
type BehaviorExpect struct {
	ExitCode          *int     `yaml:"exit_code"`
	StdoutContains    []string `yaml:"stdout_contains"`
	StdoutNotContains []string `yaml:"stdout_not_contains"`
	StdoutMatches     []string `yaml:"stdout_matches"`
	StderrContains    []string `yaml:"stderr_contains"`
}

// BehaviorResult is one case's evidence: what ran, what it printed and
// which expectations failed.
type BehaviorResult struct {
	Name      string   `json:"name"`
	Criterion string   `json:"criterion,omitempty"`
	Argv      []string `json:"argv"`
	Passed    bool     `json:"passed"`
	ExitCode  int      `json:"exit_code"`
	TimedOut  bool     `json:"timed_out,omitempty"`
	Stdout    string   `json:"stdout"`
	Stderr    string   `json:"stderr"`
	Failures  []string `json:"failures,omitempty"`
}

// checkBehaviorPath validates a stage's behavior path from the workflow file.
func checkBehaviorPath(p string) error {
	if p == "" {
		return errors.New("behavior must be a path")
	}
	if filepath.IsAbs(p) {
		return errors.New("behavior must be a path relative to --cwd")
	}
	c := filepath.Clean(p)
	if c == "." || c == ".." || strings.HasPrefix(c, ".."+string(filepath.Separator)) {
		return errors.New("behavior must stay inside --cwd")
	}
	return nil
}

func behaviorPath(cwd, rel string) string { return filepath.Join(cwd, filepath.Clean(rel)) }

// parseBehavior reads and validates a behavior file.
func parseBehavior(data []byte) (*BehaviorFile, error) {
	var f BehaviorFile
	if len(bytes.TrimSpace(data)) == 0 {
		return &f, nil
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&f); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if f.Version != 0 && f.Version != 1 {
		return nil, errors.New("version must be 1")
	}
	if len(f.Cases) > MaxBehaviorCases {
		return nil, fmt.Errorf("at most %d cases", MaxBehaviorCases)
	}
	for i, c := range f.Cases {
		where := fmt.Sprintf("case %d", i+1)
		if c.Name != "" {
			where += " (" + c.Name + ")"
		}
		switch {
		case len(c.Run) == 0 || c.Run[0] == "":
			return nil, fmt.Errorf("%s: run must be a non-empty argv list", where)
		case c.Timeout < 0:
			return nil, fmt.Errorf("%s: timeout must be >= 0", where)
		}
		e := c.Expect
		if e.ExitCode == nil && len(e.StdoutContains)+len(e.StdoutNotContains)+len(e.StdoutMatches)+len(e.StderrContains) == 0 {
			return nil, fmt.Errorf("%s: expect needs at least one of exit_code, stdout_contains, stdout_not_contains, stdout_matches, stderr_contains", where)
		}
		for _, re := range e.StdoutMatches {
			if _, err := regexp.Compile(re); err != nil {
				return nil, fmt.Errorf("%s: stdout_matches %q: %v", where, re, err)
			}
		}
	}
	return &f, nil
}

// runBehavior runs the behavior file a stage wrote and decides the stage on
// observed output alone. A missing or empty file skips the stage cleanly.
func runBehavior(ctx context.Context, cwd, rel string, maxTimeout int) Check {
	c := Check{Method: "behavior"}
	p := behaviorPath(cwd, rel)
	data, err := readCappedN(p, MaxBehaviorFileBytes)
	if errors.Is(err, os.ErrNotExist) {
		c.Passed, c.Skipped, c.Reason = true, true, "no behavioral acceptance criteria (no "+rel+" written)"
		return c
	}
	if err != nil {
		c.Reason = fmt.Sprintf("behavior file %s: %v", rel, err)
		return c
	}
	f, err := parseBehavior(data)
	if err != nil {
		c.Reason = fmt.Sprintf("behavior file %s: %v", rel, err)
		return c
	}
	if len(f.Cases) == 0 {
		c.Passed, c.Skipped, c.Reason = true, true, "no behavioral acceptance criteria ("+rel+" has no cases)"
		return c
	}
	var failed []string
	for i, bc := range f.Cases {
		if ctx.Err() != nil {
			c.Reason = "interrupted"
			return c
		}
		r := runBehaviorCase(ctx, cwd, bc, maxTimeout)
		if r.Name == "" {
			r.Name = fmt.Sprintf("case %d", i+1)
		}
		c.Evidence = append(c.Evidence, r)
		if !r.Passed {
			failed = append(failed, r.Name+": "+strings.Join(r.Failures, "; "))
		}
	}
	if len(failed) == 0 {
		c.Passed = true
		return c
	}
	c.Reason = fmt.Sprintf("%d of %d behavior cases failed:\n- %s", len(failed), len(f.Cases), strings.Join(failed, "\n- "))
	return c
}

func runBehaviorCase(ctx context.Context, cwd string, bc BehaviorCase, maxTimeout int) BehaviorResult {
	r := BehaviorResult{Name: bc.Name, Criterion: bc.Criterion, Argv: bc.Run}
	timeout := bc.Timeout
	if timeout == 0 {
		timeout = DefaultBehaviorTimeout
	}
	if maxTimeout > 0 && timeout > maxTimeout {
		timeout = maxTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(cctx, bc.Run[0], bc.Run[1:]...)
	cmd.Dir = cwd
	cmd.Stdin = strings.NewReader(bc.Stdin)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.Is(cctx.Err(), context.DeadlineExceeded):
		r.ExitCode, r.TimedOut = -1, true
		r.Failures = append(r.Failures, fmt.Sprintf("timed out after %ds", timeout))
	case errors.As(err, &ee) && ee.ExitCode() >= 0:
		r.ExitCode = ee.ExitCode()
	case errors.As(err, &ee):
		r.ExitCode = -1
		r.Failures = append(r.Failures, "killed: "+err.Error())
	default:
		r.ExitCode = -1
		r.Failures = append(r.Failures, "could not run: "+err.Error())
	}
	out, errOut := stdout.String(), stderr.String()
	r.Stdout, r.Stderr = tail(out, MaxBehaviorOutput), tail(errOut, MaxBehaviorOutput)
	e := bc.Expect
	want := 0
	if e.ExitCode != nil {
		want = *e.ExitCode
	}
	if !r.TimedOut && r.ExitCode != -1 && r.ExitCode != want {
		r.Failures = append(r.Failures, fmt.Sprintf("exit code %d, want %d", r.ExitCode, want))
	}
	for _, s := range e.StdoutContains {
		if !strings.Contains(out, s) {
			r.Failures = append(r.Failures, fmt.Sprintf("stdout lacks %q", s))
		}
	}
	for _, s := range e.StdoutNotContains {
		if strings.Contains(out, s) {
			r.Failures = append(r.Failures, fmt.Sprintf("stdout contains %q", s))
		}
	}
	for _, s := range e.StdoutMatches {
		if !regexp.MustCompile(s).MatchString(out) {
			r.Failures = append(r.Failures, fmt.Sprintf("stdout doesn't match /%s/", s))
		}
	}
	for _, s := range e.StderrContains {
		if !strings.Contains(errOut, s) {
			r.Failures = append(r.Failures, fmt.Sprintf("stderr lacks %q", s))
		}
	}
	r.Passed = len(r.Failures) == 0
	return r
}

// writeEvidence appends a behavior check's observed output to an attempt's log.
func writeEvidence(w io.Writer, c Check) {
	fmt.Fprintln(w, "\n== verify-behavior evidence ==")
	if c.Skipped {
		fmt.Fprintf(w, "skipped: %s\n", c.Reason)
		return
	}
	if len(c.Evidence) == 0 {
		fmt.Fprintf(w, "failed: %s\n", c.Reason)
		return
	}
	for _, r := range c.Evidence {
		verdict := "PASS"
		if !r.Passed {
			verdict = "FAIL: " + strings.Join(r.Failures, "; ")
		}
		fmt.Fprintf(w, "-- %s [%s]\n", r.Name, verdict)
		if r.Criterion != "" {
			fmt.Fprintf(w, "criterion: %s\n", r.Criterion)
		}
		fmt.Fprintf(w, "argv: %q\nexit code: %d\nstdout:\n%s\nstderr:\n%s\n", r.Argv, r.ExitCode, r.Stdout, r.Stderr)
	}
}
