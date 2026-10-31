package termmd

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// style is a set of inline styles.
type style uint8

const (
	stBold style = 1 << iota
	stItalic
	stCode
	stStrike
	stLink
	stDim
	stHeading
)

// span is a run of text with one style. url is set for link text.
type span struct {
	text string
	st   style
	url  string
}

// parseInline splits one paragraph's text into styled spans: `code`,
// **bold** / __bold__, *italic* / _italic_, ~~strike~~, [text](url),
// ![alt](url), <url> and backslash escapes. Unmatched delimiters stay
// literal.
func parseInline(s string, base style) []span {
	var out []span
	var cur strings.Builder
	st := base
	push := func() {
		if cur.Len() > 0 {
			out = append(out, span{text: cur.String(), st: st})
			cur.Reset()
		}
	}
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '\\' && i+1 < len(s) && strings.IndexByte("\\`*_{}[]()#+-.!|~<>\"'", s[i+1]) >= 0:
			cur.WriteByte(s[i+1])
			i += 2
			continue
		case c == '`':
			n := run(s[i:], '`')
			fence := s[i : i+n]
			if j := strings.Index(s[i+n:], fence); j >= 0 {
				push()
				code := s[i+n : i+n+j]
				if t := strings.TrimSpace(code); t != "" && len(code) >= 2 && code[0] == ' ' && code[len(code)-1] == ' ' {
					code = code[1 : len(code)-1]
				}
				out = append(out, span{text: code, st: st | stCode})
				i += 2*n + j
				continue
			}
			cur.WriteString(fence)
			i += n
			continue
		case c == '*' || c == '_' || c == '~':
			n := run(s[i:], c)
			var flag style
			var d string
			switch {
			case c == '~' && n >= 2:
				flag, d = stStrike, "~~"
			case c == '~':
			case n >= 2:
				flag, d = stBold, s[i:i+2]
			default:
				flag, d = stItalic, s[i:i+1]
			}
			if flag != 0 && delimiterOK(s, i, len(d), c, st&flag != 0) {
				push()
				st ^= flag
				i += len(d)
				continue
			}
			cur.WriteString(s[i : i+n])
			i += n
			continue
		case c == '[' || (c == '!' && i+1 < len(s) && s[i+1] == '['):
			start := i
			if c == '!' {
				start++
			}
			if text, url, end, ok := linkAt(s, start); ok {
				push()
				if c == '!' {
					alt := text
					if alt == "" {
						alt = "image"
					}
					out = append(out, span{text: "[" + alt + "]", st: st | stLink, url: url})
				} else {
					for _, sp := range parseInline(text, st) {
						sp.st |= stLink
						sp.url = url
						out = append(out, sp)
					}
				}
				i = end
				continue
			}
		case c == '<':
			if j := strings.IndexByte(s[i:], '>'); j > 0 {
				u := s[i+1 : i+j]
				if (strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://")) && !strings.ContainsAny(u, " <") {
					push()
					out = append(out, span{text: u, st: st | stLink, url: u})
					i += j + 1
					continue
				}
			}
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		cur.WriteString(s[i : i+size])
		i += size
	}
	push()
	return out
}

// run counts the bytes equal to c at the start of s.
func run(s string, c byte) int {
	n := 0
	for n < len(s) && s[n] == c {
		n++
	}
	return n
}

// delimiterOK reports whether the emphasis delimiter of length n at s[i]
// opens (a closer exists later and text follows) or closes (text precedes).
// Underscores inside words stay literal.
func delimiterOK(s string, i, n int, c byte, closing bool) bool {
	before, _ := utf8.DecodeLastRuneInString(s[:i])
	after, _ := utf8.DecodeRuneInString(s[i+n:])
	if i == 0 {
		before = ' '
	}
	if i+n >= len(s) {
		after = ' '
	}
	if c == '_' && isWordRune(before) && isWordRune(after) {
		return false
	}
	if closing {
		return !unicode.IsSpace(before)
	}
	if unicode.IsSpace(after) {
		return false
	}
	return strings.Contains(s[i+n:], s[i:i+n])
}

func isWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// linkAt parses [text](url) at s[i] and returns the index after it.
func linkAt(s string, i int) (text, url string, end int, ok bool) {
	depth := 0
	j := i
	for ; j < len(s); j++ {
		switch s[j] {
		case '\\':
			j++
		case '[':
			depth++
		case ']':
			depth--
		}
		if depth == 0 {
			break
		}
	}
	if j >= len(s) || j+1 >= len(s) || s[j+1] != '(' {
		return "", "", 0, false
	}
	k := strings.IndexByte(s[j+2:], ')')
	if k < 0 {
		return "", "", 0, false
	}
	url = strings.TrimSpace(s[j+2 : j+2+k])
	if u, _, found := strings.Cut(url, " \""); found {
		url = u // drop a "title"
	}
	url = strings.Trim(url, "<>")
	if url == "" || strings.ContainsAny(url, " \n") {
		return "", "", 0, false
	}
	return s[i+1 : j], url, j + 2 + k + 1, true
}
