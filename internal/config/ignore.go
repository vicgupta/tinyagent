package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// IgnoreFile is the ignore file tinyagent keeps in its own state folder,
// relative to --cwd. It keeps an untracked ProjectFile out of `git add .`
// without touching the repository's own .gitignore. A tracked file is not
// affected: Git does not apply ignore rules to files it already tracks.
const IgnoreFile = ".tinyagent/.gitignore"

// ignoreRule is the line IgnoreFile needs; it is relative to .tinyagent/.
const ignoreRule = "/tinyagent.yaml"

const ignoreHeader = "# Added by tinyagent: an untracked tinyagent.yaml is local configuration. This does not apply to a tracked file.\n"

// excludeRule is the pattern appended to .git/info/exclude. It has no
// slash, so it matches an untracked tinyagent.yaml in any directory (the
// old ./tinyagent.yaml, .tinyagent/tinyagent.yaml, and a copy elsewhere).
const excludeRule = "tinyagent.yaml"

const excludeHeader = "# Added by tinyagent: an untracked tinyagent.yaml is local configuration. This does not apply to a tracked file.\n"

// EnsureIgnored makes sure <cwd>/.tinyagent/.gitignore ignores an untracked
// tinyagent.yaml, so broad staging such as `git add .` leaves that file
// out. It creates the file when missing and appends the rule when absent.
// A file that already mentions tinyagent.yaml (including a `!` re-include)
// or ignores everything (`*`) is left as it is, and nothing is ever
// removed. It does nothing when .tinyagent/ does not exist, and refuses a
// symlinked .tinyagent/ or .gitignore. changed reports a write.
//
// The rule does not stop `git add` from staging a tinyagent.yaml that is
// already tracked.
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
	return writeIgnoreRule(filepath.Join(cwd, IgnoreFile), ignoreHeader, ignoreRule)
}

// EnsureGitExclude appends excludeRule to the work tree's .git/info/exclude
// when cwd is inside a Git repository, so `git add .` and `git add -A`
// leave an untracked tinyagent.yaml out, including the old ./tinyagent.yaml
// at the top of cwd and one under .tinyagent/. It does nothing when cwd is
// not a work tree or git is not on PATH, and it never creates a repository.
// The repository's own .gitignore is not edited. The same "already decided"
// rules as EnsureIgnored apply, a symlinked info directory or exclude file
// is refused, and nothing is removed.
//
// This does not protect a tracked tinyagent.yaml. Ignore rules do not apply
// to files Git already tracks, and tinyagent does not claim that they do.
func EnsureGitExclude(cwd string) (changed bool, err error) {
	if cwd == "" {
		cwd = "."
	}
	common, ok := gitCommonDir(cwd)
	if !ok {
		return false, nil
	}
	info := filepath.Join(common, "info")
	fi, err := os.Lstat(info)
	if errors.Is(err, fs.ErrNotExist) {
		parent, perr := os.Lstat(common)
		if perr != nil {
			return false, perr
		}
		if parent.Mode()&os.ModeSymlink != 0 || !parent.IsDir() {
			return false, fmt.Errorf("%s is not a directory (symlinks are refused)", common)
		}
		if err := os.Mkdir(info, 0o755); err != nil {
			return false, err
		}
	} else if err != nil {
		return false, err
	} else if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return false, fmt.Errorf("%s is not a directory (symlinks are refused)", info)
	}
	return writeIgnoreRule(filepath.Join(info, "exclude"), excludeHeader, excludeRule)
}

// writeIgnoreRule creates path with header+rule, or appends them when the
// file exists and does not already decide about tinyagent.yaml. A symlink
// is refused and the target is not written.
func writeIgnoreRule(path, header, rule string) (changed bool, err error) {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		parent := filepath.Dir(path)
		pfi, err := os.Lstat(parent)
		if err != nil {
			return false, err
		}
		if pfi.Mode()&os.ModeSymlink != 0 || !pfi.IsDir() {
			return false, fmt.Errorf("%s is not a directory (symlinks are refused)", parent)
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return false, err
		}
		_, werr := f.WriteString(header + rule + "\n")
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
	add := header + rule + "\n"
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

// ignoreCovers reports whether an existing ignore file already decides
// about tinyagent.yaml: it names the file (in any form, including a
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

// gitCommonDir returns the common Git directory of the work tree containing
// cwd. ok is false when git is missing or cwd is not a work tree.
func gitCommonDir(cwd string) (string, bool) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", false
	}
	cmd := exec.Command("git", "-C", cwd, "rev-parse", "--git-common-dir")
	cmd.Env = gitCommandEnv()
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	p := strings.TrimSpace(string(out))
	if p == "" {
		return "", false
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(cwd, p)
	}
	return filepath.Clean(p), true
}

// gitCommandEnv is the process environment without variables that would
// point Git at a different repository.
func gitCommandEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		switch name {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY":
			continue
		}
		env = append(env, kv)
	}
	return env
}
