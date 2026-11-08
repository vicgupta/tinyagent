package main

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestLineEditorBracketedPaste(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
	}{
		{"multiline crlf", "\x1b[200~hello\r\nworld\x1b[201~\r", "hello\nworld"},
		{"multiline lf", "\x1b[200~hello\nworld\n!\x1b[201~\r", "hello\nworld\n!"},
		{"cr only", "\x1b[200~a\rb\x1b[201~\r", "a\nb"},
		{"then type", "\x1b[200~foo\nbar\x1b[201~!\r", "foo\nbar!"},
		{"empty paste", "\x1b[200~\x1b[201~ok\r", "ok"},
		{"tab kept", "\x1b[200~a\tb\x1b[201~\r", "a\tb"},
		{"controls dropped", "\x1b[200~a\x00b\x01c\x1b[201~\r", "abc"},
	} {
		e, _ := rawEditor(tc.in)
		got, err := e.ReadLine("> ")
		if err != nil || got != tc.want {
			t.Errorf("%s: got %q %v, want %q", tc.name, got, err, tc.want)
		}
	}
	// Without bracketed paste, a newline still submits the first line.
	e, _ := rawEditor("one\ntwo\r")
	got, err := e.ReadLine("> ")
	if err != nil || got != "one" {
		t.Errorf("unbracketed newline: got %q %v", got, err)
	}
}

func TestLineEditorLargePaste(t *testing.T) {
	payload := strings.Repeat("x", 1<<20)
	e, _ := rawEditor("\x1b[200~" + payload + "\nsecond line\x1b[201~\r")
	got, err := e.ReadLine("> ")
	if err != nil {
		t.Fatal(err)
	}
	want := payload + "\nsecond line"
	if got != want {
		t.Fatalf("got %d bytes %q…%q, want %d", len(got), prefix(got, 20), suffix(got, 20), len(want))
	}
}

func TestLineEditorPasteCap(t *testing.T) {
	e, _ := rawEditor("\x1b[200~" + strings.Repeat("a", 100) + "\x1b[201~\r")
	e.maxInput = 32
	got, err := e.ReadLine("> ")
	if err != nil || got != strings.Repeat("a", 32) {
		t.Fatalf("got %q %v", got, err)
	}
	if !e.takeTruncated() {
		t.Fatal("expected truncated flag")
	}
	if e.takeTruncated() {
		t.Fatal("truncated flag should clear")
	}
}

func TestLineEditorCookedLargeLine(t *testing.T) {
	payload := strings.Repeat("y", 1<<20)
	c := newLineEditor(nil, strings.NewReader(payload+"\nnext\n"), io.Discard, nil)
	got, err := c.ReadLine("> ")
	if err != nil || got != payload {
		t.Fatalf("cooked 1 MiB: len=%d err=%v", len(got), err)
	}
	if got, err = c.ReadLine("> "); err != nil || got != "next" {
		t.Fatalf("cooked next: %q %v", got, err)
	}
}

func TestLineEditorCookedCap(t *testing.T) {
	c := newLineEditor(nil, strings.NewReader(strings.Repeat("z", 50)+"\n"), io.Discard, nil)
	c.maxInput = 10
	got, err := c.ReadLine("> ")
	if err != nil || got != strings.Repeat("z", 10) {
		t.Fatalf("got %q %v", got, err)
	}
	if !c.takeTruncated() {
		t.Fatal("expected truncated flag")
	}
}

func TestLineEditorPasteInterrupt(t *testing.T) {
	// Incomplete paste at EOF still returns what arrived.
	e, _ := rawEditor("\x1b[200~partial")
	got, err := e.ReadLine("> ")
	if got != "partial" || err != nil {
		t.Fatalf("eof during paste: %q %v", got, err)
	}
}

func TestChatHistoryLargeEntry(t *testing.T) {
	path := t.TempDir() + "/history/chat_history"
	h, err := loadChatHistory(path)
	if err != nil {
		t.Fatal(err)
	}
	msg := strings.Repeat("h", 1<<20) + "\nmulti\nline"
	if err := h.add(msg); err != nil {
		t.Fatal(err)
	}
	again, err := loadChatHistory(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.entries) != 1 || again.entries[0] != msg {
		t.Fatalf("round-trip: %d entries, len %d", len(again.entries), len(strings.Join(again.entries, "")))
	}
}

func TestLineEditorPasteThenCtrlC(t *testing.T) {
	e, _ := rawEditor("\x1b[200~keep\nme\x1b[201~\x03")
	got, err := e.ReadLine("> ")
	if got != "keep\nme" || !errors.Is(err, errInterrupt) {
		t.Fatalf("got %q %v", got, err)
	}
}

func prefix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func suffix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
