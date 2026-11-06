package model

// The claude-code provider hands the task to the user's installed Claude
// Code CLI in print (non-interactive) mode. Every flag, output field, exit
// code and auth rule below comes from Anthropic's official docs:
//
//   - https://code.claude.com/docs/en/cli-reference
//     -p/--print, --output-format (text, json, stream-json), --verbose,
//     --include-partial-messages (requires --print and stream-json),
//     --model (aliases or full names), --tools (restrict built-in tools),
//     --permission-mode (dontAsk). stream-json is rejected without --verbose.
//   - https://code.claude.com/docs/en/headless
//     print mode reads stdin (piped stdin capped at 10MB); exit 0 on
//     success and non-zero on failure; failures inside the run, such as
//     missing authentication, are printed as the result on stdout; the
//     json payload holds result, session ID, usage and total_cost_usd;
//     --bare never reads OAuth credentials, so it is NOT used here; dontAsk
//     denies anything that would prompt; SIGTERM exits 143. Streaming is
//     `--output-format stream-json --verbose --include-partial-messages`:
//     each line is one event, text arrives as stream_event
//     content_block_delta text_delta, and the last line is the same result
//     object as --output-format json.
//   - https://code.claude.com/docs/en/agent-sdk/streaming-output
//     with partial messages, text_delta chunks are followed by an assistant
//     message for that block (the same text; do not emit it twice) and by
//     tool_use blocks. tool_result blocks on user messages carry the tool's
//     output. Without partial messages, only complete assistant messages
//     arrive.
//   - https://code.claude.com/docs/en/agent-sdk/typescript (SDKResultMessage)
//     field names: type, subtype, is_error, result, session_id, errors,
//     total_cost_usd, usage{input_tokens, output_tokens,
//     cache_creation_input_tokens, cache_read_input_tokens}. The CLI
//     reference doesn't give the json schema itself, so it is parsed
//     defensively and raw stdout is the fallback.
//   - https://code.claude.com/docs/en/agent-sdk/cost-tracking
//     total_cost_usd is a client-side estimate, not billing data.
//   - https://code.claude.com/docs/en/authentication
//     sign in with /login (Pro/Max subscription); credential precedence,
//     including that -p always uses ANTHROPIC_API_KEY when it is set.
//   - https://code.claude.com/docs/en/legal-and-compliance
//     developers may not collect, store or intermediate Claude.ai
//     credentials; running the unmodified Claude Code binary signed in with
//     the user's own subscription is allowed. Hence no credential access.
//   - https://code.claude.com/docs/en/setup  (installation, for the error)

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ClaudeCodeProvider is the provider id of the Claude Code handoff.
const ClaudeCodeProvider = "claude-code"

// HandoffDefaultModel as the model name sends no --model flag, so the CLI's
// own configured model applies.
const HandoffDefaultModel = "default"

// claudeReadOnlyTools are the built-in tools a read-only run keeps: file
// reads and search, matching TinyAgent's read_file, list_files and grep.
const claudeReadOnlyTools = "Read,Glob,Grep"

// claudeStdinInstruction is the prompt argument; the task itself is piped
// on stdin so long prompts aren't limited by argv size.
const claudeStdinInstruction = "Your complete task is the input piped on stdin. Do what it asks and reply with your final answer."

var claudeAuthMarkers = []string{"not logged in", "/login", "login expired", "invalid api key", "authentication_error", "oauth token"}

// ClaudeCode runs `claude -p` for each query.
type ClaudeCode struct {
	Path   string       // resolved claude binary
	Model  string       // "" or HandoffDefaultModel sends no --model
	OnText func(string) // if set, stream; nil keeps today's --output-format json
	HandoffOptions
}

// NewClaudeCode resolves the claude binary for model name.
func NewClaudeCode(name string, opts HandoffOptions) (*ClaudeCode, error) {
	path, err := findCLI(ClaudeCodeProvider, "claude", MissingCLIError{
		Install: "https://code.claude.com/docs/en/setup",
		SignIn:  "run `claude`, then /login with your Claude account",
		Docs:    "https://code.claude.com/docs/en/authentication",
	})
	if err != nil {
		return nil, err
	}
	return &ClaudeCode{Path: path, Model: name, HandoffOptions: opts}, nil
}

// claude output modes. json is the non-streaming invocation (--no-stream and
// any CLI that rejects the streaming flags). stream is stream-json without
// token deltas. partial adds --include-partial-messages.
const (
	claudeOutJSON = iota
	claudeOutStream
	claudeOutPartial
)

// Args returns the claude arguments for one non-streaming run.
func (c *ClaudeCode) Args() []string { return c.args(claudeOutJSON) }

func (c *ClaudeCode) args(mode int) []string {
	// https://code.claude.com/docs/en/headless (Stream responses) and
	// https://code.claude.com/docs/en/cli-reference (--output-format,
	// --verbose, --include-partial-messages). stream-json requires --verbose
	// in print mode. --include-partial-messages needs print and stream-json;
	// a CLI that rejects it is retried without it, then without stream-json.
	format := "json"
	var stream []string
	switch mode {
	case claudeOutPartial:
		format = "stream-json"
		stream = []string{"--verbose", "--include-partial-messages"}
	case claudeOutStream:
		format = "stream-json"
		stream = []string{"--verbose"}
	}
	args := []string{"-p", "--output-format", format}
	args = append(args, stream...)
	if c.Model != "" && c.Model != HandoffDefaultModel {
		args = append(args, "--model", c.Model)
	}
	if c.ReadOnly {
		args = append(args, "--tools", claudeReadOnlyTools, "--permission-mode", "dontAsk")
	}
	return append(args, claudeStdinInstruction)
}

type claudeResult struct {
	Type         string   `json:"type"`
	Subtype      string   `json:"subtype"`
	IsError      bool     `json:"is_error"`
	Result       string   `json:"result"`
	SessionID    string   `json:"session_id"`
	Errors       []string `json:"errors"`
	TotalCostUSD float64  `json:"total_cost_usd"`
	Usage        struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	} `json:"usage"`
}

// parseClaudeOutput decodes the json result. Some versions or settings may
// print other lines before it, so the last line that decodes to an object
// with a result type or result field wins.
func parseClaudeOutput(stdout []byte) (claudeResult, bool) {
	var r claudeResult
	if json.Unmarshal(stdout, &r) == nil && (r.Type != "" || r.Result != "" || r.SessionID != "") {
		return r, true
	}
	lines := strings.Split(strings.TrimSpace(string(stdout)), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		var l claudeResult
		if json.Unmarshal([]byte(lines[i]), &l) == nil && (l.Type == "result" || l.Result != "") {
			return l, true
		}
	}
	return claudeResult{}, false
}

func (c *ClaudeCode) Query(ctx context.Context, messages []Message, _ []ToolSpec) (Message, Cost, error) {
	res, live, err := c.run(ctx, handoffPrompt(messages))
	if err != nil {
		return Message{}, Cost{}, err
	}
	r, parsed := parseClaudeOutput(res.stdout)
	// Subscription usage isn't API spend, and total_cost_usd is only a
	// client-side estimate, so USD stays 0; tokens are still reported.
	cost := Cost{
		InputTokens:      r.Usage.InputTokens + r.Usage.CacheCreationInputTokens + r.Usage.CacheReadInputTokens,
		OutputTokens:     r.Usage.OutputTokens,
		CacheReadTokens:  r.Usage.CacheReadInputTokens,
		CacheWriteTokens: r.Usage.CacheCreationInputTokens,
	}
	failed := res.exitCode != 0 || (parsed && (r.IsError || (r.Subtype != "" && r.Subtype != "success")))
	if failed {
		detail := r.Result
		if len(r.Errors) > 0 {
			detail = strings.TrimSpace(detail + " " + strings.Join(r.Errors, "; "))
		}
		if !parsed || detail == "" {
			detail = strings.TrimSpace(tail(res.stdout, 1000) + " " + tail(res.stderr, 1000))
		}
		if r.Subtype != "" && r.Subtype != "success" {
			detail = r.Subtype + ": " + detail
		}
		msg := fmt.Sprintf("claude-code: claude exited %d: %s", res.exitCode, detail)
		if authFailure(detail+" "+string(res.stderr), claudeAuthMarkers) {
			msg += "; sign in by running `claude` and then /login (https://code.claude.com/docs/en/authentication)"
		}
		if r.SessionID != "" {
			msg += " (session " + r.SessionID + ")"
		}
		return Message{}, cost, fmt.Errorf("%s", msg)
	}
	text := r.Result
	if !parsed {
		text = strings.TrimSpace(string(res.stdout))
	}
	if live != nil {
		live.text.finish(text)
	}
	out := Message{Role: "assistant", Content: text}
	if r.SessionID != "" {
		out.Extra = map[string]any{"handoff_session_id": r.SessionID}
	}
	return out, cost, nil
}

// run executes claude. With OnText set it asks for stream-json and reads
// events as they arrive; a CLI that rejects those flags is retried with the
// non-streaming invocation. Without OnText the arguments are today's.
func (c *ClaudeCode) run(ctx context.Context, prompt string) (cliResult, *claudeLive, error) {
	if c.OnText == nil {
		res, err := runCLI(ctx, ClaudeCodeProvider, c.Path, c.args(claudeOutJSON), c.Dir, prompt)
		return res, nil, err
	}
	modes := []struct {
		mode  int
		flags []string
	}{
		{claudeOutPartial, []string{"include-partial-messages"}},
		{claudeOutStream, []string{"output-format", "stream-json"}},
		{claudeOutJSON, nil},
	}
	var res cliResult
	var live *claudeLive
	for i, m := range modes {
		live = &claudeLive{text: textSink{onText: c.OnText}, activity: activityFrom(ctx)}
		var err error
		if m.mode == claudeOutJSON {
			res, err = runCLI(ctx, ClaudeCodeProvider, c.Path, c.args(m.mode), c.Dir, prompt)
		} else {
			res, err = runCLIStream(ctx, ClaudeCodeProvider, c.Path, c.args(m.mode), c.Dir, prompt, nil, live.line)
		}
		if err != nil {
			return res, live, err
		}
		if i == len(modes)-1 || !streamFlagRejected(res, live.sawEvent, m.flags...) {
			break
		}
	}
	return res, live, nil
}

// claudeLive turns stream-json lines into assistant text and tool activity.
// The result line is left to parseClaudeOutput.
//
// https://code.claude.com/docs/en/headless
// https://code.claude.com/docs/en/agent-sdk/streaming-output
type claudeLive struct {
	text      textSink
	activity  activityFunc
	sawEvent  bool
	gotDelta  bool
	toolID    string
	toolName  string
	toolInput strings.Builder
	names     map[string]string
	announced map[string]bool
}

type claudeBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
}

func (s *claudeLive) line(line string) {
	var ev struct {
		Type    string `json:"type"`
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
		Event struct {
			Type  string `json:"type"`
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				PartialJSON string `json:"partial_json"`
			} `json:"delta"`
			ContentBlock struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"content_block"`
		} `json:"event"`
	}
	if json.Unmarshal([]byte(line), &ev) != nil || ev.Type == "" {
		return
	}
	s.sawEvent = true
	switch ev.Type {
	case "stream_event":
		switch ev.Event.Type {
		case "content_block_start":
			if ev.Event.ContentBlock.Type == "tool_use" {
				s.toolID = ev.Event.ContentBlock.ID
				s.toolName = ev.Event.ContentBlock.Name
				s.toolInput.Reset()
				s.remember(s.toolID, s.toolName)
			}
		case "content_block_delta":
			switch ev.Event.Delta.Type {
			case "text_delta":
				// Token text. The matching assistant message repeats it.
				if ev.Event.Delta.Text != "" {
					s.gotDelta = true
					s.text.write(ev.Event.Delta.Text)
				}
			case "input_json_delta":
				s.toolInput.WriteString(ev.Event.Delta.PartialJSON)
			}
		case "content_block_stop":
			if s.toolID != "" {
				s.announce(s.toolID, s.toolName, s.toolInput.String())
				s.toolID, s.toolName = "", ""
				s.toolInput.Reset()
			}
		}
	case "assistant":
		for _, b := range claudeBlocks(ev.Message.Content) {
			switch b.Type {
			case "text":
				if !s.gotDelta && b.Text != "" {
					s.text.write(b.Text)
				}
			case "tool_use":
				s.remember(b.ID, b.Name)
				s.announce(b.ID, b.Name, string(b.Input))
			}
		}
		s.gotDelta = false
	case "user":
		for _, b := range claudeBlocks(ev.Message.Content) {
			if b.Type != "tool_result" {
				continue
			}
			if out := claudeText(b.Content); out != "" {
				s.output(b.ToolUseID, out)
			}
		}
	}
}

func (s *claudeLive) remember(id, name string) {
	if id == "" {
		return
	}
	if name == "" {
		name = "tool"
	}
	if s.names == nil {
		s.names = map[string]string{}
	}
	s.names[id] = name
}

func (s *claudeLive) announce(id, name, input string) {
	if id == "" {
		id = name
	}
	if id == "" {
		return
	}
	if s.announced == nil {
		s.announced = map[string]bool{}
	}
	if s.announced[id] {
		return
	}
	s.announced[id] = true
	if name == "" {
		name = s.names[id]
	}
	if name == "" {
		name = "tool"
	}
	s.remember(id, name)
	s.act(id, name, claudeToolLine(name, input))
}

func (s *claudeLive) output(id, chunk string) {
	name := "tool"
	if s.names != nil && s.names[id] != "" {
		name = s.names[id]
	}
	if id == "" {
		id = name
	}
	if !strings.HasSuffix(chunk, "\n") {
		chunk += "\n"
	}
	s.act(id, name, chunk)
}

func (s *claudeLive) act(id, name, chunk string) {
	if s.activity == nil || chunk == "" {
		return
	}
	s.activity(id, name, chunk)
}

// claudeToolLine is one preview line: the tool name and the argument that
// says what it does (a command or a path), when that JSON parsed.
func claudeToolLine(name, input string) string {
	var m map[string]any
	if json.Unmarshal([]byte(input), &m) == nil {
		for _, k := range []string{"command", "file_path", "path", "pattern", "query", "url"} {
			s, _ := m[k].(string)
			s = strings.Join(strings.Fields(s), " ")
			if s == "" {
				continue
			}
			if len(s) > 200 {
				s = s[:200] + "…"
			}
			return name + " " + s + "\n"
		}
	}
	if name == "" {
		name = "tool"
	}
	return name + "\n"
}

func claudeBlocks(raw json.RawMessage) []claudeBlock {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return nil
		}
		return []claudeBlock{{Type: "text", Text: s}}
	}
	var blocks []claudeBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return nil
	}
	return blocks
}

// claudeText reads a tool_result content string or a list of text blocks.
func claudeText(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return ""
		}
		return s
	}
	var blocks []claudeBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var b strings.Builder
	for _, bl := range blocks {
		if bl.Text != "" {
			b.WriteString(bl.Text)
		} else if len(bl.Content) > 0 {
			b.WriteString(claudeText(bl.Content))
		}
	}
	return b.String()
}
