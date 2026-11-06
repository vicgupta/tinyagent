package model

// The codex-exec provider hands the task to the user's installed Codex CLI
// through `codex exec`, OpenAI's documented non-interactive route. (The
// separate `codex` provider talks to the ChatGPT backend directly.) Every
// flag, event and auth rule below comes from OpenAI's official docs:
//
//   - https://developers.openai.com/codex/noninteractive
//     `codex exec`, reading the prompt from stdin with `-`, --json turning
//     stdout into JSONL events (thread.started, turn.started,
//     turn.completed with usage, turn.failed, item.*, error; agent_message
//     items carry the answer text), stdout being only the final message
//     without --json, default read-only sandbox, exec reusing saved CLI
//     authentication. Item types include agent messages, reasoning, command
//     executions, file changes, MCP tool calls, web searches and plan
//     updates. command_execution items carry command, aggregated_output,
//     exit_code and status (in_progress, then completed or failed).
//   - https://github.com/openai/codex/blob/main/codex-rs/exec/src/exec_events.rs
//     item.started / item.updated / item.completed share one item id;
//     agent_message.text is the answer; command_execution.aggregated_output
//     grows across events (the new suffix is the live output).
//   - https://developers.openai.com/codex/cli/reference
//     exec flags: --json, --cd/-C, --sandbox/-s (read-only,
//     workspace-write), --model/-m.
//   - https://developers.openai.com/codex/security
//     what the read-only and workspace-write sandboxes allow.
//   - https://developers.openai.com/codex/auth
//     `codex login` (ChatGPT sign-in or API key) and where credentials are
//     cached; ChatGPT sign-in usage counts against the plan.
//   - https://developers.openai.com/codex/auth/ci-cd-auth
//     let Codex refresh auth.json itself; never copy or proxy it. Hence no
//     credential access here.
//   - https://developers.openai.com/codex/pricing (plan usage vs API key)
//   - https://github.com/openai/codex (OpenAI's official repo): install
//     instructions; docs/exec.md points to the noninteractive page.
//
// The docs don't give the shape of turn.failed and error events or the
// exit status on failure, so those are parsed defensively: a "message"
// field at the top level or under "error", any non-zero exit, and raw
// stdout as the fallback answer when no JSONL events parse.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// CodexExecProvider is the provider id of the codex exec handoff.
const CodexExecProvider = "codex-exec"

var codexAuthMarkers = []string{"codex login", "not logged in", "unauthorized", "401", "authentication", "refresh token"}

// CodexExec runs `codex exec --json` for each query.
type CodexExec struct {
	Path   string       // resolved codex binary
	Model  string       // "" or HandoffDefaultModel sends no -m
	OnText func(string) // if set, read the JSONL as it arrives; flags stay --json
	HandoffOptions
}

// NewCodexExec resolves the codex binary for model name.
func NewCodexExec(name string, opts HandoffOptions) (*CodexExec, error) {
	path, err := findCLI(CodexExecProvider, "codex", MissingCLIError{
		Install: "https://github.com/openai/codex#quickstart",
		SignIn:  "run `codex login`",
		Docs:    "https://developers.openai.com/codex/auth",
	})
	if err != nil {
		return nil, err
	}
	return &CodexExec{Path: path, Model: name, HandoffOptions: opts}, nil
}

// Args returns the codex arguments for one run; the prompt goes on stdin.
func (c *CodexExec) Args() []string {
	sandbox := "workspace-write"
	if c.ReadOnly {
		sandbox = "read-only"
	}
	args := []string{"exec", "--json", "--sandbox", sandbox}
	if c.Dir != "" {
		args = append(args, "--cd", c.Dir)
	}
	if c.Model != "" && c.Model != HandoffDefaultModel {
		args = append(args, "--model", c.Model)
	}
	return append(args, "-")
}

type codexEvent struct {
	Type     string `json:"type"`
	ThreadID string `json:"thread_id"`
	Message  string `json:"message"`
	Error    *struct {
		Message string `json:"message"`
	} `json:"error"`
	Item *struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"item"`
	Usage *struct {
		InputTokens       int `json:"input_tokens"`
		CachedInputTokens int `json:"cached_input_tokens"`
		OutputTokens      int `json:"output_tokens"`
	} `json:"usage"`
}

type codexRun struct {
	parsed   bool
	threadID string
	answer   string
	failures []string
	cost     Cost
}

func parseCodexOutput(stdout []byte) codexRun {
	var r codexRun
	sc := bufio.NewScanner(bytes.NewReader(stdout))
	sc.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
	for sc.Scan() {
		var e codexEvent
		if json.Unmarshal(sc.Bytes(), &e) != nil || e.Type == "" {
			continue
		}
		r.parsed = true
		switch e.Type {
		case "thread.started":
			r.threadID = e.ThreadID
		case "item.completed":
			if e.Item != nil && e.Item.Type == "agent_message" {
				r.answer = e.Item.Text
			}
		case "turn.completed":
			if u := e.Usage; u != nil {
				r.cost.InputTokens += u.InputTokens
				r.cost.CacheReadTokens += u.CachedInputTokens
				r.cost.OutputTokens += u.OutputTokens
			}
		case "turn.failed", "error":
			msg := e.Message
			if e.Error != nil && e.Error.Message != "" {
				msg = e.Error.Message
			}
			if msg == "" {
				msg = e.Type
			}
			r.failures = append(r.failures, msg)
		}
	}
	return r
}

func (c *CodexExec) Query(ctx context.Context, messages []Message, _ []ToolSpec) (Message, Cost, error) {
	var live *codexLive
	run := runCLI
	if c.OnText != nil {
		// --json is already a JSONL stream (developers.openai.com/codex/noninteractive).
		// Streaming does not change the flags; it only reads each line as it arrives.
		live = &codexLive{text: textSink{onText: c.OnText}, activity: activityFrom(ctx)}
		run = func(ctx context.Context, provider, path string, args []string, dir, stdin string) (cliResult, error) {
			return runCLIStream(ctx, provider, path, args, dir, stdin, nil, live.line)
		}
	}
	res, err := run(ctx, CodexExecProvider, c.Path, c.Args(), c.Dir, handoffPrompt(messages))
	if err != nil {
		return Message{}, Cost{}, err
	}
	r := parseCodexOutput(res.stdout)
	// codex exec reports tokens but no price, and ChatGPT-plan usage isn't
	// API spend, so USD stays 0.
	cost := r.cost
	if res.exitCode != 0 || len(r.failures) > 0 {
		detail := strings.Join(r.failures, "; ")
		if detail == "" {
			detail = tail(res.stderr, 1500)
		}
		if detail == "" {
			detail = tail(res.stdout, 1500)
		}
		msg := fmt.Sprintf("codex-exec: codex exited %d: %s", res.exitCode, detail)
		if authFailure(detail+" "+string(res.stderr), codexAuthMarkers) {
			msg += "; sign in with `codex login` (https://developers.openai.com/codex/auth)"
		}
		if r.threadID != "" {
			msg += " (thread " + r.threadID + ")"
		}
		return Message{}, cost, fmt.Errorf("%s", msg)
	}
	text := r.answer
	if !r.parsed {
		text = strings.TrimSpace(string(res.stdout))
	}
	if live != nil {
		live.text.finish(text)
	}
	out := Message{Role: "assistant", Content: text}
	if r.threadID != "" {
		out.Extra = map[string]any{"handoff_session_id": r.threadID}
	}
	return out, cost, nil
}

// codexLive reports agent_message text and command/tool activity from JSONL
// events as they arrive. parseCodexOutput still owns the final answer and usage.
//
// https://developers.openai.com/codex/noninteractive
// https://github.com/openai/codex/blob/main/codex-rs/exec/src/exec_events.rs
type codexLive struct {
	text     textSink
	activity activityFunc
	sent     map[string]string // item id -> agent text already emitted
	out      map[string]string // item id -> command output already emitted
	started  map[string]bool
}

func (s *codexLive) line(line string) {
	var e struct {
		Type string `json:"type"`
		Item *struct {
			ID               string `json:"id"`
			Type             string `json:"type"`
			Text             string `json:"text"`
			Command          string `json:"command"`
			AggregatedOutput string `json:"aggregated_output"`
			Status           string `json:"status"`
			Server           string `json:"server"`
			Tool             string `json:"tool"`
			Query            string `json:"query"`
			Changes          []struct {
				Path string `json:"path"`
				Kind string `json:"kind"`
			} `json:"changes"`
		} `json:"item"`
	}
	if json.Unmarshal([]byte(line), &e) != nil || e.Item == nil || e.Item.Type == "" {
		return
	}
	if e.Type != "item.started" && e.Type != "item.updated" && e.Type != "item.completed" {
		return
	}
	it := e.Item
	id := it.ID
	if id == "" {
		id = it.Type
	}
	switch it.Type {
	case "agent_message":
		s.emitText(id, it.Text)
	case "command_execution":
		if it.Command != "" && !s.started[id] {
			s.markStarted(id)
			s.act(id, "command", "$ "+oneLine(it.Command)+"\n")
		}
		s.emitOut(id, "command", it.AggregatedOutput)
	case "mcp_tool_call":
		name := strings.Trim(it.Server+"/"+it.Tool, "/")
		if name == "" {
			name = "mcp"
		}
		if !s.started[id] {
			s.markStarted(id)
			s.act(id, name, name+"\n")
		}
	case "web_search":
		if e.Type == "item.completed" && !s.started[id] {
			s.markStarted(id)
			line := "web_search"
			if it.Query != "" {
				line += " " + oneLine(it.Query)
			}
			s.act(id, "web_search", line+"\n")
		}
	case "file_change":
		if e.Type == "item.completed" && !s.started[id] && len(it.Changes) > 0 {
			s.markStarted(id)
			var b strings.Builder
			for _, c := range it.Changes {
				fmt.Fprintf(&b, "%s %s\n", c.Kind, c.Path)
			}
			s.act(id, "file_change", b.String())
		}
	}
}

func (s *codexLive) markStarted(id string) {
	if s.started == nil {
		s.started = map[string]bool{}
	}
	s.started[id] = true
}

func (s *codexLive) emitText(id, text string) {
	if text == "" {
		return
	}
	prev := s.sent[id]
	if prev == text || (prev != "" && !strings.HasPrefix(text, prev)) {
		return
	}
	if s.sent == nil {
		s.sent = map[string]string{}
	}
	s.sent[id] = text
	s.text.write(text[len(prev):])
}

func (s *codexLive) emitOut(id, name, text string) {
	if text == "" {
		return
	}
	prev := s.out[id]
	if prev == text || (prev != "" && !strings.HasPrefix(text, prev)) {
		return
	}
	if s.out == nil {
		s.out = map[string]string{}
	}
	s.out[id] = text
	chunk := text[len(prev):]
	if !strings.HasSuffix(chunk, "\n") {
		chunk += "\n"
	}
	s.act(id, name, chunk)
}

func (s *codexLive) act(id, name, chunk string) {
	if s.activity == nil || chunk == "" {
		return
	}
	s.activity(id, name, chunk)
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}
