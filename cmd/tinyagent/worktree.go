package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// worktreeFlag is --worktree: bare (a generated name), --worktree=<name>, or
// --worktree=false (off).
type worktreeFlag struct {
	on   bool
	name string
}

func (w *worktreeFlag) String() string   { return "" }
func (w *worktreeFlag) IsBoolFlag() bool { return true }
func (w *worktreeFlag) Set(v string) error {
	switch v {
	case "true":
		w.on, w.name = true, ""
	case "false":
		w.on, w.name = false, ""
	case "":
		return errors.New("empty name")
	default:
		w.on, w.name = true, v
	}
	return nil
}

// worktreeDir is where worktrees go, relative to the repository root.
const worktreeDir = ".tinyagent/worktrees"

// worktreeBranchPrefix prefixes every worktree's branch name.
const worktreeBranchPrefix = "tinyagent/"

var worktreeNameRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// validWorktreeName checks a --worktree name: [A-Za-z0-9._-], 1 to 64
// characters, not starting with "." or "-", and also a valid Git branch
// component (no "..", no trailing "." or ".lock").
func validWorktreeName(name string) error {
	if !worktreeNameRE.MatchString(name) || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "-") ||
		strings.Contains(name, "..") || strings.HasSuffix(name, ".") || strings.HasSuffix(name, ".lock") {
		return fmt.Errorf("--worktree name %q is invalid: use 1 to 64 of A-Z a-z 0-9 . _ -, not starting with . or -", name)
	}
	return nil
}

// generateWorktreeName returns a sortable, unique-enough name such as
// run-20261025-162512-ab12cd.
func generateWorktreeName(now time.Time) string {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		b = []byte{byte(now.UnixNano()), byte(now.UnixNano() >> 8), byte(now.UnixNano() >> 16)}
	}
	return "run-" + now.Format("20060102-150405") + "-" + hex.EncodeToString(b)
}

// gitEnv is the environment for tinyagent's own git commands: variables
// that would point git at a different repository are dropped, since every
// command names its repository with -C.
func gitEnv() []string {
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

// git runs git with args and returns its trimmed stdout, or an error that
// includes its stderr.
func git(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Env = gitEnv()
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errOut.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", errors.New(msg)
	}
	return strings.TrimSpace(out.String()), nil
}

// setupWorktree creates a linked worktree on a new branch tinyagent/<name>
// at the current HEAD of the repository containing root, and returns the
// run's new root (the worktree, plus root's subdirectory within the repo)
// and its trajectory record. It is all-or-nothing: on error nothing new is
// left behind. Every error is a setup problem (exit 2).
func setupWorktree(root, name string, stderr io.Writer, verbose bool) (string, *trajectory.WorktreeInfo, error) {
	if name == "" {
		name = generateWorktreeName(time.Now())
	}
	if err := validWorktreeName(name); err != nil {
		return "", nil, err
	}
	if _, err := exec.LookPath("git"); err != nil {
		return "", nil, errors.New("--worktree requires git on PATH")
	}
	top, err := git("-C", root, "rev-parse", "--show-toplevel")
	if err != nil || top == "" {
		return "", nil, fmt.Errorf("--worktree: --cwd %s is not inside a Git work tree", root)
	}
	if top, err = canonicalRoot(top); err != nil {
		return "", nil, fmt.Errorf("--worktree: %w", err)
	}
	rel, err := filepath.Rel(top, root)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", nil, fmt.Errorf("--worktree: --cwd %s is not inside the repository at %s", root, top)
	}
	base, err := git("-C", top, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	if err != nil || base == "" {
		return "", nil, fmt.Errorf("--worktree: the repository at %s has no commits", top)
	}
	// Refuse a symlinked .tinyagent or .tinyagent/worktrees rather than
	// create a worktree wherever it points.
	stateDir := filepath.Join(top, ".tinyagent")
	parent := filepath.Join(top, worktreeDir)
	for _, p := range []string{stateDir, parent} {
		if fi, err := os.Lstat(p); err == nil && (fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir()) {
			return "", nil, fmt.Errorf("--worktree: %s is not a directory (symlinks are refused)", p)
		}
	}
	path := filepath.Join(parent, name)
	if _, err := os.Lstat(path); err == nil {
		return "", nil, fmt.Errorf("--worktree: %s already exists; choose another name", path)
	}
	branch := worktreeBranchPrefix + name
	if _, err := git("-C", top, "show-ref", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
		return "", nil, fmt.Errorf("--worktree: branch %s already exists; choose another name", branch)
	}
	dirty, _ := git("-C", top, "status", "--porcelain", "--", ".", ":(exclude).tinyagent")

	// Ctrl-C during setup must leave either no worktree or a complete one,
	// so the interrupt is held until git is done.
	interrupted := make(chan os.Signal, 1)
	signal.Notify(interrupted, os.Interrupt)
	defer signal.Stop(interrupted)

	var created []string
	for _, p := range []string{stateDir, parent} {
		// Another run may create the same directory concurrently, so an
		// existing directory is fine (and isn't ours to remove).
		err := os.Mkdir(p, 0o755)
		if err == nil {
			created = append(created, p)
			continue
		}
		if fi, lerr := os.Lstat(p); errors.Is(err, os.ErrExist) && lerr == nil && fi.IsDir() {
			continue
		}
		removeAll(created)
		return "", nil, fmt.Errorf("--worktree: %w", err)
	}
	ignore := filepath.Join(parent, ".gitignore")
	if f, err := os.OpenFile(ignore, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644); err == nil {
		created = append(created, ignore)
		_, werr := f.WriteString("*\n")
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			removeAll(created)
			return "", nil, fmt.Errorf("--worktree: %w", werr)
		}
	} else if !errors.Is(err, os.ErrExist) {
		removeAll(created)
		return "", nil, fmt.Errorf("--worktree: %w", err)
	}
	rollback := func() {
		os.RemoveAll(path)
		git("-C", top, "worktree", "prune")
		if _, err := git("-C", top, "show-ref", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
			git("-C", top, "branch", "-D", "--", branch)
		}
		removeAll(created)
	}
	// Repository hooks (post-checkout) don't run during setup: an empty
	// hooks directory replaces the configured one.
	noHooks, err := os.MkdirTemp("", "tinyagent-nohooks-")
	if err != nil {
		removeAll(created)
		return "", nil, fmt.Errorf("--worktree: %w", err)
	}
	defer os.RemoveAll(noHooks)
	start := time.Now()
	_, err = git("-C", top, "-c", "core.hooksPath="+noHooks, "worktree", "add", "--quiet", "-b", branch, "--", path, base)
	if err == nil {
		select {
		case <-interrupted:
			// The worktree is complete; the run itself is not started.
			fmt.Fprintf(stderr, "worktree: %s (branch %s from %s) created, then interrupted\n", path, branch, shortCommit(base))
			return "", nil, errInterruptedSetup
		default:
		}
	}
	if err != nil {
		rollback()
		return "", nil, fmt.Errorf("--worktree: creating the worktree failed: %w", err)
	}
	wtPath, err := canonicalRoot(path)
	if err != nil {
		rollback()
		return "", nil, fmt.Errorf("--worktree: %w", err)
	}
	newRoot := filepath.Join(wtPath, rel)
	if fi, err := os.Stat(newRoot); err != nil || !fi.IsDir() {
		rollback()
		return "", nil, fmt.Errorf("--worktree: --cwd's subdirectory %s doesn't exist at HEAD (it may be untracked or empty)", rel)
	}
	if dirty != "" {
		fmt.Fprintln(stderr, dirtyWorktreeWarning)
	}
	fmt.Fprintf(stderr, "worktree: %s (branch %s from %s)\n", wtPath, branch, shortCommit(base))
	if verbose {
		fmt.Fprintf(stderr, "worktree setup took %s\n", formatDuration(time.Since(start)))
	}
	return newRoot, &trajectory.WorktreeInfo{Path: wtPath, Branch: branch, BaseCommit: base, RepoRoot: top}, nil
}

// errInterruptedSetup reports Ctrl-C during worktree setup.
var errInterruptedSetup = errors.New("interrupted during worktree setup")

func removeAll(paths []string) {
	for i := len(paths) - 1; i >= 0; i-- {
		os.Remove(paths[i])
	}
}

func shortCommit(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
