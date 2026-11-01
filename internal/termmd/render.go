// Package termmd renders Markdown for a terminal as it streams in.
//
// A Renderer buffers text until a block is complete (a paragraph ends at a
// blank line, a fence at its closing line, a list item at the next item)
// and then writes that block once, wrapped to the width, so streamed text
// never re-renders. Flush writes whatever is still buffered, so a stream
// that ends mid-block loses nothing. With Color off the output has no
// escape codes but keeps the structure: indentation, bullets, code-block
// borders and table columns.
package termmd

import (
	"io"
	"regexp"
	"strings"
)

// Options configure a Renderer.
type Options struct {
	Width      int  // columns to wrap to; under 20 means 80
	Color      bool // ANSI styles and Unicode borders and bullets
	Hyperlinks bool // OSC 8 links (only with Color)
}

type blockKind int

const (
	kNone blockKind = iota
	kPara
	kFence
	kList
	kQuote
	kTable
)

// Renderer turns streamed Markdown into terminal text. It is not safe for
// concurrent use.
type Renderer struct {
	w       io.Writer
	opt     Options
	partial string // text after the last newline

	kind      blockKind
	lines     []string
	fence     string // the opening fence's backticks or tildes
	fenceInd  int
	lang      string
	itemInd   int  // the current list item's indent
	listGroup bool // the last block written was a list item
	wrote     bool // a block was written (the next one gets a blank line)
	blank     bool // a blank line came after the last block written
}

// New returns a Renderer writing to w.
func New(w io.Writer, opt Options) *Renderer {
	if opt.Width < 20 {
		opt.Width = 80
	}
	if !opt.Color {
		opt.Hyperlinks = false
	}
	return &Renderer{w: w, opt: opt}
}

// Render renders a whole document.
func Render(md string, opt Options) string {
	var b strings.Builder
	r := New(&b, opt)
	r.Write(md)
	r.Flush()
	return b.String()
}

// Write adds streamed text. Complete blocks are written; the rest waits.
func (r *Renderer) Write(s string) {
	s = r.partial + s
	for {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			break
		}
		r.line(sanitize(strings.TrimSuffix(s[:i], "\r")))
		s = s[i+1:]
	}
	r.partial = s
}

// Flush writes everything still buffered, ending any open block. An
// unclosed fence is written as a code block.
func (r *Renderer) Flush() {
	if r.partial != "" {
		p := r.partial
		r.partial = ""
		r.line(sanitize(p))
	}
	r.end()
}

// sanitize drops escape sequences and control characters other than tab,
// so model output can't move the cursor or change terminal state.
func sanitize(s string) string {
	if !strings.ContainsFunc(s, isControl) {
		return s
	}
	s = reEscape.ReplaceAllString(s, "")
	return strings.Map(func(r rune) rune {
		if isControl(r) {
			return -1
		}
		return r
	}, s)
}

func isControl(r rune) bool {
	return (r < 0x20 && r != '\t' && r != '\n') || r == 0x7f || (r >= 0x80 && r < 0xa0)
}

var (
	reHeading = regexp.MustCompile(`^ {0,3}(#{1,6})(?:[ \t]+(.*?))?[ \t#]*$`)
	reHR      = regexp.MustCompile(`^ {0,3}(?:(?:\*[ \t]*){3,}|(?:-[ \t]*){3,}|(?:_[ \t]*){3,})$`)
	reFence   = regexp.MustCompile("^( *)(`{3,}|~{3,})[ \t]*([^`]*)$")
	reItem    = regexp.MustCompile(`^( *)([-*+]|\d{1,9}[.)])(?:[ \t]+(.*)|$)`)
	reQuote   = regexp.MustCompile(`^ {0,3}> ?(.*)$`)
	reSep     = regexp.MustCompile(`^ *\|? *:?-+:? *(?:\| *:?-+:? *)*\|? *$`)
)

func indentOf(s string) int {
	s = strings.ReplaceAll(s, "\t", "    ")
	return len(s) - len(strings.TrimLeft(s, " "))
}

// line handles one complete source line.
func (r *Renderer) line(l string) {
	switch r.kind {
	case kFence:
		if t := strings.TrimSpace(l); strings.HasPrefix(t, r.fence[:1]) && run(t, r.fence[0]) >= len(r.fence) && strings.Trim(t, r.fence[:1]) == "" {
			r.end()
			return
		}
		r.lines = append(r.lines, l)
		return
	case kTable:
		if strings.Contains(l, "|") && strings.TrimSpace(l) != "" {
			r.lines = append(r.lines, l)
			return
		}
		r.end()
	}
	if strings.TrimSpace(l) == "" {
		r.end()
		r.blank = true
		return
	}
	if m := reFence.FindStringSubmatch(l); m != nil && !(m[2][0] == '`' && strings.Contains(m[3], "`")) {
		r.end()
		r.kind, r.fence, r.fenceInd, r.lang = kFence, m[2], len(m[1]), ""
		if f := strings.Fields(m[3]); len(f) > 0 {
			r.lang = f[0]
		}
		return
	}
	if m := reHeading.FindStringSubmatch(l); m != nil {
		r.end()
		r.heading(len(m[1]), m[2])
		return
	}
	if reHR.MatchString(l) {
		r.end()
		r.block(r.hr(), false)
		return
	}
	if r.kind == kPara && reSep.MatchString(l) && strings.Contains(l, "|") {
		if hdr := r.lines[len(r.lines)-1]; strings.Contains(hdr, "|") {
			r.lines = r.lines[:len(r.lines)-1]
			r.end()
			r.kind, r.lines = kTable, []string{hdr, l}
			return
		}
	}
	if m := reQuote.FindStringSubmatch(l); m != nil {
		if r.kind != kQuote {
			r.end()
		}
		r.kind = kQuote
		r.lines = append(r.lines, m[1])
		return
	}
	if reItem.MatchString(l) {
		r.end()
		r.kind, r.itemInd, r.lines = kList, indentOf(l), []string{l}
		return
	}
	if r.kind == kList || r.kind == kQuote {
		if r.kind == kList {
			r.lines = append(r.lines, l)
			return
		}
		r.end()
	}
	if r.kind == kNone && r.listGroup && indentOf(l) >= 2 {
		// An indented paragraph after a blank line continues the last item.
		r.kind, r.itemInd, r.lines = kList, -1, []string{l}
		return
	}
	r.kind = kPara
	r.lines = append(r.lines, l)
}

// end writes the open block, if any.
func (r *Renderer) end() {
	kind, lines := r.kind, r.lines
	r.kind, r.lines = kNone, nil
	switch kind {
	case kPara:
		if len(lines) == 0 {
			return
		}
		var parts []string
		for _, l := range lines {
			parts = append(parts, strings.TrimSpace(l))
		}
		r.block(r.wrapSpans(parseInline(strings.Join(parts, " "), 0), r.opt.Width, "", ""), false)
	case kFence:
		r.block(r.code(lines), false)
	case kList:
		r.block(r.item(lines), true)
	case kQuote:
		r.block(r.quote(lines), false)
	case kTable:
		r.block(r.table(lines), false)
	}
}

// block writes one rendered block, after a blank line unless it continues
// a list.
func (r *Renderer) block(text string, listItem bool) {
	if text == "" {
		return
	}
	if r.wrote && !(listItem && r.listGroup && !r.blank) {
		text = "\n" + text
	}
	r.wrote, r.listGroup, r.blank = true, listItem, false
	io.WriteString(r.w, text)
}

func (r *Renderer) glyph(fancy, plain string) string {
	if r.opt.Color {
		return fancy
	}
	return plain
}

func (r *Renderer) heading(level int, text string) {
	if !r.opt.Color {
		prefix := strings.Repeat("#", level) + " "
		r.block(r.wrapSpans(parseInline(text, 0), r.opt.Width, prefix, strings.Repeat(" ", len(prefix))), false)
		return
	}
	out := r.wrapSpans(parseInline(text, stHeading), r.opt.Width, "", "")
	if level == 1 {
		w := 0
		for _, l := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
			w = max(w, visibleWidth(l))
		}
		out += r.paint(strings.Repeat("━", min(w, r.opt.Width)), stDim, "") + "\n"
	}
	r.block(out, false)
}

func (r *Renderer) hr() string {
	w := min(r.opt.Width, 72)
	return r.paint(strings.Repeat(r.glyph("─", "-"), w), stDim, "") + "\n"
}

// item renders one list item (marker line plus continuation lines).
func (r *Renderer) item(lines []string) string {
	ind := r.itemInd
	var marker, text string
	if ind < 0 { // continuation paragraph
		ind = indentOf(lines[0])
		for _, l := range lines {
			text += " " + strings.TrimSpace(l)
		}
		pad := strings.Repeat(" ", min(ind, 2*8))
		return r.wrapSpans(parseInline(strings.TrimSpace(text), 0), r.opt.Width, pad, pad)
	}
	m := reItem.FindStringSubmatch(lines[0])
	marker, text = m[2], m[3]
	for _, l := range lines[1:] {
		text += " " + strings.TrimSpace(l)
	}
	level := min(ind/2, 8)
	if marker == "-" || marker == "*" || marker == "+" {
		bullets := []string{"•", "◦", "▪"}
		marker = r.glyph(bullets[level%3], "-")
	}
	if t := strings.TrimSpace(text); strings.HasPrefix(t, "[ ] ") || strings.HasPrefix(t, "[x] ") || strings.HasPrefix(t, "[X] ") {
		box := r.glyph("☐", "[ ]")
		if t[1] != ' ' {
			box = r.glyph("☑", "[x]")
		}
		text = box + " " + t[4:]
	}
	first := strings.Repeat("  ", level) + marker + " "
	rest := strings.Repeat(" ", visibleWidth(first))
	return r.wrapSpans(parseInline(strings.TrimSpace(text), 0), r.opt.Width, first, rest)
}

// quote renders a blockquote by rendering its contents narrower and
// prefixing a bar.
func (r *Renderer) quote(lines []string) string {
	sub := Render(strings.Join(lines, "\n"), Options{Width: max(r.opt.Width-2, 20), Color: r.opt.Color, Hyperlinks: r.opt.Hyperlinks})
	bar := r.paint(r.glyph("│", "|"), stDim, "") + " "
	var b strings.Builder
	for _, l := range strings.Split(strings.TrimSuffix(sub, "\n"), "\n") {
		if l == "" {
			b.WriteString(strings.TrimRight(bar, " ") + "\n")
			continue
		}
		if r.opt.Color {
			l = strings.ReplaceAll(l, "\x1b[0m", "\x1b[0;3m")
			l = "\x1b[3m" + l + "\x1b[0m"
		}
		b.WriteString(bar + l + "\n")
	}
	return b.String()
}

// reEscape matches CSI, OSC and two-byte escape sequences.
var reEscape = regexp.MustCompile("\x1b(?:\\[[0-?]*[ -/]*[@-~]|\\][^\x07\x1b]*(?:\x07|\x1b\\\\)?|[@-Z\\\\-_])")

var reANSI = regexp.MustCompile("\x1b\\[[0-9;]*m|\x1b\\]8;;[^\x1b]*\x1b\\\\")

// visibleWidth is the width of s without its escape codes.
func visibleWidth(s string) int { return StringWidth(reANSI.ReplaceAllString(s, "")) }
