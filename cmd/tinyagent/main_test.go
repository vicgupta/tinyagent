package main

import (
	"errors"
	"flag"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/vicgupta/tinyagent/internal/agent"
	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

func TestReadTask(t *testing.T) {
	cases := []struct {
		task, stdin, want string
	}{
		{"fix the bug", "ignored", "fix the bug"},
		{"-", "line one\nline two\n", "line one\nline two"},
		{"-", "  padded  \n\n", "padded"},
	}
	for _, c := range cases {
		got, err := readTask(c.task, strings.NewReader(c.stdin))
		if err != nil || got != c.want {
			t.Errorf("readTask(%q, %q) = %q, %v; want %q", c.task, c.stdin, got, err, c.want)
		}
	}
}

func TestReadTaskEmptyStdin(t *testing.T) {
	if _, err := readTask("-", strings.NewReader(" \n")); err == nil {
		t.Error("want an error for an empty task on stdin")
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

func TestReadTaskStdinError(t *testing.T) {
	if _, err := readTask("-", errReader{}); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("want the read error, got %v", err)
	}
}

func TestApplyFlagOverridesTimeLimit(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		cfgWall int
		want    int
	}{
		{"no flag keeps default", nil, 0, 0},
		{"no flag keeps config", nil, 60, 60},
		{"flag wins over config", []string{"--time-limit", "5"}, 60, 5},
		{"explicit zero clears config", []string{"--time-limit", "0"}, 60, 0},
	}
	for _, c := range cases {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		var step, timeLimit int
		var cost float64
		fs.IntVar(&step, "step-limit", 0, "")
		fs.Float64Var(&cost, "cost-limit", 0, "")
		fs.IntVar(&timeLimit, "time-limit", 0, "")
		if err := fs.Parse(c.args); err != nil {
			t.Fatalf("%s: parse: %v", c.name, err)
		}
		cfg := config.AgentConfig{WallTimeSec: c.cfgWall, StepLimit: 7, CostLimit: 1.5}
		applyFlagOverrides(fs, &cfg, step, cost, timeLimit)
		if cfg.WallTimeSec != c.want {
			t.Errorf("%s: WallTimeSec = %d, want %d", c.name, cfg.WallTimeSec, c.want)
		}
		if cfg.StepLimit != 7 || cfg.CostLimit != 1.5 {
			t.Errorf("%s: unrelated limits changed: %+v", c.name, cfg)
		}
	}
}

func TestReportSummary(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "")
	var b strings.Builder
	reportSummary(&b, "openai/gpt-4o", &trajectory.Trajectory{ExitStatus: agent.StatusError, Submission: "HTTP 401: Incorrect API key"}, 0)
	lines := strings.Split(strings.TrimSpace(b.String()), "\n")
	if len(lines) != 2 || lines[0] != "Error after 0 steps in 0s: HTTP 401: Incorrect API key" || !strings.HasPrefix(lines[1], "hint: OPENAI_API_KEY isn't set") {
		t.Errorf("got %q, want the summary line then a hint", b.String())
	}

	b.Reset()
	reportSummary(&b, "openai/gpt-4o", &trajectory.Trajectory{ExitStatus: agent.StatusSubmitted, Steps: 2, InputTokens: 10, OutputTokens: 5, Cost: 0.5}, 3*time.Second)
	if got := b.String(); got != "Submitted after 2 steps in 3s (10 input / 5 output tokens, $0.50)\n" {
		t.Errorf("success: got %q", got)
	}
}

func TestSummaryLine(t *testing.T) {
	tests := []struct {
		name    string
		tr      trajectory.Trajectory
		elapsed time.Duration
		want    string
	}{
		{"submitted", trajectory.Trajectory{ExitStatus: agent.StatusSubmitted, Steps: 12, InputTokens: 48213, OutputTokens: 3104, Cost: 0.2299},
			102 * time.Second, "Submitted after 12 steps in 1m42s (48,213 input / 3,104 output tokens, $0.23)"},
		{"limit", trajectory.Trajectory{ExitStatus: agent.StatusStepLimit, Steps: 50, InputTokens: 120441, OutputTokens: 11209, Cost: 1.87},
			363 * time.Second, "StepLimitExceeded after 50 steps in 6m03s (120,441 input / 11,209 output tokens, $1.87)"},
		{"error", trajectory.Trajectory{ExitStatus: agent.StatusError, Steps: 3, InputTokens: 4120, OutputTokens: 280, Cost: 0.0043, Submission: "HTTP 401: Incorrect API key"},
			12 * time.Second, "Error after 3 steps in 12s (4,120 input / 280 output tokens, $0.0043): HTTP 401: Incorrect API key"},
		{"zero cost", trajectory.Trajectory{ExitStatus: agent.StatusCompleted, Steps: 4, InputTokens: 1000, OutputTokens: 200},
			3725 * time.Second, "Completed after 4 steps in 1h02m (1,000 input / 200 output tokens)"},
		{"zero steps", trajectory.Trajectory{ExitStatus: agent.StatusInterrupted},
			2 * time.Second, "Interrupted after 0 steps in 2s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := summaryLine(&tt.tr, tt.elapsed); got != tt.want {
				t.Errorf("got  %q\nwant %q", got, tt.want)
			}
		})
	}
}

// wantUsage is the layout from issue #12, with its update: -h, --help listed
// first and -V for --version.
const wantUsage = `Usage: tinyagent -t "<prompt>" [flags]    (or --task "<prompt>")
       echo "<prompt>" | tinyagent -t -

  -h, --help              show this help and exit
Task:
  -t, --task string       task/prompt (required); "-" reads it from stdin
  -m, --model string      provider/model (default "openai/gpt-4o")
  -c, --config path       YAML config (default: built-in)
      --cwd dir           working directory (default ".")
Limits:
      --step-limit n      max model calls, 0 = unlimited
      --cost-limit usd    max USD, 0 = unlimited
      --time-limit sec    max wall-clock seconds, 0 = unlimited
Output:
  -o, --output path       write trajectory JSON
  -v, --verbose           print tool outputs
      --no-stream         wait for full responses instead of streaming
      --no-agents-md      don't add <cwd>/AGENTS.md to the system prompt
Other:
  -V, --version           print the version and exit
      --update            update to the latest release and exit

Examples:
  tinyagent -t "fix the failing test" -m anthropic/claude-sonnet-4
  cat task.md | tinyagent -t - --step-limit 30 -o run.json
`

// runWith runs the CLI with args, returning the exit code and what went to
// helpOutput (stdout) and flagOutput (stderr).
func runWith(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errOut strings.Builder
	defer func(h, f io.Writer, a []string) { helpOutput, flagOutput, os.Args = h, f, a }(helpOutput, flagOutput, os.Args)
	helpOutput, flagOutput, os.Args = &out, &errOut, append([]string{"tinyagent"}, args...)
	code = run()
	return code, out.String(), errOut.String()
}

func TestUsage(t *testing.T) {
	t.Setenv("AGENT_MODEL", "")
	for _, arg := range []string{"--help", "-h", "-help"} {
		code, got, errOut := runWith(t, arg)
		if code != 0 {
			t.Errorf("%s exit = %d, want 0", arg, code)
		}
		if got != wantUsage {
			t.Errorf("%s usage on stdout:\n%s\nwant:\n%s", arg, got, wantUsage)
		}
		if errOut != "" {
			t.Errorf("%s wrote to stderr: %q", arg, errOut)
		}
		for _, line := range strings.Split(got, "\n") {
			if strings.HasPrefix(line, "  -task") || strings.Contains(line, "shorthand for") {
				t.Errorf("old-style line: %q", line)
			}
		}
	}
	// Every flag row starts its help text in the same column.
	_, got, _ := runWith(t, "--help")
	col := -1
	for _, line := range strings.Split(got, "\n") {
		if !strings.HasPrefix(line, "  -") && !strings.HasPrefix(line, "      --") {
			continue
		}
		i := strings.Index(line, "--")
		i += strings.Index(line[i:], "    ")
		for i < len(line) && line[i] == ' ' {
			i++
		}
		if col == -1 {
			col = i
		} else if i != col {
			t.Errorf("help starts at column %d, want %d: %q", i, col, line)
		}
	}
}

func TestUsageShowsModelDefaultFromEnv(t *testing.T) {
	t.Setenv("AGENT_MODEL", "ollama/llama3.1")
	_, got, _ := runWith(t, "--help")
	if !strings.Contains(got, `  -m, --model string      provider/model (default "ollama/llama3.1")`+"\n") {
		t.Errorf("model row doesn't show $AGENT_MODEL:\n%s", got)
	}
}

func TestBadFlagExits2(t *testing.T) {
	code, out, got := runWith(t, "--nope")
	if code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if out != "" || !strings.Contains(got, "flag provided but not defined: -nope") || !strings.Contains(got, "Usage: tinyagent") {
		t.Errorf("stdout = %q, stderr = %q; want the parse error and usage on stderr", out, got)
	}
}

func TestShortAndLongFlagsParseIdentically(t *testing.T) {
	parse := func(args ...string) options {
		t.Helper()
		var o options
		fs := newFlagSet(&o)
		fs.SetOutput(io.Discard)
		if err := fs.Parse(args); err != nil {
			t.Fatalf("parse %v: %v", args, err)
		}
		return o
	}
	for _, c := range [][3][]string{
		{{"-h"}, {"--help"}, {"-help"}},
		{{"-t", "x"}, {"--task", "x"}, {"-task", "x"}},
		{{"-m", "ollama/x"}, {"--model", "ollama/x"}, {"-model=ollama/x"}},
		{{"-c", "a.yaml"}, {"--config", "a.yaml"}, {"-config", "a.yaml"}},
		{{"-o", "r.json"}, {"--output", "r.json"}, {"-output", "r.json"}},
		{{"-v"}, {"--verbose"}, {"-verbose"}},
		{{"-V"}, {"--version"}, {"-version"}},
	} {
		a, b, d := parse(c[0]...), parse(c[1]...), parse(c[2]...)
		if a != b || a != d {
			t.Errorf("%v, %v and %v parse differently: %+v / %+v / %+v", c[0], c[1], c[2], a, b, d)
		}
		if a == parse() {
			t.Errorf("%v had no effect", c[0])
		}
	}
}
