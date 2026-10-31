package main

import (
	"strings"
	"testing"
)

func TestColorEnabled(t *testing.T) {
	cases := []struct {
		name, mode, noColor string
		tty, want           bool
	}{
		{"auto tty", "auto", "", true, true},
		{"auto pipe", "auto", "", false, false},
		{"auto tty NO_COLOR", "auto", "1", true, false},
		{"always pipe", "always", "", false, true},
		{"always overrides NO_COLOR", "always", "1", false, true},
		{"never tty", "never", "", true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("NO_COLOR", c.noColor)
			if got := colorEnabled(c.mode, c.tty); got != c.want {
				t.Errorf("colorEnabled(%q, %v) with NO_COLOR=%q = %v, want %v", c.mode, c.tty, c.noColor, got, c.want)
			}
		})
	}
}

func TestStylerOff(t *testing.T) {
	s := styler{}
	if got := s.Red("error:") + s.Bold("x") + s.Glyph("✓", "ok"); got != "error:xok" {
		t.Errorf("off styler = %q", got)
	}
	on := styler{on: true}
	if got := on.Green("ok"); got != "\x1b[32mok\x1b[0m" {
		t.Errorf("on styler = %q", got)
	}
}

// colorCheck runs --check with the given --color value and a faked stdout
// terminal state.
func colorCheck(t *testing.T, tty bool, args ...string) (int, string) {
	t.Helper()
	defer func(f func() bool) { stdoutIsTerminal = f }(stdoutIsTerminal)
	stdoutIsTerminal = func() bool { return tty }
	defer setColor("never")
	cfg := writeConfig(t, "model: ollama/llama3\n")
	code, stdout, stderr := runCLI(t, append([]string{"--check", "--cwd", t.TempDir(), "-c", cfg}, args...)...)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	return code, stdout
}

func TestColorFlagCheckOutput(t *testing.T) {
	t.Setenv("AGENT_MODEL", "")
	t.Setenv("NO_COLOR", "")
	styled := func(s string) bool { return strings.Contains(s, "\x1b[") }
	if _, out := colorCheck(t, true); !styled(out) {
		t.Errorf("auto with a terminal: want color:\n%q", out)
	}
	if _, out := colorCheck(t, false); styled(out) {
		t.Errorf("auto with a pipe: want plain:\n%q", out)
	}
	if _, out := colorCheck(t, false, "--color=always"); !styled(out) {
		t.Errorf("--color=always with a pipe: want color:\n%q", out)
	}
	if _, out := colorCheck(t, true, "--color=never"); styled(out) {
		t.Errorf("--color=never: want plain:\n%q", out)
	}
	t.Setenv("NO_COLOR", "1")
	if _, out := colorCheck(t, true); styled(out) {
		t.Errorf("NO_COLOR=1: want plain:\n%q", out)
	}
	if _, out := colorCheck(t, false, "--color", "always"); !styled(out) {
		t.Errorf("--color=always with NO_COLOR: want color:\n%q", out)
	}
}

func TestColorFlagInvalid(t *testing.T) {
	code, _, stderr := runWith(t, "--color=sometimes", "--check")
	if code != 2 || !strings.Contains(stderr, `--color "sometimes" must be auto, always or never`) {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
}
