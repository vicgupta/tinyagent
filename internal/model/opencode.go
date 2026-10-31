package model

// The opencode provider hands the task to the user's installed OpenCode CLI
// through `opencode run`, OpenCode's documented non-interactive route.
// Every flag, variable and rule below comes from OpenCode's official docs:
//
//   - https://opencode.ai/docs/cli/
//     `opencode run [message..]` runs non-interactively; --model/-m takes
//     provider/model; --format json prints raw JSON events (default is
//     formatted text); --agent picks the agent; --dir is the directory to
//     run in; --auto auto-approves permissions (not used here);
//     --version/-v prints the version; OPENCODE_CONFIG_CONTENT is inline
//     JSON config.
//   - https://opencode.ai/docs/config/
//     OPENCODE_CONFIG_CONTENT is loaded after the global and project
//     configs ("runtime overrides"); only managed settings outrank it.
//   - https://opencode.ai/docs/agents/
//     the built-in `plan` agent is restricted for analysis without changes;
//     agent permissions take precedence over top-level ones.
//   - https://opencode.ai/docs/permissions/
//     the permission keys (edit, bash, webfetch, websearch, task,
//     external_directory, question, ...) and allow/ask/deny.
//   - https://opencode.ai/docs/providers/ and https://opencode.ai/docs/models/
//     `opencode auth login` stores provider credentials; models are
//     provider/model ids; `opencode models` lists them.
//   - https://opencode.ai/docs/ (install: curl script, npm i -g opencode-ai,
//     brew) and https://github.com/anomalyco/opencode (README).
//
// The docs don't describe the JSON event schema. Read-only inspection of
// the source (packages/cli/src/run/noninteractive.ts and run.ts in
// anomalyco/opencode) shows one JSON object per line with "type"
// (step_start, text, reasoning, tool_use, step_finish, error), "timestamp",
// "sessionID" and a "part" ("text" on text parts; "cost" and "tokens" on
// step_finish), errors as {"type":"error","error":{"message":...}}, and
// exit status 1 on failure. It is parsed defensively: unknown events are
// ignored, and when no JSON event parses the raw stdout is the answer.
//
// tinyagent never reads OpenCode's credential store (auth.json) or any
// token; the child inherits tinyagent's environment and signs itself in.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// OpenCodeProvider is the provider id of the opencode run handoff.
const OpenCodeProvider = "opencode"

var openCodeAuthMarkers = []string{"opencode auth login", "unauthorized", "401", "api key", "apikey", "authentication", "no providers"}

// openCodeReadOnlyPermission denies every permission that can change files,
// run commands, reach the network or spawn subagents.
const openCodeReadOnlyPermission = `{"edit":"deny","bash":"deny","webfetch":"deny","websearch":"deny","task":"deny","external_directory":"deny","question":"deny","doom_loop":"deny"}`

// openCodeReadOnlyConfig is the OPENCODE_CONFIG_CONTENT a read-only run
// sets: the restriction at the top level and on the plan agent itself,
// since agent permissions outrank top-level ones.
const openCodeReadOnlyConfig = `{"permission":` + openCodeReadOnlyPermission + `,"agent":{"plan":{"permission":` + openCodeReadOnlyPermission + `}}}`

// OpenCode runs `opencode run --format json` for each query.
type OpenCode struct {
	Path  string // resolved opencode binary
	Model string // "" or HandoffDefaultModel sends no --model
	HandoffOptions
}

// NewOpenCode resolves the opencode binary for model name.
func NewOpenCode(name string, opts HandoffOptions) (*OpenCode, error) {
	if opts.ReadOnly && os.Getenv("OPENCODE_CONFIG_CONTENT") != "" {
		return nil, fmt.Errorf("opencode: read-only mode sets OPENCODE_CONFIG_CONTENT to deny edits, commands and network tools, but it is already set in the environment; unset it or run without --read-only")
	}
	path, err := findCLI(OpenCodeProvider, "opencode", MissingCLIError{
		Install: "https://opencode.ai/docs/ (curl -fsSL https://opencode.ai/install | bash, or npm i -g opencode-ai)",
		SignIn:  "run `opencode auth login`",
		Docs:    "https://opencode.ai/docs/cli/",
	})
	if err != nil {
		return nil, err
	}
	return &OpenCode{Path: path, Model: name, HandoffOptions: opts}, nil
}

// Args returns the opencode arguments for one run with prompt as the
// message.
func (c *OpenCode) Args(prompt string) []string {
	args := []string{"run", "--format", "json"}
	if c.Dir != "" {
		args = append(args, "--dir", c.Dir)
	}
	if c.Model != "" && c.Model != HandoffDefaultModel {
		args = append(args, "--model", c.Model)
	}
	if c.ReadOnly {
		args = append(args, "--agent", "plan")
	}
	// A message starting with "-" would be read as a flag.
	if strings.HasPrefix(prompt, "-") {
		prompt = " " + prompt
	}
	return append(args, prompt)
}

// Env returns the extra environment for one run (nil when none).
func (c *OpenCode) Env() []string {
	if c.ReadOnly {
		return []string{"OPENCODE_CONFIG_CONTENT=" + openCodeReadOnlyConfig}
	}
	return nil
}

type openCodeEvent struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionID"`
	Error     *struct {
		Message string `json:"message"`
		Data    *struct {
			Message string `json:"message"`
		} `json:"data"`
	} `json:"error"`
	Part *struct {
		Type      string   `json:"type"`
		MessageID string   `json:"messageID"`
		Text      string   `json:"text"`
		Cost      *float64 `json:"cost"`
		Tokens    *struct {
			Input     int `json:"input"`
			Output    int `json:"output"`
			Reasoning int `json:"reasoning"`
			Cache     struct {
				Read  int `json:"read"`
				Write int `json:"write"`
			} `json:"cache"`
		} `json:"tokens"`
	} `json:"part"`
}

type openCodeRun struct {
	parsed    bool
	sessionID string
	answer    string
	failures  []string
	cost      Cost
}

func parseOpenCodeOutput(stdout []byte) openCodeRun {
	var r openCodeRun
	var lastMsg string
	var texts []string
	sc := bufio.NewScanner(bytes.NewReader(stdout))
	sc.Buffer(make([]byte, 0, 64*1024), 64*1024*1024)
	for sc.Scan() {
		var e openCodeEvent
		if json.Unmarshal(sc.Bytes(), &e) != nil || e.Type == "" {
			continue
		}
		r.parsed = true
		if e.SessionID != "" {
			r.sessionID = e.SessionID
		}
		switch e.Type {
		case "text":
			if e.Part == nil || strings.TrimSpace(e.Part.Text) == "" {
				continue
			}
			// The answer is the text of the last assistant message.
			if e.Part.MessageID != lastMsg {
				lastMsg, texts = e.Part.MessageID, nil
			}
			texts = append(texts, e.Part.Text)
		case "step_finish":
			if e.Part == nil {
				continue
			}
			if e.Part.Cost != nil {
				r.cost.USD += *e.Part.Cost
			}
			if t := e.Part.Tokens; t != nil {
				r.cost.InputTokens += t.Input + t.Cache.Read + t.Cache.Write
				r.cost.OutputTokens += t.Output + t.Reasoning
				r.cost.CacheReadTokens += t.Cache.Read
				r.cost.CacheWriteTokens += t.Cache.Write
			}
		case "error":
			msg := ""
			if e.Error != nil {
				msg = e.Error.Message
				if msg == "" && e.Error.Data != nil {
					msg = e.Error.Data.Message
				}
			}
			if msg == "" {
				msg = "error"
			}
			r.failures = append(r.failures, msg)
		}
	}
	r.answer = strings.TrimSpace(strings.Join(texts, "\n"))
	return r
}

func (c *OpenCode) Query(ctx context.Context, messages []Message, _ []ToolSpec) (Message, Cost, error) {
	res, err := runCLIEnv(ctx, OpenCodeProvider, c.Path, c.Args(handoffPrompt(messages)), c.Dir, "", c.Env())
	if err != nil {
		return Message{}, Cost{}, err
	}
	r := parseOpenCodeOutput(res.stdout)
	cost := r.cost
	if res.exitCode != 0 || len(r.failures) > 0 {
		detail := strings.Join(r.failures, "; ")
		if detail == "" {
			detail = tail(res.stderr, 1500)
		}
		if detail == "" {
			detail = tail(res.stdout, 1500)
		}
		msg := fmt.Sprintf("opencode: opencode exited %d: %s", res.exitCode, detail)
		if authFailure(detail+" "+string(res.stderr), openCodeAuthMarkers) {
			msg += "; sign in with `opencode auth login` (https://opencode.ai/docs/providers/)"
		}
		if r.sessionID != "" {
			msg += " (session " + r.sessionID + ")"
		}
		return Message{}, cost, fmt.Errorf("%s", msg)
	}
	text := r.answer
	if !r.parsed {
		text = strings.TrimSpace(string(res.stdout))
	}
	out := Message{Role: "assistant", Content: text}
	if r.sessionID != "" {
		out.Extra = map[string]any{"handoff_session_id": r.sessionID}
	}
	return out, cost, nil
}

// Version runs `opencode --version` with a short timeout and returns its
// first output line, or "" if it fails.
func (c *OpenCode) Version(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, c.Path, "--version")
	cmd.Dir = c.Dir
	setProcessGroup(cmd)
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return strings.TrimSpace(line)
}
