package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vicgupta/tinyagent/internal/agent"
	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/eventlog"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/tools"
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
	reportSummary(&b, "openai/gpt-4o", &trajectory.Trajectory{ExitStatus: agent.StatusError, Submission: "HTTP 401: Incorrect API key"}, 0, "")
	lines := strings.Split(strings.TrimSpace(b.String()), "\n")
	if len(lines) != 2 || lines[0] != "Error after 0 steps in 0s: HTTP 401: Incorrect API key" || !strings.HasPrefix(lines[1], "hint: OPENAI_API_KEY isn't set") {
		t.Errorf("got %q, want the summary line then a hint", b.String())
	}

	b.Reset()
	reportSummary(&b, "openai/gpt-4o", &trajectory.Trajectory{ExitStatus: agent.StatusSubmitted, Steps: 2, InputTokens: 10, OutputTokens: 5, Cost: 0.5}, 3*time.Second, "")
	if got := b.String(); got != "Submitted after 2 steps in 3s (10 input / 5 output tokens, $0.50)\n" {
		t.Errorf("success: got %q", got)
	}
}

func TestReportSummaryResume(t *testing.T) {
	var b strings.Builder
	reportSummary(&b, "openai/gpt-4o", &trajectory.Trajectory{ExitStatus: agent.StatusStepLimit, Steps: 30}, 252*time.Second, "tinyagent --resume run.json --cwd /src --step-limit 60 -o run-2.json")
	if got, want := b.String(), "StepLimitExceeded after 30 steps in 4m12s\nresume: tinyagent --resume run.json --cwd /src --step-limit 60 -o run-2.json\n"; got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestResumeCommand(t *testing.T) {
	dir := t.TempDir()
	run := filepath.Join(dir, "run.json")
	cfg := config.AgentConfig{StepLimit: 30, CostLimit: 0.75, WallTimeSec: 600}
	tests := []struct {
		name   string
		tr     trajectory.Trajectory
		output string
		root   string
		want   string
	}{
		{"step limit", trajectory.Trajectory{ExitStatus: agent.StatusStepLimit}, run, "/home/me/src",
			"tinyagent --resume " + run + " --cwd /home/me/src --step-limit 60 -o " + filepath.Join(dir, "run-2.json")},
		{"cost limit", trajectory.Trajectory{ExitStatus: agent.StatusCostLimit}, run, "/src",
			"tinyagent --resume " + run + " --cwd /src --cost-limit 1.5 -o " + filepath.Join(dir, "run-2.json")},
		{"time limit", trajectory.Trajectory{ExitStatus: agent.StatusTimeLimit}, run, "/src",
			"tinyagent --resume " + run + " --cwd /src --time-limit 1200 -o " + filepath.Join(dir, "run-2.json")},
		{"interrupted", trajectory.Trajectory{ExitStatus: agent.StatusInterrupted}, run, "/src",
			"tinyagent --resume " + run + " --cwd /src -o " + filepath.Join(dir, "run-2.json")},
		{"output truncated", trajectory.Trajectory{ExitStatus: agent.StatusOutputTruncated}, run, "/src",
			"tinyagent --resume " + run + " --cwd /src -o " + filepath.Join(dir, "run-2.json")},
		{"second resume", trajectory.Trajectory{ExitStatus: agent.StatusInterrupted}, "run-2.json", "/src",
			"tinyagent --resume run-2.json --cwd /src -o run-3.json"},
		{"no extension", trajectory.Trajectory{ExitStatus: agent.StatusInterrupted}, "out/trace", "/src",
			"tinyagent --resume out/trace --cwd /src -o out/trace-2"},
		{"spaces", trajectory.Trajectory{ExitStatus: agent.StatusInterrupted}, "my runs/run.json", "/home/me/My Project",
			"tinyagent --resume 'my runs/run.json' --cwd '/home/me/My Project' -o 'my runs/run-2.json'"},
		{"quote", trajectory.Trajectory{ExitStatus: agent.StatusInterrupted}, "it's.json", "/src",
			`tinyagent --resume 'it'\''s.json' --cwd /src -o 'it'\''s-2.json'`},
		{"no output", trajectory.Trajectory{ExitStatus: agent.StatusStepLimit}, "", "/src", ""},
		{"output schema", trajectory.Trajectory{ExitStatus: agent.StatusStepLimit, OutputSchemaDigest: "sha256:x"}, run, "/src", ""},
		{"submitted", trajectory.Trajectory{ExitStatus: agent.StatusSubmitted}, run, "/src", ""},
		{"completed", trajectory.Trajectory{ExitStatus: agent.StatusCompleted}, run, "/src", ""},
		{"error", trajectory.Trajectory{ExitStatus: agent.StatusError}, run, "/src", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resumeCommand(&tt.tr, tt.output, tt.root, cfg); got != tt.want {
				t.Errorf("got  %q\nwant %q", got, tt.want)
			}
		})
	}
}

func TestNextOutputPathSkipsExisting(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"run-2.json", "run-3.json"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := nextOutputPath(filepath.Join(dir, "run.json")), filepath.Join(dir, "run-4.json"); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got, want := nextOutputPath(filepath.Join(dir, "run-2.json")), filepath.Join(dir, "run-4.json"); got != want {
		t.Errorf("got %q, want %q", got, want)
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
		{"cached", trajectory.Trajectory{ExitStatus: agent.StatusSubmitted, Steps: 12, InputTokens: 48213, OutputTokens: 3104, CacheReadTokens: 41900, CacheWriteTokens: 5000, Cost: 0.09},
			102 * time.Second, "Submitted after 12 steps in 1m42s (48,213 input / 3,104 output tokens, 41,900 cached, $0.09)"},
		{"cache writes only", trajectory.Trajectory{ExitStatus: agent.StatusSubmitted, Steps: 1, InputTokens: 5000, OutputTokens: 10, CacheWriteTokens: 4900, Cost: 0.02},
			time.Second, "Submitted after 1 steps in 1s (5,000 input / 10 output tokens, $0.02)"},
		{"zero cost", trajectory.Trajectory{ExitStatus: agent.StatusCompleted, Steps: 4, InputTokens: 1000, OutputTokens: 200},
			3725 * time.Second, "Completed after 4 steps in 1h02m (1,000 input / 200 output tokens)"},
		{"fallback", trajectory.Trajectory{ExitStatus: agent.StatusSubmitted, Steps: 2, ActiveModel: "anthropic/b", ModelSwitches: []trajectory.ModelSwitch{{Step: 1, From: "openai/a", To: "anthropic/b", Reason: "http_503"}}},
			time.Second, "Submitted after 2 steps in 1s, 1 model fallback (now anthropic/b)"},
		{"fallbacks", trajectory.Trajectory{ExitStatus: agent.StatusSubmitted, Steps: 2, ActiveModel: "ollama/c", ModelSwitches: []trajectory.ModelSwitch{{Step: 1, To: "anthropic/b"}, {Step: 2, To: "ollama/c"}}},
			time.Second, "Submitted after 2 steps in 1s, 2 model fallbacks (now ollama/c)"},
		{"zero steps", trajectory.Trajectory{ExitStatus: agent.StatusInterrupted},
			2 * time.Second, "Interrupted after 0 steps in 2s"},
		{"verification failed", trajectory.Trajectory{ExitStatus: agent.StatusVerificationFailed, Steps: 6, StopReason: "[stop hook /bin/check]\ntests failed"},
			2 * time.Second, "VerificationFailed after 6 steps in 2s: [stop hook /bin/check] tests failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := summaryLine(&tt.tr, tt.elapsed); got != tt.want {
				t.Errorf("got  %q\nwant %q", got, tt.want)
			}
		})
	}
}

func TestJSONEventsWriteOneJSONLine(t *testing.T) {
	var out strings.Builder
	events := eventlog.NewJSONL(&out)
	events.Emit("run.started", map[string]any{"schema_version": 1, "model": "fake"})
	events.Emit("result", map[string]any{"status": agent.StatusSubmitted, "submission": "done"})
	if events.Err() != nil {
		t.Fatal(events.Err())
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines: %q", len(lines), out.String())
	}
	for i, line := range lines {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("line %d is invalid JSON: %v", i, err)
		}
		if _, ok := event["type"].(string); !ok {
			t.Errorf("line %d has no type: %#v", i, event)
		}
	}
	var result map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &result); err != nil {
		t.Fatal(err)
	}
	if result["type"] != "result" || result["status"] != agent.StatusSubmitted || result["submission"] != "done" {
		t.Errorf("final event = %#v", result)
	}
}

// replies is a model that returns its messages in order.
type replies []model.Message

func (r *replies) Query(context.Context, []model.Message, []model.ToolSpec) (model.Message, model.Cost, error) {
	if len(*r) == 0 {
		return model.Message{}, model.Cost{}, errors.New("out of replies")
	}
	msg := (*r)[0]
	*r = (*r)[1:]
	return msg, model.Cost{InputTokens: 10, OutputTokens: 5}, nil
}

func newTestAgent(t *testing.T, m model.Model) *agent.Agent {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return &agent.Agent{Model: m, ModelName: "fake", Tools: &tools.Executor{Root: root}, Config: config.Default(), SkipAgentsMD: true}
}

// TestJSONRunEventStream drives a whole run through startEvents and finish:
// stdout must be only JSONL, opening with run.started and closing with result.
func TestJSONRunEventStream(t *testing.T) {
	m := &replies{
		{Role: "assistant", Content: "looking", ToolCalls: []model.ToolCall{{ID: "1", Type: "function", Function: model.FunctionCall{Name: "list_files", Arguments: `{}`}}}},
		{Role: "assistant", Content: "all good\n" + agent.SubmitMarker},
	}
	a := newTestAgent(t, m)
	var out strings.Builder
	events := startEvents(&out, true, a, false, func() { t.Error("stop called on a healthy stream") }, nil)
	tr := a.Run(context.Background(), "check")
	if err := events.finish(tr, time.Second); err != nil {
		t.Fatal(err)
	}
	var kinds []string
	var last map[string]any
	for i, line := range strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n") {
		if err := json.Unmarshal([]byte(line), &last); err != nil {
			t.Fatalf("line %d is not JSON: %q", i, line)
		}
		kinds = append(kinds, last["type"].(string))
	}
	want := []string{"run.started", "assistant.message", "tool.call", "tool.result", "assistant.message", "result"}
	if strings.Join(kinds, " ") != strings.Join(want, " ") {
		t.Fatalf("events = %v, want %v", kinds, want)
	}
	if last["status"] != agent.StatusSubmitted || last["submission"] != "all good" || last["duration_ms"] != float64(1000) {
		t.Errorf("result = %#v", last)
	}
}

func TestJSONEventsDisabledWriteNothing(t *testing.T) {
	a := newTestAgent(t, &replies{{Role: "assistant", Content: "done"}})
	var out strings.Builder
	events := startEvents(&out, false, a, false, func() {}, nil)
	if err := events.finish(a.Run(context.Background(), "x"), 0); err != nil || out.Len() != 0 || a.OnEvent != nil {
		t.Errorf("disabled stream wrote %q (err %v, OnEvent set %v)", out.String(), err, a.OnEvent != nil)
	}
}

func TestReadOnlyStartsNoMCPServers(t *testing.T) {
	servers := []config.MCPServer{{Alias: "docs", Command: "docs-mcp", Tools: []string{"search"}}}
	var w strings.Builder
	if got := mcpServers(&w, config.AgentConfig{MCPServers: servers}); len(got) != 1 || w.Len() != 0 {
		t.Errorf("normal mode: servers %v, note %q", got, w.String())
	}
	if got := mcpServers(&w, config.AgentConfig{ReadOnly: true, MCPServers: servers}); got != nil || !strings.Contains(w.String(), "not started") {
		t.Errorf("read-only mode: servers %v, note %q", got, w.String())
	}
}

// TestDeadlineSpentBeforeRunIsTimeLimit covers a wall-clock limit used up by
// MCP discovery: the run still ends as TimeLimitExceeded.
func TestDeadlineSpentBeforeRunIsTimeLimit(t *testing.T) {
	a := newTestAgent(t, &replies{{Role: "assistant", Content: "done"}})
	a.Config.WallTimeSec = 60
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	if tr := a.Run(ctx, "x"); tr.ExitStatus != agent.StatusTimeLimit {
		t.Errorf("status = %s, want %s", tr.ExitStatus, agent.StatusTimeLimit)
	}
}

// wantUsage is the layout from issue #12, with its update: -h, --help listed
// first and -V for --version.
const wantUsage = `Usage: tinyagent -t "<prompt>" [flags]    (or --task "<prompt>")
       echo "<prompt>" | tinyagent -t -
       tinyagent --resume <trajectory.json> --cwd <dir> -o <new.json> [flags]
       tinyagent --resume <finished.json> -t "<follow-up>" --cwd <dir> -o <new.json> [flags]
       tinyagent --rewind <trajectory.json> --cwd <dir> [--to start|<step>|<id>] [--dry-run]
       tinyagent --check [-m <model>] [-c <config>] [--cwd <dir>]

  -h, --help                         show this help and exit
Task:
  -t, --task string                  task/prompt (required unless --resume; with --resume of a finished run, the follow-up instruction); "-" reads it from stdin
  -m, --model string                 provider/model; if unset, $AGENT_MODEL, then the config's model (default "openai/gpt-4o")
      --reasoning-effort level       provider reasoning effort: none, minimal, low, medium, high, xhigh, or max
      --fallback-model spec          provider/model to switch to when requests still fail with 429/5xx/network errors after retries (repeatable, tried in order; replaces fallback_models)
      --no-fallback                  don't fall back to other models (clears fallback_models)
      --openai-api api               openai provider transport: auto, responses, or chat
  -c, --config path                  YAML config (default: user config if present, else built-in)
      --cwd dir                      working directory (default ".")
      --worktree [=name]             run in a new Git worktree on branch tinyagent/<name> from HEAD of the repo containing --cwd, kept afterwards (default name generated)
      --resume path                  continue an unfinished run from a trajectory JSON checkpoint, or a finished one with -t "<follow-up>"
Limits:
      --step-limit n                 max model calls, 0 = unlimited
      --cost-limit usd               max USD, 0 = unlimited
      --time-limit sec               max wall-clock seconds, 0 = unlimited
      --max-format-errors n          stop after this many consecutive steps of malformed tool calls (>= 1) (default 3)
      --max-output-tokens n          per-request output-token budget sent to the provider, 0 = provider default (8192 for anthropic; config max_output_tokens)
      --no-compact                   don't compact the history when it nears the context window
Output:
  -o, --output path                  write trajectory JSON
  -v, --verbose                      print tool outputs
      --no-stream                    wait for full responses instead of streaming
      --json                         write run events as JSONL to stdout
      --output-schema path           end the run with a JSON result validated against this JSON Schema file (printed to stdout)
      --log-db [=path]               also log run events to SQLite (default .tinyagent/tinyagent.db under --cwd); --log-db=false turns config log_db off
      --no-agents-md                 don't add AGENTS.md (nearest, up to the repo root) to the system prompt
      --scoped-agents-md             also deliver AGENTS.md files in directories below --cwd, appended to the first file tool result that touches a path there (default off; config scoped_agents_md)
      --read-only                    allow only read_file, list_files, and grep; blocks edits and commands
      --add-dir-read path            let the file tools (and a container) read this directory outside --cwd; repeatable; with --add-dir-write, replaces config additional_dirs
      --add-dir-write path           let the file tools, --sandbox commands and a container read and write this directory outside --cwd; repeatable; with --add-dir-read, replaces config additional_dirs
      --sandbox                      confine bash and start_process commands: writes only under --cwd, a temp dir and sandbox_writable_paths
      --sandbox-network              with --sandbox, allow commands to use the network
      --sandbox-allow-domain host    with --sandbox, let commands reach only this host (name, *.domain or IP, optional :port) over HTTP(S) via a filtering proxy; repeatable, replaces sandbox_allowed_domains
      --env name                     where bash and start_process run: local or container (default: config's environment, else local)
      --command-env mode             environment bash and start_process inherit: all, core or none (default: config's command_env.inherit, else all)
      --snapshots                    snapshot the workspace before every tool batch that can change files, for --rewind; requires -o (default off; config snapshots)
Rewind:
      --rewind path                  restore --cwd to a workspace snapshot recorded in this trajectory JSON (see --to and --dry-run)
      --to target                    with --rewind: start (the baseline, the default), a step number (the state before that step's tool batch), or a snapshot id
      --dry-run                      with --rewind: print what would change and write nothing
Other:
  -V, --version                      print the version and exit
      --update                       update to the latest release and exit
      --check                        print the resolved config, model, provider key status, fallbacks, cwd and limits, then exit; makes no model request
      --init-config                  write a commented starter config to the user config path (or -c), refusing to overwrite, and exit

Examples:
  tinyagent -t "fix the failing test" -m anthropic/claude-sonnet-4
  cat task.md | tinyagent -t - --step-limit 30 -o run.json
  tinyagent --resume run.json --cwd . --step-limit 60 -o continued.json
  tinyagent --resume review.json -t "fix the races you found" --cwd . -o fix.json
  tinyagent --rewind run.json --cwd . --to 3 --dry-run
  tinyagent --check -m anthropic/claude-sonnet-4
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
	if !strings.Contains(got, `  -m, --model string                 provider/model; if unset, $AGENT_MODEL, then the config's model (default "ollama/llama3.1")`+"\n") {
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
		if !reflect.DeepEqual(a, b) || !reflect.DeepEqual(a, d) {
			t.Errorf("%v, %v and %v parse differently: %+v / %+v / %+v", c[0], c[1], c[2], a, b, d)
		}
		if reflect.DeepEqual(a, parse()) {
			t.Errorf("%v had no effect", c[0])
		}
	}
}

func TestPositionalArgExits2(t *testing.T) {
	// Without the check these runs would read stdin or call a model; both
	// point nowhere useful, so either shows up as an exit code other than 2.
	t.Setenv("AGENT_MODEL", "openai/gpt-4o")
	t.Setenv("OPENAI_API_KEY", "fake")
	t.Setenv("OPENAI_BASE_URL", "http://127.0.0.1:1")
	defer func(f *os.File) { os.Stdin = f }(os.Stdin)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	w.Close()
	defer r.Close()
	os.Stdin = r
	for _, c := range []struct {
		args []string
		bad  string
	}{
		{[]string{"-t", "fix the failing test", "typo", "--step-limit", "5"}, "typo"},
		{[]string{"fix the tests"}, "fix the tests"},
		{[]string{"-t", "x", "--", "extra"}, "extra"},
		{[]string{"--task", "-", "typo"}, "typo"},
		{[]string{"-t", "x", "--", "--step-limit"}, "--step-limit"},
	} {
		code, out, got := runWith(t, c.args...)
		if code != 2 {
			t.Errorf("%q: exit = %d, want 2", c.args, code)
		}
		want := `error: unexpected positional argument "` + c.bad + `"; pass the task with -t/--task`
		if out != "" || !strings.HasPrefix(got, want+"\n") || !strings.Contains(got, "Usage: tinyagent") {
			t.Errorf("%q: stdout = %q, stderr = %q; want %q and usage on stderr", c.args, out, got, want)
		}
	}
}

func TestFlagLookingTaskIsNotPositional(t *testing.T) {
	var o options
	fs := newFlagSet(&o)
	if err := fs.Parse([]string{"-t", "run with --step-limit 5 typo", "--step-limit", "3"}); err != nil {
		t.Fatal(err)
	}
	if fs.NArg() != 0 || o.task != "run with --step-limit 5 typo" || o.stepLimit != 3 {
		t.Errorf("args %q, task %q, step limit %d", fs.Args(), o.task, o.stepLimit)
	}
}

// fakeOpenAI serves one canned reply that submits, counting requests.
func fakeOpenAI(t *testing.T, requests *atomic.Int32) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// captureStderr runs f with os.Stderr redirected and returns what it wrote.
func captureStderr(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func(f *os.File) { os.Stderr = f }(os.Stderr)
	os.Stderr = w
	done := make(chan string)
	go func() { b, _ := io.ReadAll(r); done <- string(b) }()
	f()
	w.Close()
	return <-done
}

func TestUnpricedCostLimitWarning(t *testing.T) {
	const warn = "warning: no pricing available for "
	dir := t.TempDir()
	withLimit := filepath.Join(dir, "limit.yaml")
	if err := os.WriteFile(withLimit, []byte("cost_limit: 2.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	withPrice := filepath.Join(dir, "price.yaml")
	if err := os.WriteFile(withPrice, []byte("cost_limit: 2.0\ninput_price_per_mtok: 1.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	noConfig := filepath.Join(dir, "none.yaml")
	if err := os.WriteFile(noConfig, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { model.PriceOverride = [2]float64{} })
	cases := []struct {
		name string
		args []string
		warn bool
	}{
		{"flag limit, unpriced", []string{"-m", "openai/unlisted-model", "-c", noConfig, "--cost-limit", "1"}, true},
		{"implicit openai", []string{"-m", "unlisted-model", "-c", noConfig, "--cost-limit", "1"}, true},
		{"config limit, unpriced", []string{"-m", "openai/unlisted-model", "-c", withLimit}, true},
		{"explicit zero overrides config", []string{"-m", "openai/unlisted-model", "-c", withLimit, "--cost-limit", "0"}, false},
		{"no limit", []string{"-m", "openai/unlisted-model", "-c", noConfig}, false},
		{"priced model", []string{"-m", "openai/gpt-4o", "-c", withLimit}, false},
		{"price override", []string{"-m", "openai/unlisted-model", "-c", withPrice}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var requests atomic.Int32
			srv := fakeOpenAI(t, &requests)
			t.Setenv("OPENAI_BASE_URL", srv.URL)
			t.Setenv("OPENAI_API_KEY", "fake")
			var code int
			var out string
			got := captureStderr(t, func() {
				code, out, _ = runWith(t, append([]string{"-t", "x", "--no-stream", "--cwd", dir}, c.args...)...)
			})
			if code != 0 || requests.Load() != 1 {
				t.Fatalf("exit = %d, requests = %d; want 0, 1; stderr:\n%s", code, requests.Load(), got)
			}
			if n := strings.Count(got, warn); n != map[bool]int{true: 1, false: 0}[c.warn] {
				t.Fatalf("got %d warnings, want warn=%v; stderr:\n%s", n, c.warn, got)
			}
			if !c.warn {
				return
			}
			want := warn + c.args[1] + "; --cost-limit cannot be enforced. Set input_price_per_mtok and output_price_per_mtok in your config to enable cost tracking.\n"
			if !strings.HasPrefix(got, want) {
				t.Errorf("stderr = %q, want it to start with %q", got, want)
			}
			if strings.Contains(out, "warning") {
				t.Errorf("warning on stdout: %q", out)
			}
		})
	}
}

func TestNoWarningForFreeProviders(t *testing.T) {
	for _, m := range []string{"ollama/llama3.1", "codex/gpt-5"} {
		var b strings.Builder
		warnUnpriced(&b, m, 5)
		if b.String() != "" {
			t.Errorf("%s: unexpected warning %q", m, b.String())
		}
	}
}

func TestInvalidLimitExits2(t *testing.T) {
	// Without the check these runs would read the empty stdin or fail to
	// build the unknown provider, either way exiting 1 rather than 2.
	t.Setenv("AGENT_MODEL", "nope/x")
	defer func(f *os.File) { os.Stdin = f }(os.Stdin)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	w.Close()
	defer r.Close()
	os.Stdin = r
	const (
		step = "error: --step-limit must be >= 0 (0 = unlimited)"
		cost = "error: --cost-limit must be finite and >= 0 (0 = unlimited)"
		wall = "error: --time-limit must be >= 0"
	)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"-t", "x", "--step-limit", "-1"}, step},
		{[]string{"-t", "-", "--step-limit", "-1"}, step},
		{[]string{"-t", "x", "--cost-limit", "-0.50"}, cost},
		{[]string{"-t", "-", "--cost-limit", "-0.50"}, cost},
		{[]string{"-t", "x", "--cost-limit", "NaN"}, cost},
		{[]string{"-t", "x", "--cost-limit", "Inf"}, cost},
		{[]string{"-t", "x", "--cost-limit", "+Inf"}, cost},
		{[]string{"-t", "x", "--cost-limit", "-Inf"}, cost},
		{[]string{"-t", "x", "--cost-limit", "infinity"}, cost},
		{[]string{"-t", "x", "--time-limit", "-1"}, wall},
	} {
		code, out, got := runWith(t, c.args...)
		if code != 2 {
			t.Errorf("%q: exit = %d, want 2", c.args, code)
		}
		if out != "" || got != c.want+"\n" {
			t.Errorf("%q: stdout = %q, stderr = %q; want %q on stderr", c.args, out, got, c.want)
		}
	}
}

func TestValidLimitsPassValidation(t *testing.T) {
	for _, args := range [][]string{
		{"--step-limit", "0", "--cost-limit", "0", "--time-limit", "0"},
		{"--step-limit", "1", "--cost-limit", "0.01", "--time-limit", "1"},
		{"--cost-limit", "1e300"},
	} {
		var o options
		fs := newFlagSet(&o)
		if err := fs.Parse(args); err != nil {
			t.Fatal(err)
		}
		if msg := validateLimits(o); msg != "" {
			t.Errorf("%q: %s", args, msg)
		}
	}
}

func TestApplyFlagOverridesZeroClearsConfig(t *testing.T) {
	var o options
	fs := newFlagSet(&o)
	if err := fs.Parse([]string{"--step-limit", "0", "--cost-limit", "0"}); err != nil {
		t.Fatal(err)
	}
	cfg := config.AgentConfig{StepLimit: 50, CostLimit: 2}
	applyFlagOverrides(fs, &cfg, o.stepLimit, o.costLimit, o.timeLimit)
	if cfg.StepLimit != 0 || cfg.CostLimit != 0 {
		t.Errorf("step limit %d, cost limit %v; want both 0", cfg.StepLimit, cfg.CostLimit)
	}
}

func TestResolveModel(t *testing.T) {
	cases := []struct {
		name, env, cfg string
		args           []string
		want           string
	}{
		{"built-in default", "", "", nil, "openai/gpt-4o"},
		{"config", "", "ollama/llama3.1", nil, "ollama/llama3.1"},
		{"env wins over config", "anthropic/claude-sonnet-4", "ollama/llama3.1", nil, "anthropic/claude-sonnet-4"},
		{"--model wins over env and config", "anthropic/claude-sonnet-4", "ollama/llama3.1", []string{"--model", "openrouter/x"}, "openrouter/x"},
		{"-m wins over env and config", "anthropic/claude-sonnet-4", "ollama/llama3.1", []string{"-m", "openrouter/x"}, "openrouter/x"},
		{"explicit built-in default wins over config", "", "ollama/llama3.1", []string{"-m", "openai/gpt-4o"}, "openai/gpt-4o"},
		{"explicit env value wins over config", "ollama/llama3.1", "codex/gpt-5", []string{"--model", "ollama/llama3.1"}, "ollama/llama3.1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("AGENT_MODEL", c.env)
			var o options
			fs := newFlagSet(&o)
			if err := fs.Parse(c.args); err != nil {
				t.Fatal(err)
			}
			if got := resolveModel(fs, o.modelName, c.cfg); got != c.want {
				t.Errorf("resolveModel = %q, want %q", got, c.want)
			}
		})
	}
}

func TestConfigModelReachesRun(t *testing.T) {
	t.Setenv("AGENT_MODEL", "")
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("model: openai/from-config\ncost_limit: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "run.json")
	var requests atomic.Int32
	srv := fakeOpenAI(t, &requests)
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	t.Setenv("OPENAI_API_KEY", "fake")
	var code int
	stderr := captureStderr(t, func() {
		code, _, _ = runWith(t, "-t", "x", "--no-stream", "--cwd", dir, "-c", cfgPath, "-o", out)
	})
	if code != 0 || requests.Load() != 1 {
		t.Fatalf("exit = %d, requests = %d; want 0, 1; stderr:\n%s", code, requests.Load(), stderr)
	}
	if !strings.Contains(stderr, "warning: no pricing available for openai/from-config;") {
		t.Errorf("pricing warning doesn't name the config's model; stderr:\n%s", stderr)
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	var tr trajectory.Trajectory
	if err := json.Unmarshal(b, &tr); err != nil {
		t.Fatal(err)
	}
	if tr.Model != "openai/from-config" {
		t.Errorf("trajectory model = %q, want openai/from-config", tr.Model)
	}
}

func TestHelpIgnoresMalformedUserConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AppData", filepath.Join(home, "AppData"))
	p, err := config.DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("model: [not a string\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, got, _ := runWith(t, "--help"); code != 0 || !strings.Contains(got, "the config's model") {
		t.Errorf("--help exit = %d, want 0 and the model fallback documented:\n%s", code, got)
	}
}

func TestInvalidHookConfigFailsBeforeProvider(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("hooks:\n  post_tool:\n    - command: fmt-check\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var requests atomic.Int32
	srv := fakeOpenAI(t, &requests)
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	t.Setenv("OPENAI_API_KEY", "fake")
	var code int
	stderr := captureStderr(t, func() {
		code, _, _ = runWith(t, "-t", "x", "-m", "openai/gpt-4o", "--no-stream", "--cwd", dir, "-c", cfgPath)
	})
	if code != 1 || requests.Load() != 0 {
		t.Fatalf("exit = %d, requests = %d; want 1, 0", code, requests.Load())
	}
	if !strings.Contains(stderr, cfgPath) || !strings.Contains(stderr, "hooks.post_tool[0]: command \"fmt-check\" must be an absolute path") {
		t.Errorf("stderr = %q", stderr)
	}
}
func TestEnvFlag(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if code, _, got := runWith(t, "--env", "bogus", "-t", "x"); code != 2 || !strings.Contains(got, `--env "bogus"`) {
		t.Errorf("--env bogus: exit %d, stderr %q; want 2", code, got)
	}
	if code, _, _ := runWith(t, "--env", "container", "--sandbox", "-t", "x"); code != 2 {
		t.Errorf("--env container --sandbox: exit %d, want 2", code)
	}
	// A missing runtime fails before any provider is contacted.
	cfg := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(cfg, []byte("container: {runtime: podman, image: alpine}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	if code, _, _ := runWith(t, "--env", "container", "-c", cfg, "-t", "x", "-m", "ollama/none"); code != 1 {
		t.Errorf("missing runtime: exit %d, want 1", code)
	}
}

func TestSandboxAllowDomainValidation(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	var requests atomic.Int32
	srv := fakeOpenAI(t, &requests)
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	t.Setenv("OPENAI_API_KEY", "fake")
	base := []string{"-t", "x", "-m", "openai/gpt-4o", "--no-stream", "--cwd", t.TempDir()}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--sandbox-allow-domain", "example.com"}, "requires --sandbox"},
		{[]string{"--sandbox", "--sandbox-network", "--sandbox-allow-domain", "example.com"}, "can't be combined with --sandbox-network"},
		{[]string{"--sandbox", "--sandbox-allow-domain", "*"}, "bare wildcard"},
	} {
		var code int
		var flagErr string
		stderr := captureStderr(t, func() {
			code, _, flagErr = runWith(t, append(append([]string{}, tc.args...), base...)...)
		})
		stderr += flagErr
		if code != 2 || !strings.Contains(stderr, tc.want) {
			t.Errorf("%v: exit %d, stderr %q; want 2 and %q", tc.args, code, stderr, tc.want)
		}
	}
	if requests.Load() != 0 {
		t.Errorf("%d provider requests before validation failed", requests.Load())
	}
}

func TestSandboxAllowDomainFlagReplacesConfig(t *testing.T) {
	var o options
	fs := newFlagSet(&o)
	if err := fs.Parse([]string{"--sandbox-allow-domain", "a.test", "--sandbox-allow-domain", "*.b.test:8080"}); err != nil {
		t.Fatal(err)
	}
	cfg := config.AgentConfig{SandboxAllowedDomains: []string{"config.test"}}
	applySandboxFlags(fs, &cfg, o)
	if !reflect.DeepEqual(cfg.SandboxAllowedDomains, []string{"a.test", "*.b.test:8080"}) {
		t.Errorf("domains = %v", cfg.SandboxAllowedDomains)
	}
	cfg = config.AgentConfig{SandboxAllowedDomains: []string{"config.test"}}
	var o2 options
	fs = newFlagSet(&o2)
	fs.Parse(nil)
	applySandboxFlags(fs, &cfg, o2)
	if !reflect.DeepEqual(cfg.SandboxAllowedDomains, []string{"config.test"}) {
		t.Errorf("config domains without the flag = %v", cfg.SandboxAllowedDomains)
	}
}

func TestConfiguredSecretsIncludesHookEnv(t *testing.T) {
	t.Setenv("STOP_HOOK_TOKEN", "s3cret-stop")
	t.Setenv("PRE_HOOK_TOKEN", "s3cret-pre")
	cfg := config.Default()
	cfg.Hooks.Stop = []config.Hook{{Command: "/bin/true", Env: []string{"STOP_HOOK_TOKEN"}}}
	cfg.Hooks.PreTool = []config.Hook{{Command: "/bin/true", Env: []string{"PRE_HOOK_TOKEN"}}}
	got := strings.Join(configuredSecrets(cfg), ",")
	if !strings.Contains(got, "s3cret-stop") || !strings.Contains(got, "s3cret-pre") {
		t.Errorf("secrets = %q", got)
	}
}

func TestResultFieldsStopReason(t *testing.T) {
	f := resultFields(&trajectory.Trajectory{ExitStatus: agent.StatusVerificationFailed, StopContinuations: 5, StopReason: "r"}, time.Second)
	if f["stop_reason"] != "r" || f["stop_continuations"] != 5 {
		t.Errorf("fields = %v", f)
	}
	f = resultFields(&trajectory.Trajectory{ExitStatus: agent.StatusSubmitted}, time.Second)
	if _, ok := f["stop_reason"]; ok {
		t.Errorf("fields = %v", f)
	}
}
