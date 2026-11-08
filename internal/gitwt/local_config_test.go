package gitwt

import (
	"errors"
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
	write(t, dir, "tinyagent.yaml.bak", "not the config\n")
	snap, err := Snapshot(dir)
	if err != nil {
		t.Fatal(err)
	}
	files := gitT(t, dir, "ls-tree", "-r", "--name-only", snap)
	if treeHasConfig(files) || !strings.Contains(files, "new.txt") || !strings.Contains(files, "tinyagent.yaml.bak") {
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
	if treeHasConfig(files) || !strings.Contains(files, "work.txt") || !strings.Contains(files, "tinyagent.yaml.bak") {
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

// A tinyagent.yaml already staged in the real index is not copied into the
// snapshot, and the index is left staged.
func TestSnapshotLeavesStagedConfigAlone(t *testing.T) {
	dir := testRepo(t)
	for _, p := range []string{".tinyagent/tinyagent.yaml", "tinyagent.yaml"} {
		write(t, dir, p, "step_limit: 1\n")
	}
	gitT(t, dir, "add", ".tinyagent/tinyagent.yaml", "tinyagent.yaml")
	gitT(t, dir, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "tracked")
	for _, p := range []string{".tinyagent/tinyagent.yaml", "tinyagent.yaml"} {
		write(t, dir, p, "step_limit: 2\n")
	}
	gitT(t, dir, "add", ".tinyagent/tinyagent.yaml", "tinyagent.yaml")
	head := gitT(t, dir, "rev-parse", "HEAD")
	staged := gitT(t, dir, "diff", "--cached", "--name-only")
	snap, err := Snapshot(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := gitT(t, dir, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD moved to %s", got)
	}
	if got := gitT(t, dir, "diff", "--cached", "--name-only"); got != staged {
		t.Errorf("index changed:\n%s\nwant:\n%s", got, staged)
	}
	for _, p := range []string{".tinyagent/tinyagent.yaml", "tinyagent.yaml"} {
		if got := gitT(t, dir, "show", snap+":"+p); got != "step_limit: 1" {
			t.Errorf("snapshot %s = %q", p, got)
		}
		if got := read(t, filepath.Join(dir, p)); got != "step_limit: 2\n" {
			t.Errorf("working copy %s = %q", p, got)
		}
	}
}

// Unstaged edits to a tracked tinyagent.yaml (root and .tinyagent/) stay out
// of the snapshot and of Commit. The HEAD blob is unchanged, the working
// copy is kept, and other files are still committed.
func TestTrackedLocalConfigEditsNotCommitted(t *testing.T) {
	dir := testRepo(t)
	paths := []string{".tinyagent/tinyagent.yaml", "tinyagent.yaml", "sub/tinyagent.yaml"}
	for _, p := range paths {
		write(t, dir, p, "step_limit: 1\n")
	}
	gitT(t, dir, "add", "-f", ".tinyagent/tinyagent.yaml", "tinyagent.yaml", "sub/tinyagent.yaml")
	gitT(t, dir, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "tracked")
	for _, p := range paths {
		write(t, dir, p, "step_limit: 2\n")
	}
	snap, err := Snapshot(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range paths {
		if got := gitT(t, dir, "show", snap+":"+p); got != "step_limit: 1" {
			t.Errorf("snapshot %s = %q, want the committed copy", p, got)
		}
	}
	if staged := gitT(t, dir, "diff", "--cached", "--name-only"); staged != "" {
		t.Errorf("snapshot staged:\n%s", staged)
	}
	write(t, dir, "work.txt", "done\n")
	commit, changed, err := Commit(dir, "worker")
	if err != nil || !changed {
		t.Fatalf("Commit = %v, %v", changed, err)
	}
	names := gitT(t, dir, "diff-tree", "--no-commit-id", "--name-only", "-r", commit)
	if treeHasConfig(names) || !strings.Contains(names, "work.txt") {
		t.Fatalf("commit names:\n%s", names)
	}
	for _, p := range paths {
		if got := gitT(t, dir, "show", commit+":"+p); got != "step_limit: 1" {
			t.Errorf("commit %s = %q, want the HEAD blob", p, got)
		}
		if got := read(t, filepath.Join(dir, p)); got != "step_limit: 2\n" {
			t.Errorf("working copy %s = %q", p, got)
		}
	}
	if staged := gitT(t, dir, "diff", "--cached", "--name-only"); treeHasConfig(staged) {
		t.Errorf("config staged after commit:\n%s", staged)
	}
	// Only a further config edit: nothing to commit, and the file stays.
	write(t, dir, "tinyagent.yaml", "step_limit: 3\n")
	head := gitT(t, dir, "rev-parse", "HEAD")
	if _, changed, err := Commit(dir, "config only"); err != nil || changed {
		t.Fatalf("config-only Commit = %v, %v", changed, err)
	}
	if got := gitT(t, dir, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD moved to %s", got)
	}
	if got := read(t, filepath.Join(dir, "tinyagent.yaml")); got != "step_limit: 3\n" {
		t.Errorf("working copy changed: %q", got)
	}
}

// An already-staged tinyagent.yaml blocks the commit. Other staged files
// stay staged, unstaged files stay unstaged, and the working copy is kept.
func TestStagedLocalConfigBlocksCommit(t *testing.T) {
	dir := testRepo(t)
	write(t, dir, "keep.txt", "staged\n")
	write(t, dir, "later.txt", "unstaged\n")
	write(t, dir, ".tinyagent/tinyagent.yaml", "model: local\n")
	write(t, dir, "tinyagent.yaml", "model: root\n")
	gitT(t, dir, "add", "keep.txt", ".tinyagent/tinyagent.yaml", "tinyagent.yaml")
	head := gitT(t, dir, "rev-parse", "HEAD")
	stagedBefore := gitT(t, dir, "diff", "--cached", "--name-only")
	_, changed, err := Commit(dir, "should not")
	if err == nil || !errors.Is(err, errStagedLocalConfig) || changed {
		t.Fatalf("Commit = %v, %v", changed, err)
	}
	msg := err.Error()
	if !strings.Contains(msg, ".tinyagent/tinyagent.yaml") || !namesPath(msg, "tinyagent.yaml") || !strings.Contains(msg, "working copy") {
		t.Errorf("error = %v", err)
	}
	if got := gitT(t, dir, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD moved to %s", got)
	}
	if got := gitT(t, dir, "diff", "--cached", "--name-only"); got != stagedBefore {
		t.Errorf("staged set changed:\n%s\nwant:\n%s", got, stagedBefore)
	}
	if strings.Contains(gitT(t, dir, "diff", "--cached", "--name-only"), "later.txt") {
		t.Error("unstaged later.txt was staged")
	}
	if got := read(t, filepath.Join(dir, "tinyagent.yaml")); got != "model: root\n" {
		t.Errorf("root config = %q", got)
	}
}

// A staged deletion of a tracked tinyagent.yaml is also refused. The
// working copy stays, and a separately staged file stays staged.
func TestStagedLocalConfigDeletionBlocksCommit(t *testing.T) {
	dir := testRepo(t)
	write(t, dir, "tinyagent.yaml", "model: root\n")
	write(t, dir, "keep.txt", "one\n")
	gitT(t, dir, "add", "tinyagent.yaml", "keep.txt")
	gitT(t, dir, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "tracked")
	gitT(t, dir, "rm", "--cached", "-q", "--", "tinyagent.yaml")
	write(t, dir, "keep.txt", "two\n")
	gitT(t, dir, "add", "keep.txt")
	head := gitT(t, dir, "rev-parse", "HEAD")
	_, changed, err := Commit(dir, "should not")
	if err == nil || !errors.Is(err, errStagedLocalConfig) || changed {
		t.Fatalf("Commit = %v, %v", changed, err)
	}
	if got := gitT(t, dir, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD moved to %s", got)
	}
	if got := read(t, filepath.Join(dir, "tinyagent.yaml")); got != "model: root\n" {
		t.Errorf("working copy = %q", got)
	}
	staged := gitT(t, dir, "diff", "--cached", "--name-only")
	if !strings.Contains(staged, "tinyagent.yaml") || !strings.Contains(staged, "keep.txt") {
		t.Errorf("staged after refusal:\n%s", staged)
	}
}

func treeHasConfig(files string) bool {
	for _, l := range strings.Split(files, "\n") {
		if isLocalConfigPath(l) {
			return true
		}
	}
	return false
}

// namesPath reports whether text lists path as its own comma- or
// parenthesis-separated field, so "tinyagent.yaml" does not match
// ".tinyagent/tinyagent.yaml".
func namesPath(text, path string) bool {
	for _, f := range strings.FieldsFunc(text, func(r rune) bool {
		return r == '\n' || r == ',' || r == '(' || r == ')' || r == ';'
	}) {
		if strings.TrimSpace(f) == path {
			return true
		}
	}
	return false
}
