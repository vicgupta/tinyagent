package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// IgnoreFile is the ignore file tinyagent keeps in its own state folder,
// relative to --cwd. It keeps ProjectFile out of `git add .` without
// touching the repository's own .gitignore.
const IgnoreFile = ".tinyagent/.gitignore"

// ignoreRule is the line IgnoreFile needs; it is relative to .tinyagent/.
const ignoreRule = "/tinyagent.yaml"

const ignoreHeader = "# Added by tinyagent: tinyagent.yaml is local configuration; never commit it.\n"

// EnsureIgnored makes sure <cwd>/.tinyagent/.gitignore ignores
// tinyagent.yaml, so broad staging such as `git add .` leaves the local
// config out. It creates the file when missing and appends the rule when
// absent. A file that already mentions tinyagent.yaml (including a `!`
// re-include) or ignores everything (`*`) is left as it is, and nothing is
// ever removed. It does nothing when .tinyagent/ does not exist, and
// refuses a symlinked .tinyagent/ or .gitignore. changed reports a write.
func EnsureIgnored(cwd string) (changed bool, err error) {
	if cwd == "" {
		cwd = "."
	}
	dir := filepath.Join(cwd, filepath.Dir(ProjectFile))
	fi, err := os.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return false, fmt.Errorf("%s is not a directory (symlinks are refused)", dir)
	}
	path := filepath.Join(cwd, IgnoreFile)
	fi, err = os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return false, err
		}
		_, werr := f.WriteString(ignoreHeader + ignoreRule + "\n")
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		return werr == nil, werr
	}
	if err != nil {
		return false, err
	}
	if !fi.Mode().IsRegular() {
		return false, fmt.Errorf("%s is not a regular file (symlinks are refused)", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	if ignoreCovers(string(b)) {
		return false, nil
	}
	add := ignoreHeader + ignoreRule + "\n"
	if len(b) > 0 && b[len(b)-1] != '\n' {
		add = "\n" + add
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return false, err
	}
	_, werr := f.WriteString(add)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	return werr == nil, werr
}

// ignoreCovers reports whether an existing .tinyagent/.gitignore already
// decides about tinyagent.yaml: it names the file (in any form, including a
// negation the user wrote on purpose) or ignores everything.
func ignoreCovers(content string) bool {
	for _, l := range strings.Split(content, "\n") {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "#") {
			continue
		}
		if l == "*" || l == "/*" || strings.Contains(l, "tinyagent.yaml") {
			return true
		}
	}
	return false
}
