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
//     authentication.
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
	Path  string // resolved codex binary
	Model string // "" or HandoffDefaultModel sends no -m
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
	res, err := runCLI(ctx, CodexExecProvider, c.Path, c.Args(), c.Dir, handoffPrompt(messages))
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
	out := Message{Role: "assistant", Content: text}
	if r.threadID != "" {
		out.Extra = map[string]any{"handoff_session_id": r.threadID}
	}
	return out, cost, nil
}
