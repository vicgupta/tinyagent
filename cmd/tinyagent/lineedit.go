package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/vicgupta/tinyagent/internal/termmd"
)

// errInterrupt is returned by ReadLine for Ctrl-C, with the text typed.
var errInterrupt = errors.New("interrupted")

// lineEditor reads chat input. On a terminal it puts it in raw mode for
// each line and edits in place (arrows, Home/End, Ctrl-A/E/U/W/K, history
// on Up/Down); elsewhere, or when raw mode fails, it reads cooked lines.
type lineEditor struct {
	tty  *os.File
	r    *bufio.Reader
	w    io.Writer
	hist *chatHistory
	// raw switches the terminal to raw mode and returns the restore
	// function; nil reads cooked lines. Tests set it to a no-op.
	raw func() (func(), error)
	// width returns the terminal's column count; 0 means unknown, and
	// redraw then assumes the line never wraps.
	width func() int
	// row is the cursor's row below the prompt line's first row, as left
	// by the last redraw.
	row int

	mu        sync.Mutex
	restore   func()        // raw-mode restore while ReadLine is in raw mode
	interrupt chan struct{} // SIGINT at the prompt unblocks ReadLine
	maxInput  int           // 0 means maxChatInputBytes
	truncated bool          // the last read hit the input cap
}

func newLineEditor(tty *os.File, r io.Reader, w io.Writer, hist *chatHistory) *lineEditor {
	e := &lineEditor{tty: tty, r: bufio.NewReader(r), w: w, hist: hist, interrupt: make(chan struct{}, 1)}
	if e.hist == nil {
		e.hist = &chatHistory{}
	}
	if tty != nil && isTerminal(tty) {
		e.raw = func() (func(), error) { return makeRaw(tty) }
		e.width = func() int { return terminalWidth(tty) }
	}
	return e
}

// setRestore records the function that puts the terminal back after raw mode.
func (e *lineEditor) setRestore(fn func()) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.restore = fn
	e.mu.Unlock()
}

// restoreRaw puts the terminal back if ReadLine put it in raw mode.
// It is safe to call more than once and from another goroutine (SIGINT).
func (e *lineEditor) restoreRaw() {
	if e == nil {
		return
	}
	e.mu.Lock()
	fn := e.restore
	e.restore = nil
	e.mu.Unlock()
	if fn != nil {
		fn()
	}
}

// cancelRead restores the terminal and unblocks a pending ReadLine with
// errInterrupt. Used when SIGINT arrives at the prompt (cooked mode,
// Windows, or kill -INT during raw input).
func (e *lineEditor) cancelRead() {
	if e == nil {
		return
	}
	e.restoreRaw()
	if e.interrupt == nil {
		return
	}
	select {
	case e.interrupt <- struct{}{}:
	default:
	}
}

func (e *lineEditor) interrupted() <-chan struct{} {
	if e == nil {
		return nil
	}
	return e.interrupt
}

const (
	enableBracketedPaste  = "\x1b[?2004h"
	disableBracketedPaste = "\x1b[?2004l"
)

func (e *lineEditor) inputMax() int {
	if e != nil && e.maxInput > 0 {
		return e.maxInput
	}
	return maxChatInputBytes
}

// takeTruncated reports whether a read hit the input cap and clears the flag.
func (e *lineEditor) takeTruncated() bool {
	if e == nil {
		return false
	}
	t := e.truncated
	e.truncated = false
	return t
}

// ReadLine shows prompt and returns the line typed, without its newline.
// Ctrl-D on an empty line returns io.EOF; Ctrl-C returns errInterrupt.
func (e *lineEditor) ReadLine(prompt string) (string, error) {
	if e.raw != nil {
		if restore, err := e.raw(); err == nil {
			e.setRestore(func() {
				io.WriteString(e.w, disableBracketedPaste)
				restore()
			})
			defer e.restoreRaw()
			io.WriteString(e.w, enableBracketedPaste)
			return e.readRaw(prompt)
		}
	}
	io.WriteString(e.w, prompt)
	return e.readCooked()
}

// readCooked reads one line, or returns errInterrupt if cancelRead runs.
func (e *lineEditor) readCooked() (string, error) {
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := e.r.ReadString('\n')
		if err != nil && (line == "" || err != io.EOF) {
			if err == io.EOF {
				io.WriteString(e.w, "\n")
			}
			ch <- result{"", err}
			return
		}
		ch <- result{e.limitInput(strings.TrimRight(line, "\r\n")), nil}
	}()
	select {
	case <-e.interrupted():
		return "", errInterrupt
	case r := <-ch:
		return r.line, r.err
	}
}

// readRune reads one rune, or returns errInterrupt if cancelRead runs.
func (e *lineEditor) readRune() (rune, int, error) {
	type result struct {
		r   rune
		n   int
		err error
	}
	ch := make(chan result, 1)
	go func() {
		r, n, err := e.r.ReadRune()
		ch <- result{r, n, err}
	}()
	select {
	case <-e.interrupted():
		return 0, 0, errInterrupt
	case r := <-ch:
		return r.r, r.n, r.err
	}
}

// lineState is the edit buffer of one raw line.
type lineState struct {
	buf   []rune
	pos   int
	hist  []string
	hi    int    // index into hist; len(hist) is the line being typed
	saved []rune // the line being typed while browsing history
}

// readRaw runs the key state machine until Enter, Ctrl-C or Ctrl-D.
//
// A prompt may start with lines of its own (the chat prompt opens with a
// blank line); they are written once here, and only the prompt's last line
// is redrawn on each key. Redrawing the whole prompt printed its newline
// again on every keystroke.
func (e *lineEditor) readRaw(prompt string) (string, error) {
	st := &lineState{hist: e.hist.entries, hi: len(e.hist.entries)}
	if i := strings.LastIndexByte(prompt, '\n'); i >= 0 {
		io.WriteString(e.w, prompt[:i+1])
		prompt = prompt[i+1:]
	}
	io.WriteString(e.w, prompt)
	e.row = 0
	for {
		r, _, err := e.readRune()
		if err != nil {
			e.toEnd(prompt, st)
			io.WriteString(e.w, "\r\n")
			if len(st.buf) > 0 && err == io.EOF {
				return string(st.buf), nil
			}
			return "", err
		}
		switch r {
		case '\r', '\n':
			e.toEnd(prompt, st)
			io.WriteString(e.w, "\r\n")
			return string(st.buf), nil
		case 3: // Ctrl-C
			e.toEnd(prompt, st)
			io.WriteString(e.w, "^C\r\n")
			return string(st.buf), errInterrupt
		case 4: // Ctrl-D
			if len(st.buf) == 0 {
				e.toEnd(prompt, st)
				io.WriteString(e.w, "\r\n")
				return "", io.EOF
			}
			st.del()
		case 1: // Ctrl-A
			st.pos = 0
		case 5: // Ctrl-E
			st.pos = len(st.buf)
		case 2: // Ctrl-B
			st.left()
		case 6: // Ctrl-F
			st.right()
		case 21: // Ctrl-U
			st.buf, st.pos = append([]rune(nil), st.buf[st.pos:]...), 0
		case 11: // Ctrl-K
			st.buf = st.buf[:st.pos]
		case 23: // Ctrl-W
			st.deleteWord()
		case 127, 8: // Backspace
			st.backspace()
		case 16: // Ctrl-P
			st.up()
		case 14: // Ctrl-N
			st.down()
		case 27:
			if err := e.escape(st); err != nil {
				e.toEnd(prompt, st)
				if errors.Is(err, errInterrupt) {
					io.WriteString(e.w, "^C\r\n")
					return string(st.buf), errInterrupt
				}
				io.WriteString(e.w, "\r\n")
				if len(st.buf) > 0 && err == io.EOF {
					return string(st.buf), nil
				}
				return "", err
			}
		case '\t':
			st.insert(' ')
		default:
			if r >= 32 {
				st.insert(r)
			}
		}
		e.redraw(prompt, st)
	}
}

// escape handles an escape sequence: CSI or SS3 cursor keys, Home, End,
// Delete, or a bracketed-paste start (CSI 200~). Unknown sequences are dropped.
func (e *lineEditor) escape(st *lineState) error {
	b, err := e.r.ReadByte()
	if err != nil || (b != '[' && b != 'O') {
		return nil
	}
	c, err := e.r.ReadByte()
	if err != nil {
		return nil
	}
	if c >= '0' && c <= '9' {
		num := string(c)
		for {
			d, err := e.r.ReadByte()
			if err != nil {
				return nil
			}
			if d == '~' {
				break
			}
			if (d < '0' || d > '9') && d != ';' {
				return nil
			}
			num += string(d)
		}
		switch num {
		case "1", "7":
			st.pos = 0
		case "4", "8":
			st.pos = len(st.buf)
		case "3":
			st.del()
		case "200":
			return e.paste(st)
		}
		return nil
	}
	switch c {
	case 'A':
		st.up()
	case 'B':
		st.down()
	case 'C':
		st.right()
	case 'D':
		st.left()
	case 'H':
		st.pos = 0
	case 'F':
		st.pos = len(st.buf)
	}
	return nil
}

// reEscape matches CSI (colours, cursor moves) and OSC (hyperlinks,
// titles) escape sequences, which take no columns.
var reEscape = regexp.MustCompile("\x1b\\[[0-9;?]*[ -/]*[@-~]|\x1b\\][^\x07\x1b]*(?:\x07|\x1b\\\\)")

// visibleWidth is the number of columns s takes on screen: escape
// sequences stripped, runes measured with termmd's CJK/emoji widths.
func visibleWidth(s string) int { return termmd.StringWidth(reEscape.ReplaceAllString(s, "")) }

// cols returns the terminal width, or 0 when unknown.
func (e *lineEditor) cols() int {
	if e.width == nil {
		return 0
	}
	return e.width()
}

// layout returns the column the cursor is at and the column the line ends
// at, counted from the start of the prompt line.
func layout(prompt string, st *lineState) (cur, end int) {
	pw := visibleWidth(prompt)
	text := strings.ReplaceAll(string(st.buf), "\n", "↵")
	before := strings.ReplaceAll(string(st.buf[:st.pos]), "\n", "↵")
	return pw + termmd.StringWidth(before), pw + termmd.StringWidth(text)
}

// redraw rewrites the prompt line in place and puts the cursor at pos. It
// never prints a newline for a line that fits the terminal: it moves back
// to the prompt's first row, returns to column 0 and clears to the end.
func (e *lineEditor) redraw(prompt string, st *lineState) {
	text := strings.ReplaceAll(string(st.buf), "\n", "↵")
	cur, end := layout(prompt, st)
	w := e.cols()
	var b strings.Builder
	if w <= 0 {
		b.WriteString("\r" + prompt + text + "\x1b[K")
		if back := end - cur; back > 0 {
			fmt.Fprintf(&b, "\x1b[%dD", back)
		}
		io.WriteString(e.w, b.String())
		return
	}
	if e.row > 0 {
		fmt.Fprintf(&b, "\x1b[%dA", e.row)
	}
	// Clear before writing: clearing after a line that ends in the last
	// column would erase that column.
	b.WriteString("\r\x1b[J" + prompt + text)
	endRow := end / w
	if end > 0 && end%w == 0 {
		// The cursor waits in the last column; move it onto the next row.
		b.WriteString("\r\n")
	}
	curRow, curCol := cur/w, cur%w
	if up := endRow - curRow; up > 0 {
		fmt.Fprintf(&b, "\x1b[%dA", up)
	}
	b.WriteString("\r")
	if curCol > 0 {
		fmt.Fprintf(&b, "\x1b[%dC", curCol)
	}
	e.row = curRow
	io.WriteString(e.w, b.String())
}

// toEnd moves the cursor below the last row of a wrapped line, so output
// after Enter starts under it rather than over it.
func (e *lineEditor) toEnd(prompt string, st *lineState) {
	w := e.cols()
	if w <= 0 {
		return
	}
	_, end := layout(prompt, st)
	if down := end/w - e.row; down > 0 {
		fmt.Fprintf(e.w, "\x1b[%dB", down)
	}
	e.row = end / w
}

func (st *lineState) insert(r rune) {
	if len(st.buf) >= maxChatInputBytes {
		return
	}
	st.buf = append(st.buf[:st.pos], append([]rune{r}, st.buf[st.pos:]...)...)
	st.pos++
}

func (st *lineState) insertString(s string) {
	if s == "" {
		return
	}
	runes := []rune(s)
	st.buf = append(st.buf[:st.pos], append(runes, st.buf[st.pos:]...)...)
	st.pos += len(runes)
}

func (e *lineEditor) limitInput(s string) string {
	max := e.inputMax()
	if len(s) <= max {
		return s
	}
	e.truncated = true
	s = s[:max]
	for len(s) > 0 && !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// paste reads a bracketed-paste payload until CSI 201~ and inserts it as
// one edit, so newlines stay in the message and the line is redrawn once.
func (e *lineEditor) paste(st *lineState) error {
	s, err := e.readPaste()
	room := e.inputMax() - len(string(st.buf))
	if room < 0 {
		room = 0
	}
	if len(s) > room {
		e.truncated = true
		s = s[:room]
		for len(s) > 0 && !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
	}
	st.insertString(s)
	return err
}

func (e *lineEditor) readPaste() (string, error) {
	type result struct {
		s   string
		err error
	}
	ch := make(chan result, 1)
	go func() {
		s, err := e.collectPaste()
		ch <- result{s, err}
	}()
	select {
	case <-e.interrupted():
		return "", errInterrupt
	case r := <-ch:
		return r.s, r.err
	}
}

func (e *lineEditor) collectPaste() (string, error) {
	var b strings.Builder
	limit := e.inputMax()
	for {
		r, _, err := e.r.ReadRune()
		if err != nil {
			return b.String(), err
		}
		if r == 0x1b {
			done, err := e.sawPasteEnd()
			if err != nil {
				return b.String(), err
			}
			if done {
				return b.String(), nil
			}
			continue
		}
		if r == '\r' {
			if p, err := e.r.Peek(1); err == nil && len(p) > 0 && p[0] == '\n' {
				e.r.ReadByte()
			}
			r = '\n'
		}
		if r < 32 && r != '\n' && r != '\t' {
			continue
		}
		n := utf8.RuneLen(r)
		if n < 0 {
			continue
		}
		if b.Len()+n > limit {
			e.truncated = true
			continue
		}
		b.WriteRune(r)
	}
}

func (e *lineEditor) sawPasteEnd() (bool, error) {
	r, _, err := e.r.ReadRune()
	if err != nil {
		return false, err
	}
	if r != '[' {
		return false, nil
	}
	var num strings.Builder
	for {
		r, _, err = e.r.ReadRune()
		if err != nil {
			return false, err
		}
		if r >= '0' && r <= '9' {
			if num.Len() < 16 {
				num.WriteRune(r)
			}
			continue
		}
		return r == '~' && num.String() == "201", nil
	}
}

func (st *lineState) backspace() {
	if st.pos > 0 {
		st.buf = append(st.buf[:st.pos-1], st.buf[st.pos:]...)
		st.pos--
	}
}

func (st *lineState) del() {
	if st.pos < len(st.buf) {
		st.buf = append(st.buf[:st.pos], st.buf[st.pos+1:]...)
	}
}

func (st *lineState) left() {
	if st.pos > 0 {
		st.pos--
	}
}

func (st *lineState) right() {
	if st.pos < len(st.buf) {
		st.pos++
	}
}

// deleteWord deletes the word before the cursor and the spaces after it.
func (st *lineState) deleteWord() {
	i := st.pos
	for i > 0 && st.buf[i-1] == ' ' {
		i--
	}
	for i > 0 && st.buf[i-1] != ' ' {
		i--
	}
	st.buf = append(st.buf[:i], st.buf[st.pos:]...)
	st.pos = i
}

func (st *lineState) up() {
	if st.hi == 0 {
		return
	}
	if st.hi == len(st.hist) {
		st.saved = append([]rune(nil), st.buf...)
	}
	st.hi--
	st.buf = []rune(st.hist[st.hi])
	st.pos = len(st.buf)
}

func (st *lineState) down() {
	if st.hi >= len(st.hist) {
		return
	}
	st.hi++
	if st.hi == len(st.hist) {
		st.buf = st.saved
	} else {
		st.buf = []rune(st.hist[st.hi])
	}
	st.pos = len(st.buf)
}
