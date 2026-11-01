package main

import (
	"strconv"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/termmd"
)

// fakeTerm is a minimal VT100 screen: printable runes (with termmd
// widths and deferred autowrap), CR, LF and the CSI A/B/C/D/K/J/m
// sequences the line editor emits. It counts the line feeds it gets.
type fakeTerm struct {
	w, row, col int
	pending     bool // cursor waits in the last column (deferred wrap)
	lines       [][]rune
	lineFeeds   int
}

func newFakeTerm(w int) *fakeTerm { return &fakeTerm{w: w, lines: [][]rune{nil}} }

func (t *fakeTerm) line(r int) []rune {
	for len(t.lines) <= r {
		t.lines = append(t.lines, nil)
	}
	for len(t.lines[r]) < t.w {
		t.lines[r] = append(t.lines[r], ' ')
	}
	return t.lines[r]
}

func (t *fakeTerm) Write(p []byte) (int, error) {
	s := []rune(string(p))
	for i := 0; i < len(s); i++ {
		r := s[i]
		switch {
		case r == '\r':
			t.col, t.pending = 0, false
		case r == '\n':
			t.lineFeeds++
			t.row++
			t.pending = false
			t.line(t.row)
		case r == 0x1b && i+1 < len(s) && s[i+1] == '[':
			j := i + 2
			for j < len(s) && (s[j] >= '0' && s[j] <= '9' || s[j] == ';') {
				j++
			}
			n, err := strconv.Atoi(string(s[i+2 : j]))
			if err != nil {
				n = 1
			}
			switch s[j] {
			case 'A':
				t.row = max(0, t.row-n)
			case 'B':
				t.row += n
			case 'C':
				t.col = min(t.w-1, t.col+n)
			case 'D':
				t.col = max(0, t.col-n)
			case 'K', 'J':
				l := t.line(t.row)
				for c := t.col; c < t.w; c++ {
					l[c] = ' '
				}
				if s[j] == 'J' && len(t.lines) > t.row+1 {
					t.lines = t.lines[:t.row+1]
				}
			}
			if s[j] != 'm' {
				t.pending = false
			}
			i = j
		default:
			if t.pending {
				t.row, t.col, t.pending = t.row+1, 0, false
			}
			l := t.line(t.row)
			l[t.col] = r
			for k := 1; k < termmd.RuneWidth(r) && t.col+k < t.w; k++ {
				l[t.col+k] = 0
			}
			t.col += termmd.RuneWidth(r)
			if t.col >= t.w {
				t.col, t.pending = t.w-1, true
			}
		}
	}
	return len(p), nil
}

// screen returns the visible rows, trailing spaces trimmed.
func (t *fakeTerm) screen() []string {
	var out []string
	for _, l := range t.lines {
		out = append(out, strings.TrimRight(strings.ReplaceAll(string(l), "\x00", ""), " "))
	}
	return out
}

func termEditor(input string, width int, hist ...string) (*lineEditor, *fakeTerm) {
	ft := newFakeTerm(width)
	e := newLineEditor(nil, strings.NewReader(input), ft, &chatHistory{entries: hist})
	e.raw = func() (func(), error) { return func() {}, nil }
	e.width = func() int { return width }
	return e, ft
}

const colorPrompt = "\n\x1b[1m\x1b[36m›\x1b[0m\x1b[1m \x1b[0m"

// TestLineEditorNoNewlinePerKey is the regression test for the chat prompt
// printing a new line on every keystroke (v0.15.0): the prompt's leading
// newline was rewritten by every redraw.
func TestLineEditorNoNewlinePerKey(t *testing.T) {
	for _, prompt := range []string{colorPrompt, "\n> ", "> "} {
		for _, text := range []string{"clone the github.com/vicgupta/tinyagent", "héllo 世界 🙂 ok"} {
			for _, width := range []int{0, 80} {
				// The input is read key by key, each key redrawing the
				// line: none of them may emit a line feed.
				e, out := rawEditor(text + "\r")
				e.width = func() int { return width }
				got, err := e.ReadLine(prompt)
				if err != nil || got != text {
					t.Fatalf("%q: got %q %v", prompt, got, err)
				}
				want := strings.Count(prompt, "\n") + 1 // prompt's own lines + Enter
				if n := strings.Count(out.String(), "\n"); n != want {
					t.Errorf("prompt %q text %q width %d: %d newlines, want %d\n%q", prompt, text, width, n, want, out.String())
				}
				// On a fake terminal the prompt line shows the text once.
				tw := width
				if tw == 0 {
					tw = 200
				}
				e, ft := termEditor(text+"\r", tw)
				e.width = func() int { return width }
				e.ReadLine(prompt)
				if ft.lineFeeds != want {
					t.Errorf("fake terminal got %d line feeds, want %d", ft.lineFeeds, want)
				}
				scr := ft.screen()
				last := scr[len(scr)-2]
				if strings.Contains(prompt, "\n") && scr[0] != "" {
					t.Errorf("blank line lost: %q", scr)
				}
				if !strings.HasSuffix(last, text) || strings.Count(strings.Join(scr, "\n"), text[:5]) != 1 {
					t.Errorf("prompt %q width %d: screen %q", prompt, width, scr)
				}
			}
		}
	}
}

func TestLineEditorRedrawScreen(t *testing.T) {
	for _, tc := range []struct {
		name, in, prompt string
		width            int
		hist             []string
		want             []string
	}{
		{"backspace", "abcx\x7f", "\n> ", 40, nil, []string{"", "> abc"}},
		{"arrows", "ac\x1b[Db", colorPrompt, 40, nil, []string{"", "› abc"}},
		{"history", "draft\x1b[A", colorPrompt, 40, []string{"old"}, []string{"", "› old"}},
		{"history down", "draft\x1b[A\x1b[B", colorPrompt, 40, []string{"a much longer old entry"}, []string{"", "› draft"}},
		{"paste", "pasted words at once", "> ", 40, nil, []string{"> pasted words at once"}},
		{"wrap", "0123456789abcdef", "> ", 10, nil, []string{"> 01234567", "89abcdef"}},
		{"wrap exact", "01234567", "> ", 10, nil, []string{"> 01234567", ""}},
		{"wrap backspace", "0123456789\x7f\x7f\x7f", "> ", 10, nil, []string{"> 0123456"}},
		{"wrap edit", "0123456789ab\x01X", "> ", 10, nil, []string{"> X0123456", "789ab"}},
		{"wide", "世界世界世", "› ", 8, nil, []string{"› 世界世", "界世"}},
	} {
		e, ft := termEditor(tc.in, tc.width, tc.hist...)
		e.ReadLine(tc.prompt)
		got := ft.screen()
		// Drop the row Enter-at-EOF moved to.
		got = got[:len(got)-1]
		if strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Errorf("%s: screen %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestLineEditorCursor checks the cursor lands where the edit is, after a
// wide rune and an escape-coded prompt.
func TestLineEditorCursor(t *testing.T) {
	e, ft := termEditor("世界ab\x1b[D\x1b[DX", 40)
	e.ReadLine("\x1b[36m›\x1b[0m ")
	if l := ft.screen()[0]; l != "› 世界Xab" {
		t.Errorf("screen %q", l)
	}
}
