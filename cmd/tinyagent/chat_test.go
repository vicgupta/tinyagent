package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vicgupta/tinyagent/internal/agent"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// chatRun is one finished runChatIO call.
type chatRun struct {
	code        int
	out, stderr string
}

// runChatTest runs a chat session in root reading input (cooked lines),
// with signals sent on sig if non-nil.
func runChatTest(t *testing.T, root string, input io.Reader, sig chan os.Signal, args ...string) chatRun {
	t.Helper()
	var o options
	fs := newFlagSet(&o)
	if err := fs.Parse(append([]string{"--interactive", "--cwd", root, "--color", "never"}, args...)); err != nil {
		t.Fatal(err)
	}
	if sig == nil {
		sig = make(chan os.Signal, 1)
	}
	var out, errw strings.Builder
	code := runChatIO(&o, fs, chatIO{reader: input, out: &out, errw: &errw, interrupts: sig})
	return chatRun{code, out.String(), errw.String()}
}

func lastUser(t *testing.T, body map[string]any) string {
	t.Helper()
	msgs := requestMessages(body)
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i]["role"] == "user" || msgs[i]["role"] == "tool" {
			s, _ := msgs[i]["content"].(string)
			return s
		}
	}
	return ""
}

func TestChatRefusals(t *testing.T) {
	t.Setenv("AGENT_MODEL", "")
	// Tests have no terminal.
	code := 0
	if got := captureStderr(t, func() { code, _, _ = runWith(t, "chat") }); code != 2 || !strings.Contains(got, "interactive terminal") {
		t.Errorf("chat without a tty: exit %d %q", code, got)
	}
	for _, args := range [][]string{{"-i", "--json"}, {"chat", "--acp"}, {"-i", "--attempts", "2"}, {"chat", "--review"}} {
		code, _, errOut := runWith(t, args...)
		if code != 2 || !strings.Contains(errOut, "--interactive can't be combined with") {
			t.Errorf("%v: exit %d %q", args, code, errOut)
		}
	}
	defer func(f func() bool) { chatTerminal = f }(chatTerminal)
	chatTerminal = func() bool { return true }
	if got := captureStderr(t, func() {
		if code, _, _ := runWith(t, "-i", "-t", "-"); code != 2 {
			t.Errorf("-t - exit %d", code)
		}
	}); !strings.Contains(got, "-t -") {
		t.Errorf("-t - message %q", got)
	}
	var o options
	fs := newFlagSet(&o)
	for _, name := range chatConflicts {
		if fs.Lookup(name) == nil {
			t.Errorf("chatConflicts names unknown flag %q", name)
		}
	}
}

func TestChatMultiTurn(t *testing.T) {
	root := acpWorkspace(t)
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha contents\n"), 0o644)
	p := newACPProvider(t,
		acpReply{text: "Let me look. ", tool: "read_file", args: `{"path":"a.txt"}`},
		acpReply{text: "It says alpha.\n" + agent.SubmitMarker},
		acpReply{text: "Still alpha.\n" + agent.SubmitMarker},
	)
	r := runChatTest(t, root, strings.NewReader("what is in a.txt?\nand \\\nnow?\n/cost\n /exit\n"), nil, "-m", "openai/gpt-4o")
	if r.code != 0 {
		t.Fatalf("exit %d\n%s", r.code, r.stderr)
	}
	if !strings.Contains(r.out, "It says alpha.") || !strings.Contains(r.out, "Still alpha.") || strings.Contains(r.out, agent.SubmitMarker) {
		t.Errorf("stdout = %q", r.out)
	}
	reqs := p.requests()
	if len(reqs) != 3 {
		t.Fatalf("%d requests", len(reqs))
	}
	first, second := requestMessages(reqs[1]), requestMessages(reqs[2])
	if len(second) != len(first)+2 || fmt.Sprint(second[:len(first)]) != fmt.Sprint(first) {
		t.Fatalf("history not continued:\n%v\n%v", first, second)
	}
	if last := lastUser(t, reqs[2]); !strings.HasPrefix(last, trajectory.FollowUpNotice) || !strings.HasSuffix(last, "and \nnow?") {
		t.Errorf("follow-up = %q", last)
	}
	for _, want := range []string{"turn 1 ·", "turn 2 ·", "2 turns ·", "continue with: tinyagent chat --resume"} {
		if !strings.Contains(r.stderr, want) {
			t.Errorf("stderr lacks %q:\n%s", want, r.stderr)
		}
	}
	// The checkpoint is under .tinyagent/chats and loads.
	files, _ := filepath.Glob(filepath.Join(root, ".tinyagent", "chats", "*.json"))
	if len(files) != 1 {
		t.Fatalf("checkpoints %v", files)
	}
	if tr, err := trajectory.Load(files[0]); err != nil || tr.ExitStatus != agent.StatusSubmitted {
		t.Errorf("checkpoint: %v %+v", err, tr)
	}
	// History: the directory is 0700, the file 0600; " /exit" is not recorded.
	h, err := loadChatHistory(filepath.Join(root, chatHistoryPath))
	if err != nil || fmt.Sprint(h.entries) != fmt.Sprint([]string{"what is in a.txt?", "and \nnow?", "/cost"}) {
		t.Errorf("history %q %v", h.entries, err)
	}
	if fi, err := os.Stat(filepath.Join(root, "history")); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("history dir: %v %v", fi.Mode(), err)
	}
	if fi, err := os.Stat(filepath.Join(root, chatHistoryPath)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("history file: %v %v", fi.Mode(), err)
	}
}

func TestChatNoHistoryAndFirstMessage(t *testing.T) {
	root := acpWorkspace(t)
	p := newACPProvider(t)
	out := filepath.Join(t.TempDir(), "c.json")
	r := runChatTest(t, root, strings.NewReader(""), nil, "-m", "openai/gpt-4o", "--no-chat-history", "-t", "hello there", "-o", out)
	if r.code != 0 || len(p.requests()) != 1 || !strings.Contains(lastUser(t, p.requests()[0]), "hello there") {
		t.Fatalf("exit %d, %d requests\n%s", r.code, len(p.requests()), r.stderr)
	}
	if _, err := os.Stat(filepath.Join(root, "history")); !os.IsNotExist(err) {
		t.Errorf("--no-chat-history wrote history: %v", err)
	}
	if _, err := os.Stat(out); err != nil {
		t.Errorf("-o checkpoint: %v", err)
	}
	// config chat_history: false does the same.
	cfg := writeConfig(t, "model: openai/gpt-4o\nchat_history: false\n")
	runChatTest(t, root, strings.NewReader("hi\n"), nil, "-c", cfg)
	if _, err := os.Stat(filepath.Join(root, "history")); !os.IsNotExist(err) {
		t.Errorf("chat_history: false wrote history: %v", err)
	}
}

func TestChatInterruptAndResume(t *testing.T) {
	if testing.Short() {
		t.Skip("runs a slow command")
	}
	root := acpWorkspace(t)
	p := newACPProvider(t)
	p.loop = &acpReply{tool: "bash", args: `{"command":"sleep 5"}`}
	sig := make(chan os.Signal, 1)
	go func() {
		for len(p.requests()) == 0 {
			time.Sleep(10 * time.Millisecond)
		}
		time.Sleep(200 * time.Millisecond)
		sig <- os.Interrupt
	}()
	out := filepath.Join(t.TempDir(), "c.json")
	start := time.Now()
	r := runChatTest(t, root, strings.NewReader("run it\n"), sig, "-m", "openai/gpt-4o", "-o", out)
	if r.code != 0 || time.Since(start) > 4*time.Second {
		t.Fatalf("exit %d after %s\n%s", r.code, time.Since(start), r.stderr)
	}
	if !strings.Contains(r.stderr, "interrupting the turn") || !strings.Contains(r.stderr, agent.StatusInterrupted) {
		t.Errorf("stderr:\n%s", r.stderr)
	}
	tr, err := trajectory.Load(out)
	if err != nil || tr.ExitStatus != agent.StatusInterrupted {
		t.Fatalf("checkpoint %v %v", err, tr)
	}
	// Resuming the unfinished checkpoint asks; yes re-runs it.
	p.mu.Lock()
	p.loop = nil
	p.mu.Unlock()
	n := len(p.requests())
	r = runChatTest(t, root, strings.NewReader("y\n"), nil, "--resume", out, "-o", out+"2")
	if r.code != 0 || !strings.Contains(r.stderr, "continue that run first? [Y/n]") || len(p.requests()) != n+1 {
		t.Fatalf("resume yes: exit %d, %d requests\n%s", r.code, len(p.requests())-n, r.stderr)
	}
	// A finished checkpoint continues with the next message as a follow-up.
	n = len(p.requests())
	r = runChatTest(t, root, strings.NewReader("more please\n"), nil, "--resume", out+"2", "-o", out+"3")
	reqs := p.requests()
	if r.code != 0 || len(reqs) != n+1 || !strings.HasSuffix(lastUser(t, reqs[n]), "more please") || !strings.Contains(fmt.Sprint(requestMessages(reqs[n])), "run it") {
		t.Fatalf("resume follow-up: exit %d\n%s", r.code, r.stderr)
	}
	// -o can't overwrite the checkpoint being resumed.
	if r := runChatTest(t, root, strings.NewReader(""), nil, "--resume", out, "-o", out); r.code != 2 {
		t.Errorf("same -o: exit %d", r.code)
	}
}

func TestChatNeedsInput(t *testing.T) {
	root := acpWorkspace(t)
	p := newACPProvider(t,
		acpReply{tool: "ask_user", args: `{"question":"Which color?","options":["red","blue"]}`},
		acpReply{text: "Blue it is.\n" + agent.SubmitMarker},
	)
	out := filepath.Join(t.TempDir(), "c.json")
	r := runChatTest(t, root, strings.NewReader("paint it\nblue\n"), nil, "-m", "openai/gpt-4o", "--ask-user", "-o", out)
	if r.code != 0 || !strings.Contains(r.stderr, "question: Which color?") || !strings.Contains(r.stderr, "options: red | blue") {
		t.Fatalf("exit %d\n%s", r.code, r.stderr)
	}
	reqs := p.requests()
	if len(reqs) != 2 || !strings.Contains(fmt.Sprint(requestMessages(reqs[1])), "blue") || !strings.Contains(r.out, "Blue it is.") {
		t.Fatalf("answer not delivered: %d requests\n%s", len(reqs), r.out)
	}
}

func TestChatSlashCommands(t *testing.T) {
	root := acpWorkspace(t)
	p := newACPProvider(t)
	in := "/help\n/cost\n/save\n/bogus\n/cmopact\n//etc/hosts?\n/cost\ntwo\nthree\nfour\n/compact\nagain\n/save " + filepath.Join(root, "s.json") + "\n/new\nfresh\n/quit\nnot read\n"
	r := runChatTest(t, root, strings.NewReader(in), nil, "-m", "openai/gpt-4o")
	if r.code != 0 {
		t.Fatalf("exit %d\n%s", r.code, r.stderr)
	}
	for _, want := range []string{"/exit, /quit", "no turns yet", "nothing to save yet", "unknown command /bogus", "did you mean /compact?", "1 turns ·", "compacted context", "compacts the conversation first", "saved " + filepath.Join(root, "s.json"), "new conversation"} {
		if !strings.Contains(r.stderr, want) {
			t.Errorf("stderr lacks %q", want)
		}
	}
	reqs := p.requests()
	if len(reqs) < 4 {
		t.Fatalf("%d requests\n%s", len(reqs), r.stderr)
	}
	if got := lastUser(t, reqs[0]); !strings.Contains(got, "/etc/hosts?") || strings.Contains(got, "//etc") {
		t.Errorf("// message = %q", got)
	}
	last := reqs[len(reqs)-1]
	if msgs := requestMessages(last); strings.Contains(fmt.Sprint(msgs), "/etc/hosts") || !strings.Contains(lastUser(t, last), "fresh") {
		t.Errorf("/new kept the old conversation: %v", msgs)
	}
	s, err := trajectory.Load(filepath.Join(root, "s.json"))
	if err != nil || len(s.Compactions) != 1 || s.Compactions[0].Reason != "requested" {
		t.Errorf("/compact: %v %+v", err, s.Compactions)
	}
	files, _ := filepath.Glob(filepath.Join(root, ".tinyagent", "chats", "*.json"))
	if len(files) != 2 {
		t.Errorf("/new checkpoints %v", files)
	}
}

// rawEditor is a lineEditor over input in raw mode, writing to out.
func rawEditor(input string, hist ...string) (*lineEditor, *strings.Builder) {
	var out strings.Builder
	e := newLineEditor(nil, strings.NewReader(input), &out, &chatHistory{entries: hist})
	e.raw = func() (func(), error) { return func() {}, nil }
	return e, &out
}

func TestLineEditor(t *testing.T) {
	for _, tc := range []struct {
		name, in, want string
		hist           []string
		err            error
	}{
		{"plain", "hello\r", "hello", nil, nil},
		{"left insert", "abc\x1b[D\x1b[DX\r", "aXbc", nil, nil},
		{"home end", "bc\x01a\x05d\r", "abcd", nil, nil},
		{"csi home end", "bc\x1b[Ha\x1b[Fd\x1b[1~0\x1b[4~e\r", "0abcde", nil, nil},
		{"ss3 home", "b\x1bOHa\r", "ab", nil, nil},
		{"backspace", "abx\x7fc\r", "abc", nil, nil},
		{"delete", "abxc\x1b[D\x1b[D\x1b[3~\r", "abc", nil, nil},
		{"ctrl-u", "junk\x15ok\r", "ok", nil, nil},
		{"ctrl-k", "okjunk\x01\x1b[C\x1b[C\x0b\r", "ok", nil, nil},
		{"ctrl-w", "foo bar  \x17\r", "foo ", nil, nil},
		{"right past end", "a\x1b[C\x1b[Cb\r", "ab", nil, nil},
		{"history up", "\x1b[A\x1b[A\r", "one", []string{"one", "two"}, nil},
		{"history down restores", "typed\x1b[A\x1b[B\r", "typed", []string{"one"}, nil},
		{"history top", "\x1b[A\x1b[A\x1b[A\x10\r", "one", []string{"one"}, nil},
		{"ctrl-c", "partial\x03", "partial", nil, errInterrupt},
		{"ctrl-d empty", "\x04", "", nil, io.EOF},
		{"ctrl-d deletes", "ab\x01\x04\r", "b", nil, nil},
		{"unicode", "héllo\x1b[D\x7f\r", "hélo", nil, nil},
		{"unknown escape", "a\x1b[Zb\x1b[99~c\r", "abc", nil, nil},
		{"eof with text", "abc", "abc", nil, nil},
	} {
		e, _ := rawEditor(tc.in, tc.hist...)
		got, err := e.ReadLine("> ")
		if got != tc.want || !errors.Is(err, tc.err) && err != tc.err {
			t.Errorf("%s: got %q, %v; want %q, %v", tc.name, got, err, tc.want, tc.err)
		}
	}
	// Redraw puts the cursor back from the end of the line.
	e, out := rawEditor("abc\x1b[D\x1b[D\r")
	e.ReadLine("> ")
	if !strings.Contains(out.String(), "\r> abc\x1b[K\x1b[2D") {
		t.Errorf("redraw %q", out.String())
	}
	// Cooked fallback: lines without editing, EOF at the end.
	c := newLineEditor(nil, strings.NewReader("one\r\ntwo"), io.Discard, nil)
	if l, err := c.ReadLine("> "); l != "one" || err != nil {
		t.Errorf("cooked 1: %q %v", l, err)
	}
	if l, err := c.ReadLine("> "); l != "two" || err != nil {
		t.Errorf("cooked 2: %q %v", l, err)
	}
	if _, err := c.ReadLine("> "); err != io.EOF {
		t.Errorf("cooked eof: %v", err)
	}
}

func TestChatHistoryFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "history", "chat_history")
	h, err := loadChatHistory(path)
	if err != nil || len(h.entries) != 0 {
		t.Fatal(h.entries, err)
	}
	for i := range chatHistoryMax + 5 {
		if err := h.add("msg " + strconv.Itoa(i)); err != nil {
			t.Fatal(err)
		}
	}
	h.add("msg " + strconv.Itoa(chatHistoryMax+4)) // repeat: skipped
	h.add(" secret")                               // leading space: skipped
	h.add("")
	h.add("two\nlines \"quoted\"")
	again, err := loadChatHistory(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.entries) != chatHistoryMax || again.entries[0] != "msg 6" || again.entries[chatHistoryMax-1] != "two\nlines \"quoted\"" || again.entries[chatHistoryMax-2] != "msg 1004" {
		t.Errorf("entries: %d, first %q, last %q", len(again.entries), again.entries[0], again.entries[len(again.entries)-1])
	}
	b, _ := os.ReadFile(path)
	if strings.Count(string(b), "\n") != chatHistoryMax || strings.Contains(string(b), "secret") {
		t.Errorf("file has %d lines", strings.Count(string(b), "\n"))
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("file mode %v", fi.Mode())
	}
	if fi, _ := os.Stat(filepath.Dir(path)); fi.Mode().Perm() != 0o700 {
		t.Errorf("dir mode %v", fi.Mode())
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".chat_history-*")); len(left) != 0 {
		t.Errorf("temp files left: %v", left)
	}
	// Memory-only history never touches the disk.
	m, _ := loadChatHistory("")
	if m.add("x") != nil || len(m.entries) != 1 {
		t.Errorf("memory history %v", m.entries)
	}
}
