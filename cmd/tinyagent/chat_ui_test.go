package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/agent"
)

func TestChatBannerAndExitSummary(t *testing.T) {
	root := acpWorkspace(t)
	newACPProvider(t, acpReply{text: "ok\n" + agent.SubmitMarker})
	r := runChatTest(t, root, strings.NewReader("hi\n"), nil, "-m", "openai/gpt-4o")
	if r.code != 0 {
		t.Fatalf("exit %d\n%s", r.code, r.stderr)
	}
	for _, want := range []string{"tinyagent dev · interactive chat\n", "  model  openai/gpt-4o\n", "  cwd    ", "  saves  .tinyagent/chats/", "/help lists commands", "> ",
		"chat ended · 1 turn · 1 step · ", "\n  saved  .tinyagent/chats/", "\n  resume tinyagent chat --resume "} {
		if !strings.Contains(r.stderr+r.out, want) {
			t.Errorf("output lacks %q:\n%s", want, r.stderr)
		}
	}
	if strings.Contains(r.stderr+r.out, "\x1b") {
		t.Errorf("escape codes with --color never: %q", r.stderr)
	}
}

func TestChatToolLineAndMarkdown(t *testing.T) {
	root := acpWorkspace(t)
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha\n"), 0o644)
	newACPProvider(t,
		acpReply{text: "Let me look.", tool: "read_file", args: `{"path":"a.txt"}`},
		acpReply{text: "It says **alpha**.\n\n- one\n- two\n" + agent.SubmitMarker},
	)
	r := runChatTest(t, root, strings.NewReader("what?\n"), nil, "-m", "openai/gpt-4o")
	if !strings.Contains(r.stderr, "  # read_file a.txt\n") {
		t.Errorf("no tool line:\n%s", r.stderr)
	}
	// Narration before the tool call ends its line instead of running into the reply.
	if !strings.Contains(r.out, "Let me look.\n") || !strings.Contains(r.out, "It says alpha.\n") || !strings.Contains(r.out, "- one\n- two\n") {
		t.Errorf("stdout = %q", r.out)
	}
	if strings.Contains(r.out, "**") {
		t.Errorf("markdown not rendered: %q", r.out)
	}
}

func TestChatRawToggle(t *testing.T) {
	root := acpWorkspace(t)
	newACPProvider(t, acpReply{text: "It says **alpha**.\n" + agent.SubmitMarker})
	r := runChatTest(t, root, strings.NewReader("/raw\nwhat?\n"), nil, "-m", "openai/gpt-4o")
	if !strings.Contains(r.out, "It says **alpha**.") {
		t.Errorf("/raw did not show raw Markdown: %q", r.out)
	}
	if !strings.Contains(r.stderr, "raw Markdown") {
		t.Errorf("no /raw notice:\n%s", r.stderr)
	}
}

func TestToolSummary(t *testing.T) {
	var b strings.Builder
	a := newChatActivity(&b, styler{})
	a.toolCall("read_file", map[string]any{"path": "a.txt"})
	a.toolResult(false)
	a.toolCall("read_file", map[string]any{"path": "b.txt"})
	a.toolResult(true)
	a.toolCall("bash", map[string]any{"command": "ls"})
	a.toolResult(false)
	got := a.summary()
	for _, want := range []string{"3 tool calls", "read_file ×2", "bash", "1 failed"} {
		if !strings.Contains(got, want) {
			t.Errorf("summary %q lacks %q", got, want)
		}
	}
}
