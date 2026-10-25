package snapshot

import (
	"bufio"
	"bytes"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// ignorer applies .gitignore rules the way Git does for the common cases:
// nested .gitignore files, .git/info/exclude, negation, "**", directory-only
// and anchored patterns. Paths are slash-separated and relative to the
// repository root. The global core.excludesFile is not read.
type ignorer struct {
	repo  string // repository root (absolute, OS path)
	rules []rule
}

type rule struct {
	base    string // directory of the .gitignore, relative to repo ("" for the top)
	re      *regexp.Regexp
	negate  bool
	dirOnly bool
	name    bool // pattern without a slash: matches the base name at any depth
}

// newIgnorer returns the ignorer for root, or nil when root is not inside a
// Git work tree (in which case nothing is treated as ignored). It loads
// .git/info/exclude and the .gitignore files from the repository root down
// to root; nested ones under root are added with load as the walk reaches
// them.
func newIgnorer(root string) *ignorer {
	repo := ""
	for dir := root; ; {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			repo = dir
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil
		}
		dir = parent
	}
	ig := &ignorer{repo: repo}
	if fi, err := os.Stat(filepath.Join(repo, ".git")); err == nil && fi.IsDir() {
		ig.addFile(filepath.Join(repo, ".git", "info", "exclude"), "")
	}
	rel, err := filepath.Rel(repo, root)
	if err != nil {
		return ig
	}
	rel = filepath.ToSlash(rel)
	dir := ""
	ig.load(dir)
	if rel != "." {
		for _, part := range strings.Split(rel, "/") {
			dir = path.Join(dir, part)
			ig.load(dir)
		}
	}
	return ig
}

// load adds the .gitignore in dir (relative to the repository), if any.
func (ig *ignorer) load(dir string) {
	ig.addFile(filepath.Join(ig.repo, filepath.FromSlash(dir), ".gitignore"), dir)
}

func (ig *ignorer) addFile(file, base string) {
	data, err := os.ReadFile(file)
	if err != nil {
		return
	}
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		if r, ok := parseRule(sc.Text(), base); ok {
			ig.rules = append(ig.rules, r)
		}
	}
}

func parseRule(line, base string) (rule, bool) {
	line = strings.TrimRight(line, "\r")
	if !strings.HasSuffix(line, `\ `) {
		line = strings.TrimRight(line, " \t")
	}
	if line == "" || strings.HasPrefix(line, "#") {
		return rule{}, false
	}
	r := rule{base: base}
	if strings.HasPrefix(line, "!") {
		r.negate = true
		line = line[1:]
	} else if strings.HasPrefix(line, `\!`) || strings.HasPrefix(line, `\#`) {
		line = line[1:]
	}
	if strings.HasSuffix(line, "/") {
		r.dirOnly = true
		line = strings.TrimRight(line, "/")
	}
	if line == "" {
		return rule{}, false
	}
	if strings.Contains(line, "/") {
		line = strings.TrimPrefix(line, "/")
	} else {
		r.name = true
	}
	re, err := regexp.Compile("^" + globRegexp(line) + "$")
	if err != nil {
		return rule{}, false
	}
	r.re = re
	return r, true
}

// globRegexp translates a gitignore glob to a regular expression body.
func globRegexp(p string) string {
	var b strings.Builder
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case strings.HasPrefix(p[i:], "**/"):
			b.WriteString("(?:.*/)?")
			i += 2
		case strings.HasPrefix(p[i:], "/**") && i+3 == len(p):
			b.WriteString("/.*")
			i += 2
		case strings.HasPrefix(p[i:], "**"):
			b.WriteString(".*")
			i++
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		case c == '[':
			end := strings.IndexByte(p[i+1:], ']')
			if end < 0 {
				b.WriteString(`\[`)
				continue
			}
			class := p[i+1 : i+1+end]
			if strings.HasPrefix(class, "!") {
				class = "^" + class[1:]
			}
			b.WriteString("[" + strings.ReplaceAll(class, `\`, `\\`) + "]")
			i += end + 1
		case c == '\\' && i+1 < len(p):
			i++
			b.WriteString(regexp.QuoteMeta(string(p[i])))
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	return b.String()
}

// ignored reports whether rel (relative to the repository) is ignored. The
// last matching rule wins.
func (ig *ignorer) ignored(rel string, isDir bool) bool {
	if ig == nil {
		return false
	}
	out := false
	for _, r := range ig.rules {
		if r.dirOnly && !isDir {
			continue
		}
		sub := rel
		if r.base != "" {
			if !strings.HasPrefix(rel, r.base+"/") {
				continue
			}
			sub = rel[len(r.base)+1:]
		}
		if r.name {
			sub = path.Base(sub)
		}
		if r.re.MatchString(sub) {
			out = !r.negate
		}
	}
	return out
}
