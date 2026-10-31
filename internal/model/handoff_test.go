//go:build !windows

package model

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeCLI writes an executable shell script into a temp dir that records
// its arguments and stdin next to itself, then runs body.
func fakeCLI(t *testing.T, name, body string) (path, dir string) {
	t.Helper()
	dir = t.TempDir()
	path = filepath.Join(dir, name)
	script := "#!/bin/sh\nd=$(dirname \"$0\")\nprintf '%s\\n' \"$@\" > \"$d/args\"\ncat > \"$d/stdin\"\npwd > \"$d/cwd\"\n" + body + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { SetCLIPaths("", "") })
	return path, dir
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func newFake(t *testing.T, provider, body, name string, opts HandoffOptions) (Model, string) {
	t.Helper()
	bin := map[string]string{ClaudeCodeProvider: "claude", CodexExecProvider: "codex"}[provider]
	path, dir := fakeCLI(t, bin, body)
	if provider == ClaudeCodeProvider {
		SetCLIPaths(path, "")
	} else {
		SetCLIPaths("", path)
	}
	m, err := NewWith(provider+"/"+name, nil, Options{Handoff: opts})
	if err != nil {
		t.Fatal(err)
	}
	return m, dir
}

var userTask = []Message{{Role: "system", Content: "tinyagent system prompt"}, {Role: "user", Content: "fix the bug"}}

func TestClaudeCodeSuccess(t *testing.T) {
	work := t.TempDir()
	m, dir := newFake(t, ClaudeCodeProvider, `echo '{"type":"result","subtype":"success","is_error":false,"result":"done","session_id":"s-1","total_cost_usd":0.5,"usage":{"input_tokens":10,"output_tokens":7,"cache_read_input_tokens":3,"cache_creation_input_tokens":2}}'`, "sonnet", HandoffOptions{Dir: work})
	msg, cost, err := m.Query(context.Background(), userTask, nil)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Content != "done" || msg.Extra["handoff_session_id"] != "s-1" || len(msg.ToolCalls) != 0 {
		t.Fatalf("msg = %+v", msg)
	}
	if cost.USD != 0 || cost.InputTokens != 15 || cost.OutputTokens != 7 || cost.CacheReadTokens != 3 || cost.CacheWriteTokens != 2 {
		t.Fatalf("cost = %+v", cost)
	}
	args := readFile(t, filepath.Join(dir, "args"))
	if !strings.HasPrefix(args, "-p\n--output-format\njson\n--model\nsonnet\n") || strings.Contains(args, "--tools") || strings.Contains(args, "--bare") {
		t.Fatalf("args = %q", args)
	}
	if got := readFile(t, filepath.Join(dir, "stdin")); got != "fix the bug" {
		t.Fatalf("stdin = %q", got)
	}
	if got, _ := filepath.EvalSymlinks(strings.TrimSpace(readFile(t, filepath.Join(dir, "cwd")))); got != mustEval(t, work) {
		t.Fatalf("cwd = %q, want %q", got, work)
	}
}

func mustEval(t *testing.T, p string) string {
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestClaudeCodeReadOnlyDefaultModelLongPrompt(t *testing.T) {
	m, dir := newFake(t, ClaudeCodeProvider, `echo '{"type":"result","subtype":"success","result":"ok"}'`, "default", HandoffOptions{ReadOnly: true})
	long := strings.Repeat("x", 2<<20)
	if _, _, err := m.Query(context.Background(), []Message{{Role: "user", Content: long}}, nil); err != nil {
		t.Fatal(err)
	}
	args := readFile(t, filepath.Join(dir, "args"))
	if strings.Contains(args, "--model") || !strings.Contains(args, "--tools\nRead,Glob,Grep\n--permission-mode\ndontAsk\n") {
		t.Fatalf("args = %q", args)
	}
	if got := readFile(t, filepath.Join(dir, "stdin")); got != long {
		t.Fatalf("stdin has %d bytes, want %d", len(got), len(long))
	}
}

func TestClaudeCodeErrorOutput(t *testing.T) {
	m, _ := newFake(t, ClaudeCodeProvider, `echo '{"type":"result","subtype":"success","is_error":true,"result":"Not logged in · Please run /login","session_id":"s-2"}'; exit 1`, "default", HandoffOptions{})
	_, _, err := m.Query(context.Background(), userTask, nil)
	if err == nil || !strings.Contains(err.Error(), "Not logged in") || !strings.Contains(err.Error(), "/login") || !strings.Contains(err.Error(), "s-2") {
		t.Fatalf("err = %v", err)
	}
	if h := Hint("claude-code/default", err.Error()); !strings.Contains(h, "/login") {
		t.Fatalf("hint = %q", h)
	}
}

func TestClaudeCodeNonZeroExitAndMalformed(t *testing.T) {
	m, _ := newFake(t, ClaudeCodeProvider, `echo 'boom' >&2; exit 3`, "default", HandoffOptions{})
	if _, _, err := m.Query(context.Background(), userTask, nil); err == nil || !strings.Contains(err.Error(), "exited 3") || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v", err)
	}
	m, _ = newFake(t, ClaudeCodeProvider, `echo 'plain answer, not json'`, "default", HandoffOptions{})
	msg, _, err := m.Query(context.Background(), userTask, nil)
	if err != nil || msg.Content != "plain answer, not json" {
		t.Fatalf("msg = %+v, err = %v", msg, err)
	}
}

func TestCodexExecSuccess(t *testing.T) {
	work := t.TempDir()
	m, dir := newFake(t, CodexExecProvider, `cat <<'J'
{"type":"thread.started","thread_id":"th-1"}
{"type":"turn.started"}
{"type":"item.completed","item":{"id":"item_1","type":"reasoning","text":"thinking"}}
{"type":"item.completed","item":{"id":"item_3","type":"agent_message","text":"all fixed"}}
{"type":"turn.completed","usage":{"input_tokens":24763,"cached_input_tokens":24448,"output_tokens":122}}
J`, "gpt-5.5", HandoffOptions{Dir: work})
	msg, cost, err := m.Query(context.Background(), userTask, nil)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Content != "all fixed" || msg.Extra["handoff_session_id"] != "th-1" {
		t.Fatalf("msg = %+v", msg)
	}
	if cost.USD != 0 || cost.InputTokens != 24763 || cost.CacheReadTokens != 24448 || cost.OutputTokens != 122 {
		t.Fatalf("cost = %+v", cost)
	}
	want := "exec\n--json\n--sandbox\nworkspace-write\n--cd\n" + work + "\n--model\ngpt-5.5\n-\n"
	if args := readFile(t, filepath.Join(dir, "args")); args != want {
		t.Fatalf("args = %q, want %q", args, want)
	}
	if got := readFile(t, filepath.Join(dir, "stdin")); got != "fix the bug" {
		t.Fatalf("stdin = %q", got)
	}
}

func TestCodexExecReadOnlyAndFallback(t *testing.T) {
	m, dir := newFake(t, CodexExecProvider, `echo 'final message only'`, "default", HandoffOptions{ReadOnly: true})
	msg, _, err := m.Query(context.Background(), userTask, nil)
	if err != nil || msg.Content != "final message only" {
		t.Fatalf("msg = %+v, err = %v", msg, err)
	}
	if args := readFile(t, filepath.Join(dir, "args")); args != "exec\n--json\n--sandbox\nread-only\n-\n" {
		t.Fatalf("args = %q", args)
	}
}

func TestCodexExecErrors(t *testing.T) {
	m, _ := newFake(t, CodexExecProvider, `echo '{"type":"thread.started","thread_id":"th-2"}'; echo '{"type":"turn.failed","error":{"message":"401 Unauthorized"}}'; exit 1`, "default", HandoffOptions{})
	_, _, err := m.Query(context.Background(), userTask, nil)
	if err == nil || !strings.Contains(err.Error(), "401 Unauthorized") || !strings.Contains(err.Error(), "codex login") || !strings.Contains(err.Error(), "th-2") {
		t.Fatalf("err = %v", err)
	}
	m, _ = newFake(t, CodexExecProvider, `echo 'Not inside a trusted directory' >&2; exit 1`, "default", HandoffOptions{})
	if _, _, err := m.Query(context.Background(), userTask, nil); err == nil || !strings.Contains(err.Error(), "exited 1: Not inside a trusted directory") {
		t.Fatalf("err = %v", err)
	}
}

func TestHandoffMissingBinary(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	SetCLIPaths("", "")
	for _, spec := range []string{"claude-code/default", "codex-exec/default"} {
		_, err := NewWith(spec, nil, Options{})
		var me *MissingCLIError
		if !errors.As(err, &me) || !strings.Contains(err.Error(), "not found on PATH") || !strings.Contains(err.Error(), "https://") {
			t.Fatalf("%s: err = %v", spec, err)
		}
		if Hint(spec, err.Error()) == "" {
			t.Fatalf("%s: no hint", spec)
		}
	}
	SetCLIPaths(filepath.Join(t.TempDir(), "nope"), "")
	t.Cleanup(func() { SetCLIPaths("", "") })
	if _, err := NewWith("claude-code/default", nil, Options{}); err == nil || !strings.Contains(err.Error(), "configured path") {
		t.Fatalf("err = %v", err)
	}
}

func TestHandoffTimeoutKillsProcessGroup(t *testing.T) {
	m, dir := newFake(t, ClaudeCodeProvider, `sleep 60 & echo $! > "$d/child"; wait`, "default", HandoffOptions{})
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, _, err := m.Query(ctx, userTask, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	if d := time.Since(start); d > 10*time.Second {
		t.Fatalf("took %v", d)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(readFile(t, filepath.Join(dir, "child"))))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for syscall.Kill(pid, 0) == nil {
		if time.Now().After(deadline) {
			t.Fatalf("child %d still running", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestHandoffPromptTranscript(t *testing.T) {
	got := handoffPrompt([]Message{{Role: "system", Content: "sys"}, {Role: "user", Content: "a"}, {Role: "assistant", Content: "b"}, {Role: "user", Content: "c"}})
	if strings.Contains(got, "sys") || !strings.Contains(got, "### User\na") || !strings.Contains(got, "### Assistant\nb") || !strings.HasSuffix(got, "### User\nc\n") {
		t.Fatalf("prompt = %q", got)
	}
}
