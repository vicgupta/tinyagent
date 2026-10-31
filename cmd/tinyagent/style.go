package main

import (
	"fmt"
	"io"
	"os"
)

// Terminal styling (issue #291). Every ANSI sequence tinyagent prints goes
// through a styler, which is on only when --color allows it:
//
//   - auto (default): the stream is a terminal and NO_COLOR is unset or
//     empty (https://no-color.org);
//   - always: styled even when piped and even with NO_COLOR set;
//   - never: plain text.
//
// JSON and ACP output never go through a styler.

// colorModes are the values --color accepts.
var colorModes = []string{"auto", "always", "never"}

// validColorMode reports whether m is a --color value.
func validColorMode(m string) bool {
	for _, v := range colorModes {
		if m == v {
			return true
		}
	}
	return false
}

// stdoutIsTerminal and stderrIsTerminal report whether the stream is an
// interactive terminal; tests replace them.
var (
	stdoutIsTerminal = func() bool { return isTerminal(os.Stdout) }
	stderrIsTerminal = func() bool { return isTerminal(os.Stderr) }
)

// colorEnabled decides styling for one stream under mode.
func colorEnabled(mode string, tty bool) bool {
	switch mode {
	case "always":
		return true
	case "never":
		return false
	}
	return tty && os.Getenv("NO_COLOR") == ""
}

// styler wraps text in ANSI styles when on, and returns it unchanged
// otherwise.
type styler struct{ on bool }

// stdoutStyle and stderrStyle are set once from --color after parsing.
var stdoutStyle, stderrStyle styler

// setColor resolves --color for stdout and stderr.
func setColor(mode string) {
	stdoutStyle = styler{colorEnabled(mode, stdoutIsTerminal())}
	stderrStyle = styler{colorEnabled(mode, stderrIsTerminal())}
}

func (s styler) wrap(code, text string) string {
	if !s.on {
		return text
	}
	return fmt.Sprintf("\x1b[%sm%s\x1b[0m", code, text)
}

// Bold, Red, Yellow, Green and Dim style text.
func (s styler) Bold(t string) string   { return s.wrap("1", t) }
func (s styler) Red(t string) string    { return s.wrap("1;31", t) }
func (s styler) Yellow(t string) string { return s.wrap("33", t) }
func (s styler) Green(t string) string  { return s.wrap("32", t) }
func (s styler) Dim(t string) string    { return s.wrap("2", t) }

// Glyph returns fancy when styling is on and plain otherwise, for
// decorative characters a plain-text consumer shouldn't get.
func (s styler) Glyph(fancy, plain string) string {
	if s.on {
		return fancy
	}
	return plain
}

// styleFor returns the styler for w: stdout's or stderr's, and an off
// styler for anything else (files, buffers), so styling never leaks into
// captured output.
func styleFor(w io.Writer) styler {
	switch w {
	case io.Writer(os.Stdout):
		return stdoutStyle
	case io.Writer(os.Stderr):
		return stderrStyle
	}
	return styler{}
}
