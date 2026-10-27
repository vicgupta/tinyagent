package gitwt

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// testRepo returns a repository with one commit holding a.txt, and no
// configured identity beyond what the commit itself needed.
func testRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		dir = r
	}
	gitT(t, dir, "init", "-q")
	write(t, dir, "a.txt", "one\n")
	write(t, dir, ".gitignore", "ignored.txt\n")
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "init")
	return dir
}

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := run(dir, Env("GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"), args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return out
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestTopAndHead(t *testing.T) {
	if _, err := Top(t.TempDir()); err == nil {
		t.Error("Top of a non-repo succeeded")
	}
	empty := t.TempDir()
	gitT(t, empty, "init", "-q")
	if _, err := Head(empty); err == nil || !strings.Contains(err.Error(), "no commits") {
		t.Errorf("Head of an empty repo: %v", err)
	}
	dir := testRepo(t)
	os.Mkdir(filepath.Join(dir, "sub"), 0o755)
	if top, err := Top(filepath.Join(dir, "sub")); err != nil || top != dir {
		t.Errorf("Top = %q, %v; want %q", top, err, dir)
	}
}

func TestSnapshotLeavesIndexAndHeadAlone(t *testing.T) {
	dir := testRepo(t)
	head := gitT(t, dir, "rev-parse", "HEAD")
	write(t, dir, "a.txt", "changed\n")
	write(t, dir, "new.txt", "untracked\n")
	write(t, dir, "ignored.txt", "secret\n")
	write(t, dir, ".tinyagent/worktrees/x/f", "other worktree\n")
	statusBefore := gitT(t, dir, "status", "--porcelain")
	snap, err := Snapshot(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := gitT(t, dir, "rev-parse", "HEAD"); got != head {
		t.Errorf("HEAD moved to %s", got)
	}
	if got := gitT(t, dir, "status", "--porcelain"); got != statusBefore {
		t.Errorf("status changed:\n%s\nwant:\n%s", got, statusBefore)
	}
	if got := gitT(t, dir, "rev-parse", snap+"^"); got != head {
		t.Errorf("snapshot parent = %s, want HEAD", got)
	}
	files := gitT(t, dir, "ls-tree", "-r", "--name-only", snap)
	for _, want := range []string{"a.txt", "new.txt", ".gitignore"} {
		if !strings.Contains(files, want) {
			t.Errorf("snapshot lacks %s: %s", want, files)
		}
	}
	for _, bad := range []string{"ignored.txt", ".tinyagent"} {
		if strings.Contains(files, bad) {
			t.Errorf("snapshot has %s: %s", bad, files)
		}
	}
	if got := gitT(t, dir, "show", snap+":a.txt"); got != "changed" {
		t.Errorf("snapshot a.txt = %q", got)
	}
}

func TestCreateCommitApply(t *testing.T) {
	// No identity anywhere: the fallback must be used.
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	dir := testRepo(t)
	write(t, dir, "a.txt", "dirty\n")
	base, err := Snapshot(dir)
	if err != nil {
		t.Fatal(err)
	}
	wt, branch, err := Create(dir, "run-w1", base)
	if err != nil {
		t.Fatal(err)
	}
	if branch != "tinyagent/run-w1" || wt != filepath.Join(dir, Dir, "run-w1") {
		t.Errorf("Create = %q, %q", wt, branch)
	}
	if got := read(t, filepath.Join(wt, "a.txt")); got != "dirty\n" {
		t.Errorf("worktree a.txt = %q", got)
	}
	if got := gitT(t, dir, "status", "--porcelain"); strings.Contains(got, ".tinyagent") {
		t.Errorf("worktree dir shows in status: %q", got)
	}
	if _, _, err := Create(dir, "run-w1", base); err == nil {
		t.Error("second Create with the same name succeeded")
	}
	write(t, wt, "a.txt", "dirty\nworker\n")
	write(t, wt, "b/new.go", "package b\n")
	commit, changed, err := Commit(wt, "worker change")
	if err != nil || !changed {
		t.Fatalf("Commit = %v, %v", changed, err)
	}
	if got := gitT(t, dir, "log", "-1", "--format=%an <%ae>", commit); got != FallbackName+" <"+FallbackEmail+">" {
		t.Errorf("author = %q", got)
	}
	files, err := ChangedFiles(dir, base, commit)
	if err != nil || !slices.Equal(files, []string{"a.txt", "b/new.go"}) {
		t.Errorf("ChangedFiles = %v, %v", files, err)
	}
	stat, short := DiffStat(dir, base, commit)
	if !strings.Contains(stat, "b/new.go") || !strings.Contains(short, "2 files changed") {
		t.Errorf("DiffStat = %q / %q", stat, short)
	}
	conflicts, err := Apply(dir, base, commit)
	if err != nil || conflicts != nil {
		t.Fatalf("Apply = %v, %v", conflicts, err)
	}
	if got := read(t, filepath.Join(dir, "a.txt")); got != "dirty\nworker\n" {
		t.Errorf("applied a.txt = %q", got)
	}
	if got := read(t, filepath.Join(dir, "b/new.go")); got != "package b\n" {
		t.Errorf("applied b/new.go = %q", got)
	}
	if got := gitT(t, dir, "diff", "--cached", "--name-only"); got != "" {
		t.Errorf("Apply touched the index: %q", got)
	}
}

func TestApplyConflictChangesNothing(t *testing.T) {
	dir := testRepo(t)
	base, _ := Snapshot(dir)
	wt, _, err := Create(dir, "c-w1", base)
	if err != nil {
		t.Fatal(err)
	}
	write(t, wt, "a.txt", "worker\n")
	write(t, wt, "z.txt", "new\n")
	commit, _, err := Commit(wt, "w")
	if err != nil {
		t.Fatal(err)
	}
	write(t, dir, "a.txt", "parent moved on\n")
	conflicts, err := Apply(dir, base, commit)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(conflicts, []string{"a.txt"}) {
		t.Errorf("conflicts = %v", conflicts)
	}
	if got := read(t, filepath.Join(dir, "a.txt")); got != "parent moved on\n" {
		t.Errorf("a.txt = %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "z.txt")); err == nil {
		t.Error("a partial apply created z.txt")
	}
}

func TestCommitNothingAndRemove(t *testing.T) {
	dir := testRepo(t)
	base, _ := Snapshot(dir)
	wt, branch, err := Create(dir, "n-w1", base)
	if err != nil {
		t.Fatal(err)
	}
	if _, changed, err := Commit(wt, "nothing"); err != nil || changed {
		t.Errorf("Commit with no changes = %v, %v", changed, err)
	}
	Remove(dir, wt, branch)
	if _, err := os.Stat(wt); err == nil {
		t.Error("worktree still exists")
	}
	if BranchExists(dir, branch) {
		t.Error("branch still exists")
	}
}

func TestCreateRefusesSymlinkedDir(t *testing.T) {
	dir := testRepo(t)
	if err := os.Symlink(t.TempDir(), filepath.Join(dir, ".tinyagent")); err != nil {
		t.Skip(err)
	}
	base, _ := Snapshot(dir)
	if _, _, err := Create(dir, "s-w1", base); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Errorf("Create through a symlink: %v", err)
	}
}
