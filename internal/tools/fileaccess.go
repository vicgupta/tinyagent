package tools

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
)

// FileAccess is the compiled file_access policy for the built-in file tools:
// gitignore-style deny_read and deny_write patterns relative to Root. A nil
// *FileAccess denies nothing. deny_read paths are also write-denied.
//
// It covers only tinyagent's in-process file tools (read_file, write_file,
// edit_file, apply_patch, list_files, grep, view_image). bash,
// start_process, hooks and MCP servers are not affected.
type FileAccess struct {
	root      string // canonical root
	realRoot  string // root with symlinks resolved
	denyRead  []accessRule
	denyWrite []accessRule
	fold      bool
}

type accessRule struct {
	pattern  string   // as configured, for messages
	segs     []string // pattern split on "/", without anchor or trailing "/"
	anchored bool     // contains a "/" before the end: matched from root
	dirOnly  bool     // trailing "/": matches directories only
}

// DeniedError is returned for a call that touches a denied path.
type DeniedError struct {
	Path    string // the path as the caller named it
	List    string // "deny_read" or "deny_write"
	Pattern string // the rule that matched, as configured
}

func (e *DeniedError) Error() string {
	return fmt.Sprintf("%s is denied by file_access.%s rule %q", e.Path, e.List, e.Pattern)
}

// foldCase reports whether file_access matching ignores case: on macOS and
// Windows, whose default filesystems are case-insensitive.
func foldCase() bool { return runtime.GOOS == "darwin" || runtime.GOOS == "windows" }

// ValidateAccessPattern reports whether p is a supported file_access
// pattern: non-empty, no "!" negation, and valid glob syntax.
func ValidateAccessPattern(p string) error {
	_, err := compileRule(p, false)
	return err
}

func compileRule(p string, fold bool) (accessRule, error) {
	r := accessRule{pattern: p}
	s := strings.TrimSpace(p)
	if s == "" {
		return r, errors.New("empty pattern")
	}
	if strings.HasPrefix(s, "!") {
		return r, fmt.Errorf("pattern %q: ! negation is not supported", p)
	}
	s = filepath.ToSlash(s)
	if strings.HasSuffix(s, "/") {
		r.dirOnly = true
		s = strings.TrimRight(s, "/")
	}
	if strings.HasPrefix(s, "/") {
		r.anchored = true
		s = strings.TrimLeft(s, "/")
	}
	if strings.Contains(s, "/") {
		r.anchored = true
	}
	if s == "" {
		return r, fmt.Errorf("pattern %q matches nothing", p)
	}
	if fold {
		s = strings.ToLower(s)
	}
	for _, seg := range strings.Split(s, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return r, fmt.Errorf("pattern %q: empty, . or .. path segment", p)
		}
		if seg != "**" {
			if _, err := path.Match(seg, ""); err != nil {
				return r, fmt.Errorf("pattern %q: %w", p, err)
			}
		}
		r.segs = append(r.segs, seg)
	}
	return r, nil
}

// NewFileAccess compiles the patterns for root. It returns nil, and no
// error, when both lists are empty.
func NewFileAccess(root string, denyRead, denyWrite []string) (*FileAccess, error) {
	if len(denyRead) == 0 && len(denyWrite) == 0 {
		return nil, nil
	}
	f := &FileAccess{root: filepath.Clean(root), fold: foldCase()}
	f.realRoot = f.root
	if r, err := filepath.EvalSymlinks(f.root); err == nil {
		f.realRoot = r
	}
	for _, list := range []struct {
		name string
		in   []string
		out  *[]accessRule
	}{{"deny_read", denyRead, &f.denyRead}, {"deny_write", denyWrite, &f.denyWrite}} {
		for _, p := range list.in {
			r, err := compileRule(p, f.fold)
			if err != nil {
				return nil, fmt.Errorf("file_access.%s: %w", list.name, err)
			}
			*list.out = append(*list.out, r)
		}
	}
	return f, nil
}

// Counts returns the number of deny_read and deny_write rules.
func (f *FileAccess) Counts() (read, write int) {
	if f == nil {
		return 0, 0
	}
	return len(f.denyRead), len(f.denyWrite)
}

// Rules returns the configured deny_read and deny_write patterns.
func (f *FileAccess) Rules() (read, write []string) {
	if f == nil {
		return nil, nil
	}
	for _, r := range f.denyRead {
		read = append(read, r.pattern)
	}
	for _, r := range f.denyWrite {
		write = append(write, r.pattern)
	}
	return read, write
}

// CheckRead returns a *DeniedError when full (an absolute path under the
// root, as returned by Resolve) or its symlink-resolved target matches a
// deny_read rule. name is the path as the caller gave it.
func (f *FileAccess) CheckRead(full, name string) error {
	if f == nil {
		return nil
	}
	if p := f.match(full, f.denyRead); p != "" {
		return &DeniedError{Path: name, List: "deny_read", Pattern: p}
	}
	return nil
}

// CheckWrite is CheckRead for writes: deny_read rules apply too.
func (f *FileAccess) CheckWrite(full, name string) error {
	if err := f.CheckRead(full, name); err != nil || f == nil {
		return err
	}
	if p := f.match(full, f.denyWrite); p != "" {
		return &DeniedError{Path: name, List: "deny_write", Pattern: p}
	}
	return nil
}

// Hidden reports whether a walk should hide full: it matches a deny_read
// rule, directly or through its symlink target.
func (f *FileAccess) Hidden(full string) bool {
	return f != nil && f.match(full, f.denyRead) != ""
}

// match returns the first rule in rules matching full or its resolved
// target inside the root, or "".
func (f *FileAccess) match(full string, rules []accessRule) string {
	if len(rules) == 0 {
		return ""
	}
	full = filepath.Clean(full)
	if p := f.matchUnder(f.root, full, rules); p != "" {
		return p
	}
	if target, ok := resolveExisting(full); ok && target != full {
		for _, base := range []string{f.realRoot, f.root} {
			if p := f.matchUnder(base, target, rules); p != "" {
				return p
			}
		}
	}
	return ""
}

func (f *FileAccess) matchUnder(base, full string, rules []accessRule) string {
	r, err := filepath.Rel(base, full)
	if err != nil || r == "." || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return ""
	}
	r = filepath.ToSlash(r)
	if f.fold {
		r = strings.ToLower(r)
	}
	segs := strings.Split(r, "/")
	// The full path is a directory only if it is one on disk; every proper
	// prefix is a directory by construction. A rule matching any prefix
	// covers everything beneath it, as in gitignore.
	last := isDirNoFollow(full)
	for n := 1; n <= len(segs); n++ {
		dir := n < len(segs) || last
		for _, rule := range rules {
			if rule.matches(segs[:n], dir) {
				return rule.pattern
			}
		}
	}
	return ""
}

func isDirNoFollow(p string) bool {
	fi, err := os.Lstat(p)
	if err != nil {
		return false
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		fi, err = os.Stat(p)
		if err != nil {
			return false
		}
	}
	return fi.IsDir()
}

func (r accessRule) matches(segs []string, dir bool) bool {
	if r.dirOnly && !dir {
		return false
	}
	if !r.anchored {
		ok, _ := path.Match(r.segs[0], segs[len(segs)-1])
		return ok
	}
	return matchSegs(r.segs, segs)
}

// matchSegs matches path segments against pattern segments, where a "**"
// segment matches zero or more path segments.
func matchSegs(pat, segs []string) bool {
	if len(pat) == 0 {
		return len(segs) == 0
	}
	if pat[0] == "**" {
		for i := 0; i <= len(segs); i++ {
			if matchSegs(pat[1:], segs[i:]) {
				return true
			}
		}
		return false
	}
	if len(segs) == 0 {
		return false
	}
	if ok, _ := path.Match(pat[0], segs[0]); !ok {
		return false
	}
	return matchSegs(pat[1:], segs[1:])
}

// resolveExisting resolves symlinks in full. When full does not exist, it
// resolves its deepest existing ancestor and appends the rest, so a new file
// under a symlinked directory is checked against the directory's target.
func resolveExisting(full string) (string, bool) {
	rest := ""
	p := full
	for {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			if rest != "" {
				r = filepath.Join(r, rest)
			}
			return r, true
		}
		parent := filepath.Dir(p)
		if parent == p {
			return "", false
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
}

// hiddenNote is the neutral line ending walk output when n paths were
// hidden by file_access rules; it names none of them.
func hiddenNote(n int) string {
	if n == 1 {
		return "(1 path hidden by file_access rules)"
	}
	return fmt.Sprintf("(%d paths hidden by file_access rules)", n)
}

func withHiddenNote(out string, n int) string {
	if n == 0 {
		return out
	}
	if out == "" {
		return hiddenNote(n)
	}
	return out + "\n" + hiddenNote(n)
}
