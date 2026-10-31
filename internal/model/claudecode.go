package model

// The claude-code provider hands the task to the user's installed Claude
// Code CLI in print (non-interactive) mode. Every flag, output field, exit
// code and auth rule below comes from Anthropic's official docs:
//
//   - https://code.claude.com/docs/en/cli-reference
//     -p/--print, --output-format json, --model (aliases or full names),
//     --tools (restrict built-in tools), --permission-mode (dontAsk).
//   - https://code.claude.com/docs/en/headless
//     print mode reads stdin (piped stdin capped at 10MB); exit 0 on
//     success and non-zero on failure; failures inside the run, such as
//     missing authentication, are printed as the result on stdout; the
//     json payload holds result, session ID, usage and total_cost_usd;
//     --bare never reads OAuth credentials, so it is NOT used here; dontAsk
//     denies anything that would prompt; SIGTERM exits 143.
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
	Path  string // resolved claude binary
	Model string // "" or HandoffDefaultModel sends no --model
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

// Args returns the claude arguments for one run.
func (c *ClaudeCode) Args() []string {
	args := []string{"-p", "--output-format", "json"}
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
	res, err := runCLI(ctx, ClaudeCodeProvider, c.Path, c.Args(), c.Dir, handoffPrompt(messages))
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
	out := Message{Role: "assistant", Content: text}
	if r.SessionID != "" {
		out.Extra = map[string]any{"handoff_session_id": r.SessionID}
	}
	return out, cost, nil
}
