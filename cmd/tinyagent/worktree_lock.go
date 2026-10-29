package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Concurrent "git worktree add" runs in one repository race: each scans
// .git/worktrees while the other is still writing its entry, and fails with
// "failed to read .git/worktrees/<name>/commondir". Setup therefore holds an
// exclusive lock file under .tinyagent/worktrees while git changes the
// worktree list, which serializes runs in one process and across processes.

const worktreeLockName = ".setup.lock"

// worktreeLockStale is how old a lock must be before it is taken to be left
// by a crashed run; setup itself takes seconds.
const worktreeLockStale = 10 * time.Minute

var worktreeLockWait = 2 * time.Minute

// lockWorktrees takes the setup lock in parent and returns its release.
func lockWorktrees(parent string) (func(), error) {
	p := filepath.Join(parent, worktreeLockName)
	deadline := time.Now().Add(worktreeLockWait)
	for {
		f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err == nil {
			fmt.Fprintf(f, "%d\n", os.Getpid())
			f.Close()
			return func() { os.Remove(p) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if fi, serr := os.Stat(p); serr == nil && time.Since(fi.ModTime()) > worktreeLockStale {
			os.Remove(p)
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("another run holds %s; remove it if no tinyagent is setting up a worktree", p)
		}
		time.Sleep(25 * time.Millisecond)
	}
}
