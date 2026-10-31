package termmd

import "strings"

// word is a run of non-space text, possibly in several styles.
type word struct {
	pieces []span
	space  bool // a space separates it from the previous word
	w      int
}

func (w *word) add(c rune, sp span) {
	if n := len(w.pieces); n > 0 && w.pieces[n-1].st == sp.st && w.pieces[n-1].url == sp.url {
		w.pieces[n-1].text += string(c)
	} else {
		w.pieces = append(w.pieces, span{text: string(c), st: sp.st, url: sp.url})
	}
	w.w += RuneWidth(c)
}

// decorate prepares spans for output: plain output keeps backticks around
// code, and links get their URL after the text.
func (r *Renderer) decorate(spans []span) []span {
	var out []span
	for i, sp := range spans {
		if sp.st&stCode != 0 && !r.opt.Color {
			sp.text = "`" + sp.text + "`"
		}
		out = append(out, sp)
		if sp.url != "" && (i+1 == len(spans) || spans[i+1].url != sp.url) {
			text := ""
			for j := i; j >= 0 && spans[j].url == sp.url; j-- {
				text = spans[j].text + text
			}
			if text != sp.url && "["+text+"]" != sp.url {
				out = append(out, span{text: " (" + sp.url + ")", st: sp.st&^(stLink|stCode|stBold|stItalic) | stDim})
			}
		}
	}
	return out
}

// words splits spans at spaces. Code spans don't break at their spaces,
// and each wide (CJK) character is a word of its own that joins without a
// space, so CJK text can wrap between any two characters.
func words(spans []span) []word {
	var out []word
	var cur word
	pending := false
	flush := func() {
		if len(cur.pieces) > 0 {
			out = append(out, cur)
		}
		cur = word{}
	}
	for _, sp := range spans {
		for _, c := range sp.text {
			switch {
			case (c == ' ' || c == '\t') && sp.st&stCode == 0:
				flush()
				pending = true
				continue
			case RuneWidth(c) == 2:
				flush()
				cur.space, pending = pending, false
				cur.add(c, sp)
				flush()
				continue
			}
			if len(cur.pieces) == 0 {
				cur.space, pending = pending, false
			}
			cur.add(c, sp)
		}
	}
	flush()
	return out
}

// split cuts w into pieces at most width columns wide.
func split(w word, width int) []word {
	var out []word
	cur := word{space: w.space}
	for _, p := range w.pieces {
		for _, c := range p.text {
			if cw := RuneWidth(c); cur.w+cw > width && cur.w > 0 {
				out = append(out, cur)
				cur = word{}
			}
			cur.add(c, p)
		}
	}
	if len(cur.pieces) > 0 {
		out = append(out, cur)
	}
	return out
}

// wrapSpans lays spans out in lines of at most width columns, the first
// starting with first and the rest with rest. Every line ends in "\n";
// no spans give "".
func (r *Renderer) wrapSpans(spans []span, width int, first, rest string) string {
	ws := words(r.decorate(spans))
	if len(ws) == 0 {
		return ""
	}
	var b strings.Builder
	var run span // text waiting to be painted in one style
	paint := func() {
		b.WriteString(r.paint(run.text, run.st, run.url))
		run = span{}
	}
	put := func(p span) {
		if run.text != "" && (p.st != run.st || p.url != run.url) {
			paint()
		}
		run.text, run.st, run.url = run.text+p.text, p.st, p.url
	}
	b.WriteString(first)
	col, empty := visibleWidth(first), true
	restW := visibleWidth(rest)
	for i := 0; i < len(ws); i++ {
		w := ws[i]
		sep := 0
		if w.space && !empty {
			sep = 1
		}
		if !empty && col+sep+w.w > width {
			paint()
			b.WriteString("\n" + rest)
			col, empty, sep = restW, true, 0
		}
		if empty && col+w.w > width && width-col > 0 {
			if parts := split(w, width-col); len(parts) > 1 {
				ws = append(ws[:i], append(parts, ws[i+1:]...)...)
				w = ws[i]
			}
		}
		if sep == 1 {
			if p := w.pieces[0]; run.text != "" && p.st == run.st && p.url == run.url {
				run.text += " "
			} else {
				paint()
				b.WriteByte(' ')
			}
		}
		for _, p := range w.pieces {
			put(p)
		}
		col += sep + w.w
		empty = false
	}
	paint()
	b.WriteByte('\n')
	return b.String()
}
