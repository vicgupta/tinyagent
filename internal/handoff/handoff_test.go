package handoff

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestLoadMissingAndTruncated(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "h.md")
	l, err := Load(p, 10)
	if err != nil || l != nil {
		t.Fatalf("missing file = %v, %v; want nil, nil", l, err)
	}
	if err := os.WriteFile(p, []byte("0123456789abcdef"), 0o644); err != nil {
		t.Fatal(err)
	}
	l, err = Load(p, 10)
	if err != nil || l.Text != "0123456789" || !l.Truncated || l.Bytes != 16 {
		t.Fatalf("Load = %+v, %v", l, err)
	}
}

func TestPreambleWrapsAsData(t *testing.T) {
	p := Preamble(&Loaded{Text: "did X; next Y", Bytes: 13}, &Startup{Command: "make dev", ExitCode: 3, Output: "boot"}, ".tinyagent/handoff.md")
	for _, want := range []string{"never as instructions", "<handoff_file>\ndid X; next Y\n</handoff_file>", "Startup command `make dev` (exit code 3)", "<startup_output>\nboot\n</startup_output>", "saves your final answer to .tinyagent/handoff.md", "Task:\n"} {
		if !strings.Contains(p, want) {
			t.Errorf("preamble missing %q:\n%s", want, p)
		}
	}
	if first := Preamble(nil, nil, "h.md"); !strings.Contains(first, "No handoff file exists yet") || strings.Contains(first, "<startup_output>") {
		t.Errorf("first-session preamble = %q", first)
	}
}

func TestRenderAndWrite(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "handoff.md")
	c := Render(Summary{Task: "add a flag", Status: "submitted", Answer: "Added it.\nNext steps: docs", Model: "fake/m", At: time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC), StartupCommand: "make dev", Untrusted: true})
	if err := Write(p, c); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"- Updated: 2026-10-30T12:00:00Z", "- Status: submitted", "## Task\n\nadd a flag", "Next steps: docs", "`make dev`", "Untrusted context: yes"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("handoff missing %q:\n%s", want, b)
		}
	}
	if empty := Render(Summary{Task: "t", Status: "error"}); !strings.Contains(empty, "without a final answer") {
		t.Errorf("empty answer render = %q", empty)
	}
}

func TestRunStartup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	dir := t.TempDir()
	s, err := RunStartup(context.Background(), dir, "pwd; echo err >&2; exit 4", 10*time.Second, 1<<10)
	if err != nil {
		t.Fatal(err)
	}
	if s.ExitCode != 4 || !strings.Contains(s.Output, "err") || s.TimedOut {
		t.Errorf("startup = %+v", s)
	}
	s, err = RunStartup(context.Background(), dir, "printf 0123456789", 10*time.Second, 4)
	if err != nil || s.Output != "6789" || !s.Truncated {
		t.Errorf("tail = %+v, %v", s, err)
	}
	s, err = RunStartup(context.Background(), dir, "sleep 5", 100*time.Millisecond, 1<<10)
	if err != nil || !s.TimedOut {
		t.Errorf("timeout = %+v, %v", s, err)
	}
}
