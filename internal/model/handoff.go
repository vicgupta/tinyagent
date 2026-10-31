package model

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Handoff providers (claude-code, codex-exec, opencode) run a coding-agent CLI the
// user installed and signed in to themselves, hand it the task, and return
// its final answer as one assistant message with no tool calls. They never
// read, copy, store, refresh or proxy the CLI's credentials and never set
// auth environment variables: the child inherits tinyagent's environment
// unchanged and the CLI authenticates itself.

// HandoffOptions are the TinyAgent settings a handoff provider maps onto
// the CLI it runs.
type HandoffOptions struct {
	// Dir is the working directory the CLI runs in (--cwd). "" keeps
	// tinyagent's own working directory.
	Dir string
	// ReadOnly maps --read-only/read_only onto the CLI's own restriction.
	ReadOnly bool
}

// cliPaths are optional configured binary paths, by provider id.
var cliPaths = map[string]string{}

// SetCLIPaths sets the configured binary paths of the claude-code and
// codex-exec providers; "" means look the binary up on PATH.
func SetCLIPaths(claude, codex string) {
	cliPaths = map[string]string{ClaudeCodeProvider: claude, CodexExecProvider: codex}
}

// SetOpenCodePath sets the configured binary path of the opencode
// provider; "" means look it up on PATH. Call it after SetCLIPaths.
func SetOpenCodePath(p string) {
	cliPaths[OpenCodeProvider] = p
}

// handoffWaitDelay bounds how long Wait waits for the CLI's output pipes
// after the process exits or is killed, in case a grandchild holds them.
var handoffWaitDelay = 5 * time.Second

// MissingCLIError is returned when a handoff provider's CLI isn't found.
type MissingCLIError struct {
	Provider, Binary, Configured, Install, SignIn, Docs string
}

func (e *MissingCLIError) Error() string {
	where := fmt.Sprintf("%q was not found on PATH", e.Binary)
	if e.Configured != "" {
		where = fmt.Sprintf("configured path %q is not an executable", e.Configured)
	}
	return fmt.Sprintf("%s: %s; install it (%s), sign in (%s), then rerun. Docs: %s", e.Provider, where, e.Install, e.SignIn, e.Docs)
}

// findCLI resolves a handoff CLI: the configured path if set, else PATH.
func findCLI(provider, binary string, missing MissingCLIError) (string, error) {
	missing.Provider, missing.Binary = provider, binary
	if p := cliPaths[provider]; p != "" {
		if path, err := exec.LookPath(p); err == nil {
			return path, nil
		}
		missing.Configured = p
		return "", &missing
	}
	path, err := exec.LookPath(binary)
	if err != nil {
		return "", &missing
	}
	return path, nil
}

// cliResult is one finished CLI run.
type cliResult struct {
	stdout, stderr []byte
	exitCode       int
}

// runCLI runs path with args in dir, feeding stdin, without a shell. A
// cancelled or expired ctx kills the CLI's whole process group and returns
// ctx's error.
func runCLI(ctx context.Context, provider, path string, args []string, dir, stdin string) (cliResult, error) {
	return runCLIEnv(ctx, provider, path, args, dir, stdin, nil)
}

// runCLIEnv is runCLI with extra environment entries added to tinyagent's
// own environment (nil inherits it unchanged).
func runCLIEnv(ctx context.Context, provider, path string, args []string, dir, stdin string, env []string) (cliResult, error) {
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = dir
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	setProcessGroup(cmd)
	cmd.WaitDelay = handoffWaitDelay
	err := cmd.Run()
	res := cliResult{stdout: out.Bytes(), stderr: errb.Bytes()}
	if ctx.Err() != nil {
		return res, fmt.Errorf("%s: stopped: %w", provider, ctx.Err())
	}
	var ee *exec.ExitError
	switch {
	case errors.As(err, &ee):
		res.exitCode = ee.ExitCode()
	case err != nil:
		return res, fmt.Errorf("%s: run %s: %w", provider, path, err)
	}
	return res, nil
}

// handoffPrompt renders the conversation as one prompt for a CLI agent.
// TinyAgent's system prompt describes TinyAgent's own tools, which the CLI
// doesn't have, so system messages are left out; the CLI brings its own.
// A single user message is sent as is; a longer conversation is sent as a
// labeled transcript.
func handoffPrompt(messages []Message) string {
	var turns []Message
	for _, m := range messages {
		if m.Role != "system" && strings.TrimSpace(m.Content) != "" {
			turns = append(turns, m)
		}
	}
	if len(turns) == 1 && turns[0].Role == "user" {
		return turns[0].Content
	}
	var b strings.Builder
	b.WriteString("Continue this conversation. Earlier turns are below, oldest first; answer the last user turn.\n")
	for _, m := range turns {
		label := map[string]string{"user": "User", "assistant": "Assistant", "tool": "Tool result"}[m.Role]
		if label == "" {
			label = m.Role
		}
		fmt.Fprintf(&b, "\n### %s\n%s\n", label, m.Content)
	}
	return b.String()
}

// tail returns the last n bytes of s, trimmed, for error messages.
func tail(s []byte, n int) string {
	t := strings.TrimSpace(string(s))
	if len(t) > n {
		t = "…" + t[len(t)-n:]
	}
	return t
}

// authFailure reports whether CLI output looks like a sign-in problem.
func authFailure(text string, markers []string) bool {
	l := strings.ToLower(text)
	for _, m := range markers {
		if strings.Contains(l, m) {
			return true
		}
	}
	return false
}
