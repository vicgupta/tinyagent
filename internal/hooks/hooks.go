// Package hooks runs user-configured commands before and after tool calls.
// Hooks are trusted local executables from the user's config; they are a
// guardrail and automation point, not a sandbox.
package hooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/tools"
)

const (
	SchemaVersion  = 1
	PreTool        = "pre_tool"
	PostTool       = "post_tool"
	defaultTimeout = 30
	maxTimeout     = 600
	maxCapture     = 64 << 10 // bytes kept from each of a hook's stdout and stderr
	MaxFeedback    = 8 << 10  // bytes of one post_tool hook's feedback
)

// Decisions reported for each hook run.
const (
	Allow    = "allow"
	Deny     = "deny"
	Feedback = "feedback"
	Error    = "error"
)

type hook struct {
	config.Hook
	match        *regexp.Regexp // nil matches every tool
	timeout      time.Duration
	allowOnError bool
}

// Runner holds the validated hooks for one run. A nil Runner runs nothing.
type Runner struct {
	pre, post []hook
}

// New validates cfg, read from the config file at path, and returns its
// hooks, or nil when none are configured.
func New(cfg config.Hooks, path string) (*Runner, error) {
	if len(cfg.PreTool) == 0 && len(cfg.PostTool) == 0 {
		return nil, nil
	}
	r := &Runner{}
	var err error
	if r.pre, err = compile(cfg.PreTool, PreTool, path); err != nil {
		return nil, err
	}
	if r.post, err = compile(cfg.PostTool, PostTool, path); err != nil {
		return nil, err
	}
	return r, nil
}

func compile(defs []config.Hook, event, path string) ([]hook, error) {
	out := make([]hook, 0, len(defs))
	for i, def := range defs {
		fail := func(format string, args ...any) error {
			return fmt.Errorf("config %s: hooks.%s[%d]: %s", path, event, i, fmt.Sprintf(format, args...))
		}
		h := hook{Hook: def, timeout: defaultTimeout * time.Second}
		if strings.TrimSpace(def.Command) == "" {
			return nil, fail("command is required")
		}
		if !filepath.IsAbs(def.Command) {
			return nil, fail("command %q must be an absolute path", def.Command)
		}
		if def.Match != "" {
			re, err := regexp.Compile("^(?:" + def.Match + ")$")
			if err != nil {
				return nil, fail("invalid match regexp: %v", err)
			}
			h.match = re
		}
		if def.TimeoutSec != nil {
			if *def.TimeoutSec < 1 || *def.TimeoutSec > maxTimeout {
				return nil, fail("timeout_seconds must be between 1 and %d, got %d", maxTimeout, *def.TimeoutSec)
			}
			h.timeout = time.Duration(*def.TimeoutSec) * time.Second
		}
		if def.OnError != "" && event != PreTool {
			return nil, fail("on_error applies only to pre_tool hooks")
		}
		switch def.OnError {
		case "", Deny:
		case Allow:
			h.allowOnError = true
		default:
			return nil, fail("on_error must be deny or allow, got %q", def.OnError)
		}
		seen := map[string]bool{}
		for _, key := range def.Env {
			if key == "" || strings.ContainsAny(key, "=\x00") || seen[key] {
				return nil, fail("invalid or duplicate forwarded environment variable name %q", key)
			}
			seen[key] = true
			if _, ok := os.LookupEnv(key); !ok {
				return nil, fail("forwarded environment variable %s is not set", key)
			}
		}
		out = append(out, h)
	}
	return out, nil
}

// Input describes the tool call a hook receives as JSON on stdin.
type Input struct {
	ToolName   string `json:"tool_name"`
	ToolCallID string `json:"tool_call_id"`
	Arguments  any    `json:"arguments"` // decoded JSON, or the raw string if malformed
	Root       string `json:"root"`
	ReadOnly   bool   `json:"read_only"`
	Step       int    `json:"step"`
}

type payload struct {
	SchemaVersion int    `json:"schema_version"`
	Event         string `json:"event"`
	Input
	Output  *string `json:"output,omitempty"`
	IsError *bool   `json:"is_error,omitempty"`
}

// Result reports one hook run.
type Result struct {
	Event    string
	Command  string
	Decision string // Allow, Deny, Feedback, or Error
	Duration time.Duration
	// Denied means the pre_tool call must not run.
	Denied bool
	// Reason is the deny reason, or the failure when Decision is Error.
	Reason string
	// Feedback is the marked text to append to the tool result.
	Feedback string
}

// Pre runs the pre_tool hooks matching in.ToolName in config order, stopping
// after the first that denies the call.
func (r *Runner) Pre(ctx context.Context, in Input) []Result {
	if r == nil {
		return nil
	}
	data, _ := json.Marshal(payload{SchemaVersion: SchemaVersion, Event: PreTool, Input: in})
	var results []Result
	for _, h := range r.pre {
		if !h.matches(in.ToolName) {
			continue
		}
		res := h.pre(ctx, in.Root, data)
		results = append(results, res)
		if res.Denied {
			break
		}
	}
	return results
}

// Post runs every post_tool hook matching in.ToolName in config order.
func (r *Runner) Post(ctx context.Context, in Input, output string, isError bool) []Result {
	if r == nil {
		return nil
	}
	data, _ := json.Marshal(payload{SchemaVersion: SchemaVersion, Event: PostTool, Input: in, Output: &output, IsError: &isError})
	var results []Result
	for _, h := range r.post {
		if h.matches(in.ToolName) {
			results = append(results, h.post(ctx, in.Root, data))
		}
	}
	return results
}

func (h hook) matches(name string) bool { return h.match == nil || h.match.MatchString(name) }

func (h hook) pre(ctx context.Context, root string, data []byte) Result {
	start := time.Now()
	res := Result{Event: PreTool, Command: h.Command, Decision: Allow}
	code, stdout, stderr, err := h.run(ctx, root, data)
	res.Duration = time.Since(start)
	if err == nil {
		switch code {
		case 0:
			var reply struct {
				Decision string `json:"decision"`
				Reason   string `json:"reason"`
			}
			if strings.TrimSpace(stdout) == "" {
				return res
			}
			if jerr := json.Unmarshal([]byte(stdout), &reply); jerr != nil {
				err = errors.New("printed invalid JSON on stdout")
			} else if reply.Decision == "" || reply.Decision == Allow {
				return res
			} else if reply.Decision == Deny {
				res.Decision, res.Denied, res.Reason = Deny, true, reason(reply.Reason)
				return res
			} else {
				err = fmt.Errorf("printed unknown decision %q", reply.Decision)
			}
		case 2:
			res.Decision, res.Denied, res.Reason = Deny, true, reason(stderr)
			return res
		default:
			err = exitError(code, stderr)
		}
	}
	res.Decision, res.Reason = Error, err.Error()
	res.Denied = !h.allowOnError || ctx.Err() != nil
	return res
}

func (h hook) post(ctx context.Context, root string, data []byte) Result {
	start := time.Now()
	res := Result{Event: PostTool, Command: h.Command, Decision: Allow}
	code, stdout, stderr, err := h.run(ctx, root, data)
	res.Duration = time.Since(start)
	var feedback string
	if err == nil {
		switch code {
		case 0:
			var reply struct {
				Feedback string `json:"feedback"`
			}
			if strings.TrimSpace(stdout) != "" {
				if jerr := json.Unmarshal([]byte(stdout), &reply); jerr != nil {
					err = errors.New("printed invalid JSON on stdout")
				}
			}
			feedback = reply.Feedback
		case 2:
			feedback = stderr
		default:
			err = exitError(code, stderr)
		}
	}
	if err != nil {
		res.Decision, res.Reason = Error, err.Error()
		return res
	}
	if feedback = strings.TrimSpace(feedback); feedback != "" {
		res.Decision = Feedback
		res.Feedback = "\n[post_tool hook " + h.Command + "]\n" + capFeedback(feedback)
	}
	return res
}

// run executes the hook directly (no shell) in root with data on stdin. err
// reports a failure to start, a timeout, a signal, or an interruption; any
// other exit is returned as code.
func (h hook) run(ctx context.Context, root string, data []byte) (code int, stdout, stderr string, err error) {
	hctx, cancel := context.WithTimeout(ctx, h.timeout)
	defer cancel()
	cmd := exec.CommandContext(hctx, h.Command, h.Args...)
	cmd.Dir = root
	cmd.Env = h.env()
	cmd.Stdin = bytes.NewReader(data)
	var out, errOut capped
	cmd.Stdout, cmd.Stderr = &out, &errOut
	cmd.WaitDelay = 2 * time.Second
	tools.SetProcessGroup(cmd)

	runErr := cmd.Run()
	switch {
	case ctx.Err() != nil:
		return 0, "", "", errors.New("interrupted")
	case errors.Is(hctx.Err(), context.DeadlineExceeded):
		return 0, "", "", fmt.Errorf("timed out after %s", h.timeout)
	}
	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		if exitErr.ExitCode() < 0 {
			return 0, "", "", fmt.Errorf("terminated: %v", runErr)
		}
		return exitErr.ExitCode(), out.String(), errOut.String(), nil
	} else if runErr != nil {
		return 0, "", "", fmt.Errorf("failed to run: %v", runErr)
	}
	return 0, out.String(), errOut.String(), nil
}

// env is the minimal environment also given to MCP servers: PATH, the
// Windows process variables when present, and the named variables.
func (h hook) env() []string {
	env := make([]string, 0, len(h.Env)+6)
	if p := os.Getenv("PATH"); p != "" {
		env = append(env, "PATH="+p)
	}
	if runtime.GOOS == "windows" {
		for _, k := range []string{"SystemRoot", "WINDIR", "USERPROFILE", "TEMP", "TMP"} {
			if v, ok := os.LookupEnv(k); ok {
				env = append(env, k+"="+v)
			}
		}
	}
	for _, k := range h.Env {
		env = append(env, k+"="+os.Getenv(k)) // presence checked by New
	}
	return env
}

func reason(s string) string {
	if s = strings.TrimSpace(s); s == "" {
		return "no reason given"
	}
	return s
}

func exitError(code int, stderr string) error {
	if s := strings.TrimSpace(stderr); s != "" {
		return fmt.Errorf("exited with status %d: %s", code, capText(s, 500))
	}
	return fmt.Errorf("exited with status %d", code)
}

func capFeedback(s string) string {
	if len(s) <= MaxFeedback {
		return s
	}
	return capText(s, MaxFeedback) + "\n[feedback truncated]"
}

// capText cuts s to at most n bytes on a rune boundary.
func capText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// capped keeps the first maxCapture bytes written and discards the rest.
type capped struct{ bytes.Buffer }

func (c *capped) Write(p []byte) (int, error) {
	if room := maxCapture - c.Len(); room > 0 {
		c.Buffer.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}
