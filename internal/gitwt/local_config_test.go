package gitwt

import (
	"path/filepath"
	"strings"
	"testing"
)

// tinyagent's own commits (worker snapshots and worker commits) never
// include a tinyagent.yaml: not .tinyagent/tinyagent.yaml, not the old root
// file, not one in a subdirectory, even when nothing ignores them.
func TestLocalConfigNeverCommitted(t *testing.T) {
	dir := testRepo(t)
	for _, p := range []string{".tinyagent/tinyagent.yaml", "tinyagent.yaml", "sub/.tinyagent/tinyagent.yaml"} {
		write(t, dir, p, "model: openai/gpt-4o\n")
	}
	write(t, dir, "new.txt", "untracked\n")
	snap, err := Snapshot(dir)
	if err != nil {
		t.Fatal(err)
	}
	files := gitT(t, dir, "ls-tree", "-r", "--name-only", snap)
	if strings.Contains(files, "tinyagent.yaml") || !strings.Contains(files, "new.txt") {
		t.Fatalf("snapshot files:\n%s", files)
	}
	wt, branch, err := Create(dir, "cfg-w1", snap)
	if err != nil {
		t.Fatal(err)
	}
	defer Remove(dir, wt, branch)
	write(t, wt, ".tinyagent/tinyagent.yaml", "model: openai/gpt-4o-mini\n")
	write(t, wt, "tinyagent.yaml", "step_limit: 3\n")
	write(t, wt, "work.txt", "done\n")
	commit, changed, err := Commit(wt, "worker")
	if err != nil || !changed {
		t.Fatalf("Commit = %v, %v", changed, err)
	}
	files = gitT(t, dir, "ls-tree", "-r", "--name-only", commit)
	if strings.Contains(files, "tinyagent.yaml") || !strings.Contains(files, "work.txt") {
		t.Fatalf("worker commit files:\n%s", files)
	}
	// Only a config change: nothing to commit.
	write(t, wt, ".tinyagent/tinyagent.yaml", "model: openai/gpt-4o\n")
	if _, changed, err := Commit(wt, "worker 2"); err != nil || changed {
		t.Fatalf("config-only Commit = %v, %v", changed, err)
	}
	// The user's files are left in place.
	for _, p := range []string{".tinyagent/tinyagent.yaml", "tinyagent.yaml"} {
		if read(t, filepath.Join(dir, p)) != "model: openai/gpt-4o\n" {
			t.Errorf("%s changed", p)
		}
	}
}

// A tinyagent.yaml already tracked in HEAD stays in history untouched: the
// snapshot keeps the committed copy and local edits are not staged.
func TestTrackedLocalConfigNotRestaged(t *testing.T) {
	dir := testRepo(t)
	write(t, dir, ".tinyagent/tinyagent.yaml", "step_limit: 1\n")
	gitT(t, dir, "add", "-f", ".tinyagent/tinyagent.yaml")
	gitT(t, dir, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "tracked")
	write(t, dir, ".tinyagent/tinyagent.yaml", "step_limit: 2\n")
	snap, err := Snapshot(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := gitT(t, dir, "show", snap+":.tinyagent/tinyagent.yaml"); got != "step_limit: 1" {
		t.Errorf("snapshot copy = %q, want the committed one", got)
	}
	if got := read(t, filepath.Join(dir, ".tinyagent/tinyagent.yaml")); got != "step_limit: 2\n" {
		t.Errorf("working copy = %q", got)
	}
}
