// Package gitwt holds the Git plumbing for worker subagents: snapshotting
// a working tree into a commit without touching the index, creating a
// linked worktree on its own branch, committing a worker's changes, and
// applying them back to the parent's working tree atomically.
//
// Every command runs with -C <dir> and an environment stripped of the
// variables that would redirect git to another repository.
package gitwt

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Dir is where worker worktrees go, relative to the repository root. It is
// shared with --worktree.
const Dir = ".tinyagent/worktrees"

// BranchPrefix prefixes every worker branch.
const BranchPrefix = "tinyagent/"

// FallbackName and FallbackEmail are the identity used for tinyagent's
// branch-local commits when the repository has no user.name/user.email.
const (
	FallbackName  = "tinyagent"
	FallbackEmail = "tinyagent@localhost"
)

// Env is the environment for tinyagent's own git commands: variables that
// would point git at a different repository are dropped. extra is appended.
func Env(extra ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		switch name {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY":
			continue
		}
		env = append(env, kv)
	}
	return append(env, extra...)
}

// run runs git in dir with env and returns trimmed stdout, or an error
// holding its stderr.
func run(dir string, env []string, args ...string) (string, error) {
	out, err := runRaw(dir, env, args...)
	return strings.TrimSpace(out), err
}

func runRaw(dir string, env []string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = env
	if cmd.Env == nil {
		cmd.Env = Env()
	}
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errOut.String())
		if msg == "" {
			msg = err.Error()
		}
		return out.String(), errors.New(msg)
	}
	return out.String(), nil
}

// Top returns the root of the Git work tree containing dir, with symlinks
// resolved.
func Top(dir string) (string, error) {
	if _, err := exec.LookPath("git"); err != nil {
		return "", errors.New("git is not on PATH")
	}
	top, err := run(dir, nil, "rev-parse", "--show-toplevel")
	if err != nil || top == "" {
		return "", fmt.Errorf("%s is not inside a Git work tree", dir)
	}
	if r, err := filepath.EvalSymlinks(top); err == nil {
		top = r
	}
	return filepath.Clean(top), nil
}

// Head returns the commit at HEAD, or an error when the repository has no
// commits.
func Head(top string) (string, error) {
	h, err := run(top, nil, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	if err != nil || h == "" {
		return "", fmt.Errorf("the repository at %s has no commits", top)
	}
	return h, nil
}

// identity returns environment entries that set the committer and author
// when the repository has no configured identity.
func identity(dir string) []string {
	name, _ := run(dir, nil, "config", "user.name")
	email, _ := run(dir, nil, "config", "user.email")
	var env []string
	if name == "" {
		env = append(env, "GIT_AUTHOR_NAME="+FallbackName, "GIT_COMMITTER_NAME="+FallbackName)
	}
	if email == "" {
		env = append(env, "GIT_AUTHOR_EMAIL="+FallbackEmail, "GIT_COMMITTER_EMAIL="+FallbackEmail)
	}
	return env
}

// Snapshot records the working tree of the repository at top (tracked
// changes plus untracked, non-ignored files, excluding .tinyagent/worktrees)
// as a commit whose parent is HEAD. It uses a temporary index, so the
// repository's index, HEAD and refs are untouched.
func Snapshot(top string) (string, error) {
	head, err := Head(top)
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp("", "tinyagent-index-")
	if err != nil {
		return "", err
	}
	idx := f.Name()
	f.Close()
	os.Remove(idx) // git creates it; an empty file is not a valid index
	defer os.Remove(idx)
	env := Env("GIT_INDEX_FILE=" + idx)
	if _, err := run(top, env, "read-tree", head); err != nil {
		return "", fmt.Errorf("snapshot: %w", err)
	}
	if _, err := run(top, env, "add", "-A", "--", ".", ":(exclude)"+Dir); err != nil {
		return "", fmt.Errorf("snapshot: %w", err)
	}
	tree, err := run(top, env, "write-tree")
	if err != nil {
		return "", fmt.Errorf("snapshot: %w", err)
	}
	commit, err := run(top, Env(identity(top)...), "commit-tree", tree, "-p", head, "-m", "tinyagent worker base: snapshot of the working tree")
	if err != nil {
		return "", fmt.Errorf("snapshot: %w", err)
	}
	return commit, nil
}

// Create adds a linked worktree at <top>/.tinyagent/worktrees/<name> on a
// new branch tinyagent/<name> at commit, with repository hooks disabled.
// On error nothing new is left behind except the ignored parent directory.
func Create(top, name, commit string) (path, branch string, err error) {
	parent := filepath.Join(top, Dir)
	for _, p := range []string{filepath.Join(top, ".tinyagent"), parent} {
		if fi, err := os.Lstat(p); err == nil && (fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir()) {
			return "", "", fmt.Errorf("%s is not a directory (symlinks are refused)", p)
		}
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", "", err
	}
	ignore := filepath.Join(parent, ".gitignore")
	if f, err := os.OpenFile(ignore, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644); err == nil {
		f.WriteString("*\n")
		f.Close()
	}
	path = filepath.Join(parent, name)
	branch = BranchPrefix + name
	if _, err := os.Lstat(path); err == nil {
		return "", "", fmt.Errorf("%s already exists", path)
	}
	if BranchExists(top, branch) {
		return "", "", fmt.Errorf("branch %s already exists", branch)
	}
	noHooks, err := os.MkdirTemp("", "tinyagent-nohooks-")
	if err != nil {
		return "", "", err
	}
	defer os.RemoveAll(noHooks)
	if _, err := run(top, nil, "-c", "core.hooksPath="+noHooks, "worktree", "add", "--quiet", "-b", branch, "--", path, commit); err != nil {
		Remove(top, path, branch)
		return "", "", fmt.Errorf("creating the worktree failed: %w", err)
	}
	if r, err := filepath.EvalSymlinks(path); err == nil {
		path = r
	}
	return path, branch, nil
}

// BranchExists reports whether refs/heads/<branch> exists.
func BranchExists(top, branch string) bool {
	_, err := run(top, nil, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	return err == nil
}

// Remove deletes a worktree and its branch, ignoring errors.
func Remove(top, path, branch string) {
	run(top, nil, "worktree", "remove", "--force", "--", path)
	os.RemoveAll(path)
	run(top, nil, "worktree", "prune")
	if BranchExists(top, branch) {
		run(top, nil, "branch", "-D", "--", branch)
	}
}

// Commit stages every change in the worktree at path (respecting
// .gitignore) and commits it with message, with repository hooks disabled.
// changed is false, and no commit is made, when there is nothing to commit.
func Commit(path, message string) (commit string, changed bool, err error) {
	if _, err := run(path, nil, "add", "-A", "--", "."); err != nil {
		return "", false, fmt.Errorf("staging the worker's changes: %w", err)
	}
	if _, err := run(path, nil, "diff", "--cached", "--quiet"); err == nil {
		return "", false, nil
	}
	noHooks, err := os.MkdirTemp("", "tinyagent-nohooks-")
	if err != nil {
		return "", false, err
	}
	defer os.RemoveAll(noHooks)
	if _, err := run(path, Env(identity(path)...), "-c", "core.hooksPath="+noHooks, "-c", "commit.gpgsign=false",
		"commit", "--quiet", "--no-verify", "-m", message); err != nil {
		return "", false, fmt.Errorf("committing the worker's changes: %w", err)
	}
	c, err := run(path, nil, "rev-parse", "HEAD")
	return c, err == nil, err
}

// DiffStat returns `git diff --stat base commit` and its one-line summary.
func DiffStat(top, base, commit string) (stat, short string) {
	stat, _ = run(top, nil, "diff", "--stat", base, commit)
	short, _ = run(top, nil, "diff", "--shortstat", base, commit)
	return stat, short
}

// ChangedFiles lists the paths, relative to top and slash separated, that
// base..commit adds, changes or deletes (renames count as both paths).
func ChangedFiles(top, base, commit string) ([]string, error) {
	out, err := run(top, nil, "diff", "--name-only", "--no-renames", "-z", base, commit)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, f := range strings.Split(out, "\x00") {
		if f = strings.TrimSpace(f); f != "" {
			files = append(files, f)
		}
	}
	return files, nil
}

var conflictRE = regexp.MustCompile(`(?m)^error: (?:patch failed: (.+):\d+|(.+): (?:already exists in working directory|does not exist in working directory|does not match index|patch does not apply))$`)

// Apply applies base..commit to the working tree at top (not the index).
// It is atomic: when any hunk fails, nothing is changed and conflicts
// lists the affected paths.
func Apply(top, base, commit string) (conflicts []string, err error) {
	patch, err := runRaw(top, nil, "diff", "--binary", "--no-renames", base, commit)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(patch) == "" {
		return nil, nil
	}
	f, err := os.CreateTemp("", "tinyagent-worker-*.patch")
	if err != nil {
		return nil, err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(patch); err != nil {
		f.Close()
		return nil, err
	}
	f.Close()
	if _, err := run(top, nil, "apply", "--check", "--", f.Name()); err != nil {
		seen := map[string]bool{}
		for _, m := range conflictRE.FindAllStringSubmatch(err.Error(), -1) {
			p := m[1]
			if p == "" {
				p = m[2]
			}
			if !seen[p] {
				seen[p] = true
				conflicts = append(conflicts, p)
			}
		}
		sort.Strings(conflicts)
		if len(conflicts) == 0 {
			return nil, fmt.Errorf("the change does not apply: %w", err)
		}
		return conflicts, nil
	}
	if _, err := run(top, nil, "apply", "--", f.Name()); err != nil {
		return nil, fmt.Errorf("applying the change failed: %w", err)
	}
	return nil, nil
}
