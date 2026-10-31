package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
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
}

func newLineEditor(tty *os.File, r io.Reader, w io.Writer, hist *chatHistory) *lineEditor {
	e := &lineEditor{tty: tty, r: bufio.NewReader(r), w: w, hist: hist}
	if e.hist == nil {
		e.hist = &chatHistory{}
	}
	if tty != nil && isTerminal(tty) {
		e.raw = func() (func(), error) { return makeRaw(tty) }
	}
	return e
}

// ReadLine shows prompt and returns the line typed, without its newline.
// Ctrl-D on an empty line returns io.EOF; Ctrl-C returns errInterrupt.
func (e *lineEditor) ReadLine(prompt string) (string, error) {
	if e.raw != nil {
		if restore, err := e.raw(); err == nil {
			defer restore()
			return e.readRaw(prompt)
		}
	}
	io.WriteString(e.w, prompt)
	line, err := e.r.ReadString('\n')
	if err != nil && (line == "" || err != io.EOF) {
		if err == io.EOF {
			io.WriteString(e.w, "\n")
		}
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
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
func (e *lineEditor) readRaw(prompt string) (string, error) {
	st := &lineState{hist: e.hist.entries, hi: len(e.hist.entries)}
	io.WriteString(e.w, prompt)
	for {
		r, _, err := e.r.ReadRune()
		if err != nil {
			io.WriteString(e.w, "\r\n")
			if len(st.buf) > 0 && err == io.EOF {
				return string(st.buf), nil
			}
			return "", err
		}
		switch r {
		case '\r', '\n':
			io.WriteString(e.w, "\r\n")
			return string(st.buf), nil
		case 3: // Ctrl-C
			io.WriteString(e.w, "^C\r\n")
			return string(st.buf), errInterrupt
		case 4: // Ctrl-D
			if len(st.buf) == 0 {
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
			e.escape(st)
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
// Delete. Unknown sequences are dropped.
func (e *lineEditor) escape(st *lineState) {
	b, err := e.r.ReadByte()
	if err != nil || (b != '[' && b != 'O') {
		return
	}
	c, err := e.r.ReadByte()
	if err != nil {
		return
	}
	if c >= '0' && c <= '9' {
		num := string(c)
		for {
			d, err := e.r.ReadByte()
			if err != nil {
				return
			}
			if d == '~' {
				break
			}
			if (d < '0' || d > '9') && d != ';' {
				return
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
		}
		return
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
}

// redraw rewrites the line and puts the cursor at pos.
func (e *lineEditor) redraw(prompt string, st *lineState) {
	s := "\r" + prompt + strings.ReplaceAll(string(st.buf), "\n", "↵") + "\x1b[K"
	if back := len(st.buf) - st.pos; back > 0 {
		s += fmt.Sprintf("\x1b[%dD", back)
	}
	io.WriteString(e.w, s)
}

func (st *lineState) insert(r rune) {
	st.buf = append(st.buf[:st.pos], append([]rune{r}, st.buf[st.pos:]...)...)
	st.pos++
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
