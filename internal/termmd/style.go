package termmd

import (
	"strconv"
	"strings"
)

// sgr returns the ANSI SGR parameters for st, or "".
func sgr(st style) string {
	var p []string
	if st&stBold != 0 || st&stHeading != 0 {
		p = append(p, "1")
	}
	if st&stDim != 0 {
		p = append(p, "2")
	}
	if st&stItalic != 0 {
		p = append(p, "3")
	}
	if st&stLink != 0 {
		p = append(p, "4", "34")
	}
	if st&stStrike != 0 {
		p = append(p, "9")
	}
	if st&stCode != 0 {
		p = append(p, "36")
	} else if st&stHeading != 0 {
		p = append(p, "35")
	}
	return strings.Join(p, ";")
}

// paint wraps text in the escape codes for st when color is on, and an
// OSC 8 hyperlink to url when those are on.
func (r *Renderer) paint(text string, st style, url string) string {
	if !r.opt.Color || text == "" {
		return text
	}
	out := text
	if p := sgr(st); p != "" {
		out = "\x1b[" + p + "m" + text + "\x1b[0m"
	}
	if url != "" && r.opt.Hyperlinks {
		out = "\x1b]8;;" + url + "\x1b\\" + out + "\x1b]8;;\x1b\\"
	}
	return out
}

// SupportsHyperlinks reports whether the terminal described by getenv
// clearly renders OSC 8 hyperlinks. Unknown terminals, tmux and screen
// get false: there the escape can show up as garbage.
func SupportsHyperlinks(getenv func(string) string) bool {
	if getenv("TMUX") != "" || getenv("STY") != "" || getenv("TERM") == "dumb" {
		return false
	}
	switch getenv("TERM_PROGRAM") {
	case "iTerm.app", "WezTerm", "vscode", "ghostty", "Hyper", "rio":
		return true
	}
	switch getenv("TERM") {
	case "xterm-kitty", "xterm-ghostty", "foot", "wezterm":
		return true
	}
	if getenv("WT_SESSION") != "" || getenv("KITTY_WINDOW_ID") != "" {
		return true
	}
	if v, err := strconv.Atoi(getenv("VTE_VERSION")); err == nil && v >= 5000 {
		return true
	}
	return false
}
