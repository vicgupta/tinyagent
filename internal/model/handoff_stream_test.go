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

// flushedLine writes one stdout line from a short-lived child. The shell
// itself block-buffers a pipe, so a builtin echo would not reach the reader
// until the script exits.
func collect(b *strings.Builder) func(string) {
	return func(s string) { b.WriteString(s) }
}

func flushedLine(line string) string {
	return "sh -c 'cat' <<'END'\n" + line + "\nEND\n"
}

func openGate(t *testing.T, dir string) {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(dir, "gate"), os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

func makeGate(t *testing.T, dir string) {
	t.Helper()
	if err := syscall.Mkfifo(filepath.Join(dir, "gate"), 0o600); err != nil {
		t.Fatal(err)
	}
}

const claudeStreamResult = `{"type":"result","subtype":"success","is_error":false,"result":"all fixed","session_id":"s-1","total_cost_usd":0.5,"usage":{"input_tokens":10,"output_tokens":7,"cache_read_input_tokens":3,"cache_creation_input_tokens":2}}`

func TestHandoffStreamIncremental(t *testing.T) {
	cases := []struct {
		provider string
		body     string
		wantText string
		wantAct  string
	}{
		{
			provider: ClaudeCodeProvider,
			body: flushedLine(`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"all "}}}`) +
				flushedLine(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"go test"}}]}}`) +
				"read _ < \"$d/gate\" || exit 0\n" +
				flushedLine(`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"fixed"}}}`) +
				flushedLine(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"ok\n"}]}}`) +
				flushedLine(claudeStreamResult),
			wantText: "all fixed",
			wantAct:  "Bash go test\nok\n",
		},
		{
			provider: CodexExecProvider,
			body: flushedLine(`{"type":"thread.started","thread_id":"th-1"}`) +
				flushedLine(`{"type":"item.started","item":{"id":"item_1","type":"command_execution","command":"go test","aggregated_output":"","status":"in_progress"}}`) +
				"read _ < \"$d/gate\" || exit 0\n" +
				flushedLine(`{"type":"item.completed","item":{"id":"item_1","type":"command_execution","command":"go test","aggregated_output":"ok\n","exit_code":0,"status":"completed"}}`) +
				flushedLine(`{"type":"item.completed","item":{"id":"item_3","type":"agent_message","text":"all fixed"}}`) +
				flushedLine(`{"type":"turn.completed","usage":{"input_tokens":3,"cached_input_tokens":1,"output_tokens":2}}`),
			wantText: "all fixed",
			wantAct:  "$ go test\nok\n",
		},
		{
			provider: OpenCodeProvider,
			body: flushedLine(`{"type":"tool_use","sessionID":"ses_1","part":{"type":"tool","callID":"c1","tool":"bash","state":{"status":"completed","title":"go test","output":"ok\n"}}}`) +
				"read _ < \"$d/gate\" || exit 0\n" +
				flushedLine(`{"type":"part_delta","partID":"p3","delta":"all ","partType":"text","sessionID":"ses_1"}`) +
				flushedLine(`{"type":"part_delta","partID":"p3","delta":"fixed","partType":"text","sessionID":"ses_1"}`) +
				flushedLine(`{"type":"text","sessionID":"ses_1","part":{"type":"text","id":"p3","messageID":"m2","text":"all fixed"}}`) +
				flushedLine(`{"type":"step_finish","sessionID":"ses_1","part":{"type":"step-finish","cost":0.5,"tokens":{"input":10,"output":4,"reasoning":1,"cache":{"read":2,"write":3}}}}`),
			wantText: "all fixed",
			wantAct:  "go test\nok\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			var m Model
			var dir string
			if tc.provider == OpenCodeProvider {
				m, dir = newOpenCodeFake(t, tc.body, "default", HandoffOptions{})
			} else {
				m, dir = newFake(t, tc.provider, tc.body, "default", HandoffOptions{})
			}
			makeGate(t, dir)
			var text, act strings.Builder
			started := make(chan struct{}, 1)
			note := func() {
				select {
				case started <- struct{}{}:
				default:
				}
			}
			switch p := m.(type) {
			case *ClaudeCode:
				p.OnText = func(s string) { text.WriteString(s); note() }
			case *CodexExec:
				p.OnText = func(s string) { text.WriteString(s); note() }
			case *OpenCode:
				p.OnText = func(s string) { text.WriteString(s); note() }
			}
			ctx := WithActivity(context.Background(), func(_, _, chunk string) { act.WriteString(chunk); note() })
			done := make(chan struct{})
			var msg Message
			var err error
			go func() {
				msg, _, err = m.Query(ctx, userTask, nil)
				close(done)
			}()
			// The first flushed line is delivered while the script is blocked
			// on the fifo, so Query has not returned.
			select {
			case <-started:
			case <-done:
				t.Fatalf("query finished with no live output: %v", err)
			case <-time.After(5 * time.Second):
				t.Fatal("no live output while the CLI was blocked")
			}
			select {
			case <-done:
				t.Fatal("query finished before the gate opened")
			default:
			}
			openGate(t, dir)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("query did not finish after the gate opened")
			}
			if err != nil {
				t.Fatal(err)
			}
			if msg.Content != "all fixed" {
				t.Fatalf("content = %q", msg.Content)
			}
			if text.String() != tc.wantText {
				t.Fatalf("streamed = %q, want %q", text.String(), tc.wantText)
			}
			if act.String() != tc.wantAct {
				t.Fatalf("activity = %q, want %q", act.String(), tc.wantAct)
			}
		})
	}
}

func TestHandoffStreamMatchesBuffered(t *testing.T) {
	claude := flushedLine(`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"all fixed"}}}`) +
		flushedLine(`{"type":"assistant","message":{"content":[{"type":"text","text":"all fixed"}]}}`) +
		flushedLine(claudeStreamResult)
	codex := flushedLine(`{"type":"thread.started","thread_id":"th-1"}`) +
		flushedLine(`{"type":"item.completed","item":{"id":"item_3","type":"agent_message","text":"all fixed"}}`) +
		flushedLine(`{"type":"turn.completed","usage":{"input_tokens":24763,"cached_input_tokens":24448,"output_tokens":122}}`)
	opencode := flushedLine(`{"type":"text","sessionID":"ses_1","part":{"type":"text","messageID":"m2","text":"all fixed"}}`) +
		flushedLine(`{"type":"step_finish","sessionID":"ses_1","part":{"type":"step-finish","cost":0.25,"tokens":{"input":10,"output":5,"reasoning":2,"cache":{"read":3,"write":1}}}}`)
	for _, tc := range []struct {
		provider string
		body     string
		cost     Cost
		session  string
	}{
		{ClaudeCodeProvider, claude, Cost{InputTokens: 15, OutputTokens: 7, CacheReadTokens: 3, CacheWriteTokens: 2}, "s-1"},
		{CodexExecProvider, codex, Cost{InputTokens: 24763, CacheReadTokens: 24448, OutputTokens: 122}, "th-1"},
		{OpenCodeProvider, opencode, Cost{USD: 0.25, InputTokens: 14, OutputTokens: 7, CacheReadTokens: 3, CacheWriteTokens: 1}, "ses_1"},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			quiet, _ := handoffPair(t, tc.provider, tc.body, false)
			stream, streamed := handoffPair(t, tc.provider, tc.body, true)
			if stream.Content != quiet.Content || stream.Content != "all fixed" {
				t.Fatalf("stream %q quiet %q", stream.Content, quiet.Content)
			}
			if stream.Extra["handoff_session_id"] != tc.session || quiet.Extra["handoff_session_id"] != tc.session {
				t.Fatalf("session stream %v quiet %v", stream.Extra, quiet.Extra)
			}
			if *stream.cost != tc.cost || *quiet.cost != tc.cost {
				t.Fatalf("cost stream %+v quiet %+v want %+v", *stream.cost, *quiet.cost, tc.cost)
			}
			if streamed != "all fixed" {
				t.Fatalf("streamed text = %q (answer printed twice or not at all)", streamed)
			}
		})
	}
}

type handoffGot struct {
	Message
	cost *Cost
}

func handoffPair(t *testing.T, provider, body string, stream bool) (handoffGot, string) {
	t.Helper()
	var m Model
	if provider == OpenCodeProvider {
		m, _ = newOpenCodeFake(t, body, "sonnet", HandoffOptions{})
	} else {
		m, _ = newFake(t, provider, body, "sonnet", HandoffOptions{})
	}
	var text strings.Builder
	if stream {
		switch p := m.(type) {
		case *ClaudeCode:
			p.OnText = collect(&text)
		case *CodexExec:
			p.OnText = collect(&text)
		case *OpenCode:
			p.OnText = collect(&text)
		}
	}
	msg, cost, err := m.Query(context.Background(), userTask, nil)
	if err != nil {
		t.Fatal(err)
	}
	return handoffGot{Message: msg, cost: &cost}, text.String()
}

func TestHandoffNoStreamKeepsFlags(t *testing.T) {
	for _, provider := range []string{ClaudeCodeProvider, CodexExecProvider, OpenCodeProvider} {
		t.Run(provider, func(t *testing.T) {
			body := flushedLine(`{"type":"result","subtype":"success","result":"ok"}`)
			if provider == CodexExecProvider {
				body = flushedLine(`{"type":"item.completed","item":{"id":"i","type":"agent_message","text":"ok"}}`)
			}
			if provider == OpenCodeProvider {
				body = flushedLine(`{"type":"text","sessionID":"s","part":{"type":"text","messageID":"m","text":"ok"}}`)
			}
			var quiet, stream Model
			var qdir, sdir string
			if provider == OpenCodeProvider {
				quiet, qdir = newOpenCodeFake(t, body, "default", HandoffOptions{})
				stream, sdir = newOpenCodeFake(t, body, "default", HandoffOptions{})
				stream.(*OpenCode).OnText = func(string) {}
			} else {
				quiet, qdir = newFake(t, provider, body, "default", HandoffOptions{})
				stream, sdir = newFake(t, provider, body, "default", HandoffOptions{})
				switch p := stream.(type) {
				case *ClaudeCode:
					p.OnText = func(string) {}
				case *CodexExec:
					p.OnText = func(string) {}
				}
			}
			if _, _, err := quiet.Query(context.Background(), userTask, nil); err != nil {
				t.Fatal(err)
			}
			if _, _, err := stream.Query(context.Background(), userTask, nil); err != nil {
				t.Fatal(err)
			}
			qargs := readFile(t, filepath.Join(qdir, "args"))
			sargs := readFile(t, filepath.Join(sdir, "args"))
			switch provider {
			case ClaudeCodeProvider:
				if !strings.HasPrefix(qargs, "-p\n--output-format\njson\n") || strings.Contains(qargs, "stream-json") || strings.Contains(qargs, "include-partial") {
					t.Fatalf("no-stream args = %q", qargs)
				}
				if !strings.Contains(sargs, "--output-format\nstream-json\n--verbose\n--include-partial-messages\n") {
					t.Fatalf("stream args = %q", sargs)
				}
			default:
				if qargs != sargs {
					t.Fatalf("flags changed for streaming:\n quiet %q\n stream %q", qargs, sargs)
				}
			}
		})
	}
}

func TestClaudeStreamFlagFallback(t *testing.T) {
	body := `
n=$(cat "$d/n" 2>/dev/null || echo 0)
n=$((n+1))
echo "$n" > "$d/n"
cp "$d/args" "$d/args.$n"
if printf '%s\n' "$@" | grep -q include-partial-messages; then
  echo "error: unknown option '--include-partial-messages'" >&2
  exit 2
fi
if printf '%s\n' "$@" | grep -q stream-json; then
  echo "error: invalid value 'stream-json' for '--output-format'" >&2
  exit 2
fi
` + flushedLine(claudeStreamResult)
	m, dir := newFake(t, ClaudeCodeProvider, body, "default", HandoffOptions{})
	var text strings.Builder
	m.(*ClaudeCode).OnText = collect(&text)
	msg, cost, err := m.Query(context.Background(), userTask, nil)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Content != "all fixed" || cost.OutputTokens != 7 || text.String() != "all fixed" {
		t.Fatalf("msg %q cost %+v streamed %q", msg.Content, cost, text.String())
	}
	if n := strings.TrimSpace(readFile(t, filepath.Join(dir, "n"))); n != "3" {
		t.Fatalf("runs = %s, want 3 (partial, stream-json, json)", n)
	}
	final := readFile(t, filepath.Join(dir, "args.3"))
	if !strings.HasPrefix(final, "-p\n--output-format\njson\n") || strings.Contains(final, "stream-json") {
		t.Fatalf("fallback args = %q", final)
	}
}

func TestHandoffStreamCancelKillsChild(t *testing.T) {
	body := "sleep 60 &\necho $! > \"$d/child\"\n" +
		flushedLine(`{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"partial"}}}`) +
		"wait\n"
	m, dir := newFake(t, ClaudeCodeProvider, body, "default", HandoffOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.(*ClaudeCode).OnText = func(s string) {
		if s != "" {
			cancel()
		}
	}
	start := time.Now()
	_, _, err := m.Query(ctx, userTask, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("cancel did not return promptly")
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
