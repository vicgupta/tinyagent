package tools

import (
	"bytes"
	"html"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

// htmlToText converts an HTML document to readable, Markdown-like text with
// the standard library only. Script, style, nav and similar elements are
// dropped; headings, list items, links and pre blocks are kept; whitespace
// is collapsed. Relative links resolve against base. It is a best-effort
// reader for documentation pages, not a full HTML parser.
func htmlToText(src string, base *url.URL) string {
	c := &htmlConv{base: base}
	for i := 0; i < len(src); {
		if src[i] != '<' {
			j := strings.IndexByte(src[i:], '<')
			if j < 0 {
				j = len(src) - i
			}
			c.text(src[i : i+j])
			i += j
			continue
		}
		rest := src[i:]
		switch {
		case strings.HasPrefix(rest, "<!--"):
			end := strings.Index(rest[4:], "-->")
			if end < 0 {
				return c.done()
			}
			i += 4 + end + 3
			continue
		case strings.HasPrefix(rest, "<!"), strings.HasPrefix(rest, "<?"):
			end := strings.IndexByte(rest, '>')
			if end < 0 {
				return c.done()
			}
			i += end + 1
			continue
		case len(rest) < 2 || !(isASCIILetter(rest[1]) || (rest[1] == '/' && len(rest) > 2 && isASCIILetter(rest[2]))):
			c.text("<")
			i++
			continue
		}
		end := tagEnd(rest)
		if end < 0 {
			return c.done()
		}
		raw := rest[1:end]
		i += end + 1
		closing := strings.HasPrefix(raw, "/")
		if closing {
			raw = raw[1:]
		}
		name, attrs := parseTag(raw)
		if closing {
			c.close(name)
			continue
		}
		if name == "script" || name == "style" {
			// Raw text: skip to the matching close tag.
			k := strings.Index(strings.ToLower(src[i:]), "</"+name)
			if k < 0 {
				return c.done()
			}
			i += k
			continue
		}
		c.open(name, attrs, strings.HasSuffix(raw, "/"))
	}
	return c.done()
}

// Elements whose content is dropped.
var htmlSkip = map[string]bool{
	"script": true, "style": true, "noscript": true, "template": true, "svg": true,
	"nav": true, "iframe": true, "object": true, "canvas": true, "select": true, "button": true,
}

// Elements that start and end a block.
var htmlBlock = map[string]bool{
	"p": true, "div": true, "section": true, "article": true, "main": true, "header": true,
	"footer": true, "aside": true, "ul": true, "ol": true, "dl": true, "dt": true, "dd": true,
	"table": true, "thead": true, "tbody": true, "tfoot": true, "tr": true, "blockquote": true,
	"form": true, "figure": true, "figcaption": true, "title": true, "address": true,
	"details": true, "summary": true, "fieldset": true, "caption": true, "body": true, "html": true,
}

type htmlConv struct {
	base  *url.URL
	buf   bytes.Buffer
	skip  []string // open dropped elements
	pre   int
	links []htmlLink
	space bool // whitespace is pending before the next word
	glue  bool // just opened a link or code span: no space before the next word
}

type htmlLink struct {
	href  string
	start int // buffer offset of "[", or -1 for no link
}

func (c *htmlConv) open(name string, attrs map[string]string, selfClosing bool) {
	if len(c.skip) > 0 || htmlSkip[name] {
		if htmlSkip[name] && !selfClosing {
			c.skip = append(c.skip, name)
		}
		return
	}
	switch {
	case name == "br":
		c.newline()
	case name == "hr":
		c.block(2)
		c.buf.WriteString("---")
		c.block(2)
	case len(name) == 2 && name[0] == 'h' && name[1] >= '1' && name[1] <= '6':
		c.block(2)
		c.buf.WriteString(strings.Repeat("#", int(name[1]-'0')) + " ")
	case name == "li":
		c.block(1)
		c.buf.WriteString("- ")
	case name == "pre":
		c.block(2)
		c.buf.WriteString("```\n")
		c.pre++
	case name == "td" || name == "th":
		c.space = true
	case name == "code" && c.pre == 0:
		c.word("`")
		c.glue = true
	case name == "a" && !selfClosing:
		l := htmlLink{start: -1}
		if href := c.resolve(attrs["href"]); href != "" && c.pre == 0 {
			c.word("")
			l = htmlLink{href: href, start: c.buf.Len()}
			c.buf.WriteByte('[')
			c.glue = true
		}
		c.links = append(c.links, l)
	case htmlBlock[name]:
		c.block(2)
	}
}

func (c *htmlConv) close(name string) {
	if len(c.skip) > 0 {
		if c.skip[len(c.skip)-1] == name {
			c.skip = c.skip[:len(c.skip)-1]
		}
		return
	}
	switch {
	case len(name) == 2 && name[0] == 'h' && name[1] >= '1' && name[1] <= '6':
		c.block(2)
	case name == "li":
		c.block(1)
	case name == "pre" && c.pre > 0:
		c.pre--
		if b := c.buf.Bytes(); len(b) > 0 && b[len(b)-1] != '\n' {
			c.buf.WriteByte('\n')
		}
		c.buf.WriteString("```")
		c.block(2)
	case name == "code" && c.pre == 0:
		c.buf.WriteString("`")
	case name == "a" && len(c.links) > 0:
		l := c.links[len(c.links)-1]
		c.links = c.links[:len(c.links)-1]
		if l.start < 0 {
			return
		}
		if strings.TrimSpace(c.buf.String()[l.start+1:]) == "" {
			c.buf.Truncate(l.start) // no link text: drop the link
			return
		}
		c.buf.WriteString("](" + l.href + ")")
	case htmlBlock[name]:
		c.block(2)
	}
}

func (c *htmlConv) resolve(href string) string {
	href = strings.TrimSpace(html.UnescapeString(href))
	if href == "" || strings.HasPrefix(href, "#") {
		return ""
	}
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if c.base != nil {
		u = c.base.ResolveReference(u)
	}
	if u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "mailto" {
		return ""
	}
	return u.String()
}

func (c *htmlConv) text(s string) {
	if len(c.skip) > 0 || s == "" {
		return
	}
	s = html.UnescapeString(s)
	if c.pre > 0 {
		c.buf.WriteString(s)
		return
	}
	first, _ := utf8.DecodeRuneInString(s)
	last, _ := utf8.DecodeLastRuneInString(s)
	words := strings.Fields(s)
	if len(words) == 0 {
		c.space = true
		return
	}
	if unicode.IsSpace(first) {
		c.space = true
	}
	c.word(strings.Join(words, " "))
	c.space = unicode.IsSpace(last)
}

// word writes s after any pending space, unless at the start of a line.
func (c *htmlConv) word(s string) {
	if c.space && !c.glue && !c.lineStart() {
		c.buf.WriteByte(' ')
	}
	c.space, c.glue = false, false
	c.buf.WriteString(s)
}

func (c *htmlConv) lineStart() bool {
	b := c.buf.Bytes()
	return len(b) == 0 || b[len(b)-1] == '\n' ||
		bytes.HasSuffix(b, []byte("- ")) || bytes.HasSuffix(b, []byte("# "))
}

func (c *htmlConv) newline() {
	c.trimSpaces()
	c.buf.WriteByte('\n')
	c.space = false
}

// block ends the current line and leaves n line breaks before what follows.
func (c *htmlConv) block(n int) {
	c.space = false
	if c.pre > 0 {
		return
	}
	c.trimSpaces()
	b := c.buf.Bytes()
	if len(b) == 0 {
		return
	}
	have := len(b) - len(bytes.TrimRight(b, "\n"))
	for ; have < n; have++ {
		c.buf.WriteByte('\n')
	}
}

func (c *htmlConv) trimSpaces() {
	b := c.buf.Bytes()
	c.buf.Truncate(len(bytes.TrimRight(b, " \t")))
}

func (c *htmlConv) done() string {
	lines := strings.Split(c.buf.String(), "\n")
	out := make([]string, 0, len(lines))
	blank, inPre := false, false
	for _, l := range lines {
		if strings.HasPrefix(l, "```") {
			inPre = !inPre
		}
		if !inPre {
			l = strings.TrimRight(l, " \t")
		}
		if strings.TrimSpace(l) == "" && !inPre {
			if blank || len(out) == 0 {
				continue
			}
			blank = true
			out = append(out, "")
			continue
		}
		blank = false
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func isASCIILetter(b byte) bool { return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' }

// tagEnd returns the index of the '>' that ends the tag starting s, skipping
// quoted attribute values, or -1.
func tagEnd(s string) int {
	var quote byte
	for i := 1; i < len(s); i++ {
		switch b := s[i]; {
		case quote != 0:
			if b == quote {
				quote = 0
			}
		case b == '"' || b == '\'':
			if i > 0 && s[i-1] == '=' || i > 1 && s[i-1] == ' ' && s[i-2] == '=' {
				quote = b
			}
		case b == '>':
			return i
		}
	}
	return -1
}

// parseTag returns the lowercase tag name and attributes of a tag body.
func parseTag(raw string) (string, map[string]string) {
	n := 0
	for n < len(raw) && !strings.ContainsRune(" \t\r\n/>", rune(raw[n])) {
		n++
	}
	name := strings.ToLower(raw[:n])
	attrs := map[string]string{}
	s := raw[n:]
	for {
		s = strings.TrimLeft(s, " \t\r\n/")
		if s == "" {
			return name, attrs
		}
		k := 0
		for k < len(s) && !strings.ContainsRune(" \t\r\n=/", rune(s[k])) {
			k++
		}
		key := strings.ToLower(s[:k])
		s = strings.TrimLeft(s[k:], " \t\r\n")
		val := ""
		if strings.HasPrefix(s, "=") {
			s = strings.TrimLeft(s[1:], " \t\r\n")
			if s != "" && (s[0] == '"' || s[0] == '\'') {
				q := s[0]
				end := strings.IndexByte(s[1:], q)
				if end < 0 {
					val, s = s[1:], ""
				} else {
					val, s = s[1:1+end], s[2+end:]
				}
			} else {
				v := 0
				for v < len(s) && !strings.ContainsRune(" \t\r\n", rune(s[v])) {
					v++
				}
				val, s = s[:v], s[v:]
			}
		}
		if key != "" {
			if _, dup := attrs[key]; !dup {
				attrs[key] = val
			}
		}
		if k == 0 && val == "" && !strings.HasPrefix(s, "=") {
			if len(s) > 0 {
				s = s[1:]
			}
		}
	}
}
