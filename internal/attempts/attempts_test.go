package attempts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// fakeAgent writes a trajectory for each run: statuses[i] is attempt i+1's
// exit status, and each attempt's root is a fresh directory holding a file
// "n" with the attempt number.
type fakeAgent struct {
	t        *testing.T
	statuses []string
	cost     float64
	calls    [][]string
}

func (f *fakeAgent) run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	f.calls = append(f.calls, args)
	n := len(f.calls)
	var traj, wt string
	for i, a := range args {
		if a == "-o" {
			traj = args[i+1]
		}
		if strings.HasPrefix(a, "--worktree=") {
			wt = strings.TrimPrefix(a, "--worktree=")
		}
	}
	root := filepath.Join(f.t.TempDir(), wt)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	os.WriteFile(filepath.Join(root, "n"), []byte(fmt.Sprint(n)), 0o644)
	fmt.Fprintf(stdout, "submission %d\n", n)
	b, _ := json.Marshal(map[string]any{
		"exit_status": f.statuses[n-1], "steps": 3, "cost": f.cost,
		"input_tokens": 10, "output_tokens": 5, "root": root,
		"worktree": map[string]string{"path": root, "branch": "tinyagent/" + wt},
	})
	return os.WriteFile(traj, b, 0o644)
}

func skipNoBash(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("checks run through bash")
	}
}

func TestStopsAtFirstPass(t *testing.T) {
	skipNoBash(t)
	f := &fakeAgent{t: t, statuses: []string{"Submitted", "Submitted", "Submitted"}, cost: 0.01}
	out := filepath.Join(t.TempDir(), "run.json")
	var stderr, stdout bytes.Buffer
	rep, code := Run(context.Background(), Options{
		Models: []string{"m1", "m2", "m3"}, BaseName: "base",
		Checks: []Check{{Command: "test $(cat n) -ge 2", TimeoutSeconds: 10}},
		Output: out, ExtraArgs: []string{"--", "task"},
		Stderr: &stderr, Stdout: &stdout, RunAgent: f.run,
	})
	if code != 0 || rep.Winner == nil || *rep.Winner != 2 || rep.StopReason != StopPassed {
		t.Fatalf("code=%d rep=%+v stderr=%s", code, rep, stderr.String())
	}
	if len(f.calls) != 2 || len(rep.Attempts) != 2 {
		t.Fatalf("ran %d attempts, want 2 (stop on pass)", len(f.calls))
	}
	if rep.Attempts[0].Passed || rep.Attempts[0].Checks[0].ExitCode != 1 {
		t.Fatalf("attempt 1 = %+v", rep.Attempts[0])
	}
	if got := stdout.String(); got != "submission 2\n" {
		t.Fatalf("stdout = %q, want only the winner's submission", got)
	}
	if rep.WinnerBranch != "tinyagent/base-a2" {
		t.Fatalf("winner branch %q", rep.WinnerBranch)
	}
	args := strings.Join(f.calls[1], " ")
	for _, want := range []string{"-- task", "-m m2", "--worktree=base-a2", "-o " + TrajectoryPath(out, 2)} {
		if !strings.Contains(args, want) {
			t.Fatalf("args %q missing %q", args, want)
		}
	}
	b, err := os.ReadFile(ReportPath(out))
	if err != nil {
		t.Fatal(err)
	}
	var got Report
	if err := json.Unmarshal(b, &got); err != nil || got.SchemaVersion != 1 || got.Requested != 3 || *got.Winner != 2 {
		t.Fatalf("report %s err=%v", b, err)
	}
	if !strings.Contains(stderr.String(), "winner: attempt 2") || !strings.Contains(stderr.String(), "diff HEAD") {
		t.Fatalf("stderr: %s", stderr.String())
	}
}

func TestNoPassAndSkippedChecks(t *testing.T) {
	skipNoBash(t)
	f := &fakeAgent{t: t, statuses: []string{"StepLimitExceeded", "Submitted"}}
	var stderr bytes.Buffer
	rep, code := Run(context.Background(), Options{
		Models: []string{"a", "b"}, BaseName: "x",
		Checks: []Check{{Command: "echo nope; exit 3"}},
		Stderr: &stderr, RunAgent: f.run,
	})
	if code != 1 || rep.Winner != nil || rep.StopReason != StopExhausted {
		t.Fatalf("code=%d rep=%+v", code, rep)
	}
	if c := rep.Attempts[0].Checks[0]; !c.Skipped {
		t.Fatalf("checks should be skipped for a StepLimitExceeded run: %+v", c)
	}
	if c := rep.Attempts[1].Checks[0]; c.ExitCode != 3 || c.Output != "nope\n" {
		t.Fatalf("check = %+v", c)
	}
	if rep.Attempts[0].Trajectory != "" {
		t.Fatal("temporary trajectory path should not be reported")
	}
	if !strings.Contains(stderr.String(), "no attempt passed") {
		t.Fatalf("stderr: %s", stderr.String())
	}
}

func TestBudgetPassedOnAndExhausted(t *testing.T) {
	skipNoBash(t)
	f := &fakeAgent{t: t, statuses: []string{"Submitted", "Submitted", "Submitted"}, cost: 0.6}
	rep, code := Run(context.Background(), Options{
		Models: []string{"a", "b", "c"}, BaseName: "x", CostLimit: 1,
		Checks: []Check{{Command: "false"}},
		Stderr: io.Discard, RunAgent: f.run,
	})
	if code != 1 || rep.StopReason != StopBudget || len(f.calls) != 2 {
		t.Fatalf("code=%d calls=%d rep=%+v", code, len(f.calls), rep)
	}
	if got := strings.Join(f.calls[1], " "); !strings.Contains(got, "--cost-limit 0.4") {
		t.Fatalf("second attempt should get the remaining budget: %s", got)
	}
}

func TestTimeLimitForwarded(t *testing.T) {
	skipNoBash(t)
	f := &fakeAgent{t: t, statuses: []string{"Submitted"}}
	clock := time.Unix(0, 0)
	_, code := Run(context.Background(), Options{
		Models: []string{"a"}, BaseName: "x", TimeLimit: 90 * time.Second,
		Checks: []Check{{Command: "true"}}, Stderr: io.Discard, RunAgent: f.run,
		Now: func() time.Time { return clock },
	})
	if code != 0 || !strings.Contains(strings.Join(f.calls[0], " "), "--time-limit 90") {
		t.Fatalf("code=%d args=%v", code, f.calls)
	}
}

func TestCheckTimeoutAndRedaction(t *testing.T) {
	skipNoBash(t)
	dir := t.TempDir()
	cr := runCheck(context.Background(), nil, dir, Check{Command: "sleep 30", TimeoutSeconds: 1}, nil)
	if !cr.TimedOut || cr.ExitCode != -1 || cr.DurationMS > 15000 {
		t.Fatalf("check = %+v", cr)
	}
	cr = runCheck(context.Background(), nil, dir, Check{Command: "echo secret-123"}, func(s string) string {
		return strings.ReplaceAll(s, "secret-123", "[REDACTED]")
	})
	if cr.Output != "[REDACTED]\n" {
		t.Fatalf("output %q", cr.Output)
	}
	cr = runCheck(context.Background(), nil, dir, Check{Command: "head -c 20000 /dev/zero | tr '\\0' x"}, nil)
	if len(cr.Output) > MaxCheckOutput+len("…") || !strings.HasPrefix(cr.Output, "…") {
		t.Fatalf("output not capped: %d bytes", len(cr.Output))
	}
}

func TestInterrupted(t *testing.T) {
	skipNoBash(t)
	ctx, cancel := context.WithCancel(context.Background())
	f := &fakeAgent{t: t, statuses: []string{"Interrupted", "Submitted"}}
	run := func(c context.Context, a []string, o, e io.Writer) error { cancel(); return f.run(c, a, o, e) }
	rep, code := Run(ctx, Options{Models: []string{"a", "b"}, BaseName: "x",
		Checks: []Check{{Command: "true"}}, Stderr: io.Discard, RunAgent: run})
	if code != 1 || rep.StopReason != StopInterrupt || len(f.calls) != 1 {
		t.Fatalf("code=%d rep=%+v", code, rep)
	}
}

func TestPaths(t *testing.T) {
	if got := TrajectoryPath("out/run.json", 2); got != "out/run.a2.json" {
		t.Fatal(got)
	}
	if got := ReportPath("run"); got != "run.attempts.json" {
		t.Fatal(got)
	}
}
