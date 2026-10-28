package attempts

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// envRunner runs checks on the host with a fixed environment, recording
// each root it was asked for.
type envRunner struct {
	env   []string
	roots []string
}

func (r *envRunner) Command(ctx context.Context, root, command string) *exec.Cmd {
	r.roots = append(r.roots, root)
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Dir = root
	cmd.Env = r.env
	return cmd
}

func TestChecksUseCheckEnv(t *testing.T) {
	skipNoBash(t)
	f := &fakeAgent{t: t, statuses: []string{"Submitted"}}
	r := &envRunner{env: []string{"PATH=/usr/bin:/bin", "ATTEMPT_MARK=yes"}}
	prepared, released := 0, 0
	var stderr bytes.Buffer
	rep, code := Run(context.Background(), Options{
		Models: []string{"m1"}, BaseName: "base",
		Checks: []Check{{Command: `test "$ATTEMPT_MARK" = yes`}, {Command: `test -z "$HOME"`}},
		Output: filepath.Join(t.TempDir(), "o.json"), Stderr: &stderr, Stdout: &bytes.Buffer{}, RunAgent: f.run,
		CheckEnv: func(ctx context.Context, root string) (CheckRunner, func(), error) {
			prepared++
			return r, func() { released++ }, nil
		},
	})
	if code != 0 || rep.Winner == nil {
		t.Fatalf("code=%d rep=%+v stderr=%s", code, rep, stderr.String())
	}
	if prepared != 1 || released != 1 {
		t.Fatalf("prepared=%d released=%d, want 1 each", prepared, released)
	}
	if len(r.roots) != 2 || r.roots[0] != rep.Attempts[0].root {
		t.Fatalf("roots = %v, want the attempt root twice", r.roots)
	}
}

func TestCheckEnvErrorFailsClosed(t *testing.T) {
	skipNoBash(t)
	f := &fakeAgent{t: t, statuses: []string{"Submitted", "Submitted"}}
	calls := 0
	var stderr bytes.Buffer
	rep, code := Run(context.Background(), Options{
		Models: []string{"m1", "m2"}, BaseName: "base",
		Checks: []Check{{Command: "true"}, {Command: "true"}},
		Output: filepath.Join(t.TempDir(), "o.json"), Stderr: &stderr, Stdout: &bytes.Buffer{}, RunAgent: f.run,
		Redact: func(s string) string { return strings.ReplaceAll(s, "tok-1", "[REDACTED]") },
		CheckEnv: func(ctx context.Context, root string) (CheckRunner, func(), error) {
			calls++
			return nil, nil, errors.New("docker not found (tok-1)")
		},
	})
	if code == 0 || rep.Winner != nil {
		t.Fatalf("code=%d winner=%v: a check environment failure must not pass", code, rep.Winner)
	}
	if calls != 2 {
		t.Fatalf("CheckEnv called %d times, want once per attempt", calls)
	}
	for _, a := range rep.Attempts {
		if a.Passed || !strings.Contains(a.Error, "check environment: docker not found ([REDACTED])") {
			t.Fatalf("attempt = %+v", a)
		}
		if len(a.Checks) != 2 || a.Checks[0].ExitCode != -1 || a.Checks[0].Skipped {
			t.Fatalf("checks = %+v", a.Checks)
		}
	}
}
