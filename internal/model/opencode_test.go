//go:build !windows

package model

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// newOpenCodeFake installs a fake opencode that also records
// OPENCODE_CONFIG_CONTENT, then builds the provider for name.
func newOpenCodeFake(t *testing.T, body, name string, opts HandoffOptions) (Model, string) {
	t.Helper()
	path, dir := fakeCLI(t, "opencode", `printf '%s' "$OPENCODE_CONFIG_CONTENT" > "$d/config"`+"\n"+body)
	SetCLIPaths("", "")
	SetOpenCodePath(path)
	m, err := NewWith(OpenCodeProvider+"/"+name, nil, Options{Handoff: opts})
	if err != nil {
		t.Fatal(err)
	}
	return m, dir
}

const openCodeEvents = `cat <<'J'
{"type":"step_start","timestamp":1,"sessionID":"ses_1","part":{"type":"step-start","messageID":"m1"}}
{"type":"text","timestamp":2,"sessionID":"ses_1","part":{"type":"text","messageID":"m1","text":"looking"}}
{"type":"tool_use","timestamp":3,"sessionID":"ses_1","part":{"type":"tool","messageID":"m1","tool":"read"}}
{"type":"step_finish","timestamp":4,"sessionID":"ses_1","part":{"type":"step-finish","messageID":"m1","cost":0.25,"tokens":{"input":10,"output":5,"reasoning":2,"cache":{"read":3,"write":1}}}}
not json
{"type":"text","timestamp":5,"sessionID":"ses_1","part":{"type":"text","messageID":"m2","text":"Fixed it."}}
{"type":"text","timestamp":6,"sessionID":"ses_1","part":{"type":"text","messageID":"m2","text":"All tests pass."}}
{"type":"step_finish","timestamp":7,"sessionID":"ses_1","part":{"type":"step-finish","messageID":"m2","cost":0.5,"tokens":{"input":20,"output":4,"reasoning":0,"cache":{"read":0,"write":0}}}}
J`

func TestOpenCodeSuccess(t *testing.T) {
	work := t.TempDir()
	m, dir := newOpenCodeFake(t, openCodeEvents, "anthropic/claude-sonnet-4-5", HandoffOptions{Dir: work})
	msg, cost, err := m.Query(context.Background(), userTask, nil)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Content != "Fixed it.\nAll tests pass." || len(msg.ToolCalls) != 0 || msg.Extra["handoff_session_id"] != "ses_1" {
		t.Fatalf("msg = %+v", msg)
	}
	if cost.USD != 0.75 || cost.InputTokens != 34 || cost.OutputTokens != 11 || cost.CacheReadTokens != 3 || cost.CacheWriteTokens != 1 {
		t.Fatalf("cost = %+v", cost)
	}
	want := "run\n--format\njson\n--dir\n" + work + "\n--model\nanthropic/claude-sonnet-4-5\nfix the bug\n"
	if got := readFile(t, filepath.Join(dir, "args")); got != want {
		t.Fatalf("args = %q, want %q", got, want)
	}
	if got := strings.TrimSpace(readFile(t, filepath.Join(dir, "cwd"))); got != work {
		if real, _ := filepath.EvalSymlinks(work); got != real {
			t.Fatalf("cwd = %q, want %q", got, work)
		}
	}
	if readFile(t, filepath.Join(dir, "stdin")) != "" || readFile(t, filepath.Join(dir, "config")) != "" {
		t.Fatal("stdin or OPENCODE_CONFIG_CONTENT set on a normal run")
	}
}

func TestOpenCodeDefaultModelAndDashPrompt(t *testing.T) {
	m, dir := newOpenCodeFake(t, `echo ok`, "default", HandoffOptions{})
	if _, _, err := m.Query(context.Background(), []Message{{Role: "user", Content: "--help me"}}, nil); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(dir, "args")); got != "run\n--format\njson\n --help me\n" {
		t.Fatalf("args = %q", got)
	}
}

func TestOpenCodeReadOnlyAndFallback(t *testing.T) {
	m, dir := newOpenCodeFake(t, `echo 'plain text answer'`, "default", HandoffOptions{ReadOnly: true})
	msg, _, err := m.Query(context.Background(), userTask, nil)
	if err != nil || msg.Content != "plain text answer" {
		t.Fatalf("msg = %+v, err = %v", msg, err)
	}
	if got := readFile(t, filepath.Join(dir, "args")); !strings.Contains(got, "--agent\nplan\n") {
		t.Fatalf("args = %q", got)
	}
	if got := readFile(t, filepath.Join(dir, "config")); got != openCodeReadOnlyConfig || !strings.Contains(got, `"edit":"deny"`) || !strings.Contains(got, `"bash":"deny"`) {
		t.Fatalf("config = %q", got)
	}
	t.Setenv("OPENCODE_CONFIG_CONTENT", `{"x":1}`)
	if _, err := NewWith("opencode/default", nil, Options{Handoff: HandoffOptions{ReadOnly: true}}); err == nil || !strings.Contains(err.Error(), "OPENCODE_CONFIG_CONTENT") {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenCodeErrors(t *testing.T) {
	m, _ := newOpenCodeFake(t, `echo '{"type":"error","timestamp":1,"sessionID":"ses_2","error":{"type":"unknown","message":"401 Unauthorized"}}'; exit 1`, "default", HandoffOptions{})
	_, _, err := m.Query(context.Background(), userTask, nil)
	if err == nil || !strings.Contains(err.Error(), "exited 1: 401 Unauthorized") || !strings.Contains(err.Error(), "opencode auth login") || !strings.Contains(err.Error(), "ses_2") {
		t.Fatalf("err = %v", err)
	}
	m, _ = newOpenCodeFake(t, `echo 'boom on stderr' >&2; exit 3`, "default", HandoffOptions{})
	_, _, err = m.Query(context.Background(), userTask, nil)
	if err == nil || !strings.Contains(err.Error(), "exited 3: boom on stderr") {
		t.Fatalf("err = %v", err)
	}
	// An error event with exit 0 still fails.
	m, _ = newOpenCodeFake(t, `echo '{"type":"error","error":{"data":{"message":"model not found"}}}'`, "default", HandoffOptions{})
	if _, _, err = m.Query(context.Background(), userTask, nil); err == nil || !strings.Contains(err.Error(), "model not found") {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenCodeMissingBinary(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	SetCLIPaths("", "")
	_, err := NewWith("opencode/anthropic/x", nil, Options{})
	var me *MissingCLIError
	if !errors.As(err, &me) || !strings.Contains(err.Error(), `"opencode" was not found on PATH`) || !strings.Contains(err.Error(), "https://opencode.ai/docs/") {
		t.Fatalf("err = %v", err)
	}
	if h := Hint("opencode/x", err.Error()); !strings.Contains(h, "install OpenCode") {
		t.Fatalf("hint = %q", h)
	}
	SetOpenCodePath(filepath.Join(t.TempDir(), "nope"))
	t.Cleanup(func() { SetCLIPaths("", "") })
	if _, err := NewWith("opencode/default", nil, Options{}); err == nil || !strings.Contains(err.Error(), "configured path") {
		t.Fatalf("err = %v", err)
	}
}

func TestOpenCodeCancelKillsProcessGroup(t *testing.T) {
	m, dir := newOpenCodeFake(t, `sleep 60 & echo $! > "$d/child"; wait`, "default", HandoffOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(300*time.Millisecond, cancel)
	start := time.Now()
	_, _, err := m.Query(ctx, userTask, nil)
	if !errors.Is(err, context.Canceled) {
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
			t.Fatalf("child %d still alive", pid)
		}
		time.Sleep(50 * time.Millisecond)
	}
	m, _ = newOpenCodeFake(t, `sleep 60`, "default", HandoffOptions{})
	tctx, tcancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer tcancel()
	if _, _, err := m.Query(tctx, userTask, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout err = %v", err)
	}
}

func TestOpenCodeVersion(t *testing.T) {
	m, _ := newOpenCodeFake(t, `[ "$1" = "--version" ] && echo 1.2.3`, "default", HandoffOptions{})
	if v := m.(*OpenCode).Version(context.Background()); v != "1.2.3" {
		t.Fatalf("version = %q", v)
	}
}
