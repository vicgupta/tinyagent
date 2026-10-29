package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWorktreeLock(t *testing.T) {
	dir := t.TempDir()
	unlock, err := lockWorktrees(dir)
	if err != nil {
		t.Fatal(err)
	}
	old := worktreeLockWait
	worktreeLockWait = 100 * time.Millisecond
	defer func() { worktreeLockWait = old }()
	if _, err := lockWorktrees(dir); err == nil || !strings.Contains(err.Error(), "another run holds") {
		t.Fatalf("second lock: %v", err)
	}
	unlock()
	unlock2, err := lockWorktrees(dir)
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	// A lock left by a crashed run is taken over once stale.
	stale := time.Now().Add(-2 * worktreeLockStale)
	os.Chtimes(filepath.Join(dir, worktreeLockName), stale, stale)
	unlock3, err := lockWorktrees(dir)
	if err != nil {
		t.Fatalf("stale lock: %v", err)
	}
	unlock3()
	unlock2() // releasing twice is harmless
	if _, err := os.Stat(filepath.Join(dir, worktreeLockName)); !os.IsNotExist(err) {
		t.Errorf("lock left behind: %v", err)
	}
}
