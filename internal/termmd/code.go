package termmd

import (
	"strings"
	"unicode"
)

// code renders a fenced block: a border with the language label on top,
// a bar before each line, and simple highlighting when color is on. Code
// lines are never wrapped.
func (r *Renderer) code(lines []string) string {
	pad := strings.Repeat(" ", min(r.fenceInd, 16))
	var b strings.Builder
	top := r.glyph("╭─", "+--")
	if r.lang != "" {
		top += " " + r.lang
	}
	b.WriteString(pad + r.paint(top, stDim, "") + "\n")
	bar := r.paint(r.glyph("│", "|"), stDim, "")
	lang := langAlias(r.lang)
	for _, l := range lines {
		l = strings.ReplaceAll(stripIndent(l, r.fenceInd), "\t", "    ")
		if r.opt.Color {
			l = highlight(l, lang)
		}
		b.WriteString(strings.TrimRight(pad+bar+" "+l, " ") + "\n")
	}
	b.WriteString(pad + r.paint(r.glyph("╰─", "+--"), stDim, "") + "\n")
	return b.String()
}

func stripIndent(l string, n int) string {
	for n > 0 && strings.HasPrefix(l, " ") {
		l, n = l[1:], n-1
	}
	return l
}

func langAlias(l string) string {
	switch strings.ToLower(l) {
	case "go", "golang":
		return "go"
	case "py", "python", "python3":
		return "python"
	case "js", "javascript", "ts", "typescript", "jsx", "tsx", "mjs":
		return "js"
	case "sh", "bash", "shell", "zsh", "console", "fish":
		return "sh"
	case "rs", "rust":
		return "rust"
	case "c", "h", "cpp", "c++", "cc", "java", "cs", "csharp", "kotlin", "kt", "swift":
		return "c"
	case "json", "yaml", "yml", "toml":
		return "data"
	case "diff", "patch":
		return "diff"
	}
	return ""
}

var keywords = map[string]map[string]bool{
	"go":     set("break case chan const continue default defer else fallthrough for func go goto if import interface map package range return select struct switch type var nil true false"),
	"python": set("and as assert async await break class continue def del elif else except finally for from global if import in is lambda None nonlocal not or pass raise return True False try while with yield"),
	"js":     set("async await break case catch class const continue default delete do else export extends false finally for from function if import in instanceof interface let new null return switch this throw true try type typeof undefined var void while yield"),
	"sh":     set("if then else elif fi for while until do done case esac in function return export local"),
	"rust":   set("as async await break const continue crate else enum false fn for if impl in let loop match mod move mut pub ref return self Self static struct super trait true type unsafe use where while"),
	"c":      set("auto break case catch char class const continue default do double else enum extern false final float for if import int long new null nullptr package private protected public return short signed static struct switch this throw true try typedef union unsigned using var void volatile while"),
	"data":   set("true false null"),
}

func set(s string) map[string]bool {
	m := map[string]bool{}
	for _, f := range strings.Fields(s) {
		m[f] = true
	}
	return m
}

func paintCode(code, text string) string { return "\x1b[" + code + "m" + text + "\x1b[0m" }

// highlight colors one code line: keywords, strings, numbers and
// comments. It is a line-at-a-time lexer, so multi-line strings and block
// comments aren't tracked; unknown languages are left as they are.
func highlight(l, lang string) string {
	switch lang {
	case "":
		return l
	case "diff":
		switch {
		case strings.HasPrefix(l, "+++"), strings.HasPrefix(l, "---"):
			return paintCode("1", l)
		case strings.HasPrefix(l, "+"):
			return paintCode("32", l)
		case strings.HasPrefix(l, "-"):
			return paintCode("31", l)
		case strings.HasPrefix(l, "@@"):
			return paintCode("36", l)
		}
		return l
	}
	kw := keywords[lang]
	var b strings.Builder
	rs := []rune(l)
	for i := 0; i < len(rs); {
		c := rs[i]
		rest := string(rs[i:])
		switch {
		case (lang == "go" || lang == "js" || lang == "rust" || lang == "c") && strings.HasPrefix(rest, "//"),
			(lang == "python" || lang == "sh" || lang == "data") && c == '#' && (i == 0 || unicode.IsSpace(rs[i-1])):
			b.WriteString(paintCode("2", rest))
			return b.String()
		case c == '"' || c == '\'' || (c == '`' && (lang == "go" || lang == "js")):
			j := i + 1
			for j < len(rs) && rs[j] != c {
				if rs[j] == '\\' {
					j++
				}
				j++
			}
			j = min(j+1, len(rs))
			b.WriteString(paintCode("32", string(rs[i:j])))
			i = j
			continue
		case unicode.IsDigit(c) && (i == 0 || !isIdent(rs[i-1])):
			j := i
			for j < len(rs) && (isIdent(rs[j]) || rs[j] == '.') {
				j++
			}
			b.WriteString(paintCode("33", string(rs[i:j])))
			i = j
			continue
		case isIdent(c):
			j := i
			for j < len(rs) && isIdent(rs[j]) {
				j++
			}
			w := string(rs[i:j])
			if kw[w] {
				w = paintCode("35", w)
			}
			b.WriteString(w)
			i = j
			continue
		}
		b.WriteRune(c)
		i++
	}
	return b.String()
}

func isIdent(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }
