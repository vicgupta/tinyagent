package gitwt

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecideRefusesCommitWithoutTouchingIndex(t *testing.T) {
	dir := testRepo(t)
	write(t, dir, "keep.txt", "KEEP-HEAD\n")
	write(t, dir, "tinyagent.yaml", "CFG-HEAD\n")
	write(t, dir, ".tinyagent/tinyagent.yaml", "LOC-HEAD\n")
	gitT(t, dir, "add", "keep.txt", "tinyagent.yaml", ".tinyagent/tinyagent.yaml")
	gitT(t, dir, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "base")
	write(t, dir, "keep.txt", "KEEP-INDEX\n")
	write(t, dir, "tinyagent.yaml", "CFG-INDEX\n")
	write(t, dir, ".tinyagent/tinyagent.yaml", "LOC-INDEX\n")
	gitT(t, dir, "add", "keep.txt", "tinyagent.yaml", ".tinyagent/tinyagent.yaml")
	write(t, dir, "keep.txt", "KEEP-WORK\n")
	write(t, dir, "tinyagent.yaml", "CFG-WORK\n")
	write(t, dir, ".tinyagent/tinyagent.yaml", "LOC-WORK\n")
	before := gitT(t, dir, "ls-files", "-s")
	keep := gitT(t, dir, "rev-parse", ":keep.txt")
	root := gitT(t, dir, "rev-parse", ":tinyagent.yaml")
	local := gitT(t, dir, "rev-parse", ":.tinyagent/tinyagent.yaml")
	head := gitT(t, dir, "rev-parse", "HEAD")

	block, msg, rewrite, err := Decide("git", dir, []string{"commit", "-am", "worker"})
	if err != nil || !block || rewrite != nil {
		t.Fatalf("Decide = block %v rewrite %v err %v msg %q", block, rewrite, err, msg)
	}
	if !strings.Contains(msg, ".tinyagent/tinyagent.yaml") || !namesPath(msg, "tinyagent.yaml") || !strings.Contains(msg, "not changed") {
		t.Errorf("message = %q", msg)
	}
	if got := gitT(t, dir, "ls-files", "-s"); got != before {
		t.Errorf("index changed:\n%s\nwant:\n%s", got, before)
	}
	if gitT(t, dir, "rev-parse", ":keep.txt") != keep || gitT(t, dir, "rev-parse", ":tinyagent.yaml") != root || gitT(t, dir, "rev-parse", ":.tinyagent/tinyagent.yaml") != local {
		t.Error("staged blob changed")
	}
	if gitT(t, dir, "rev-parse", "HEAD") != head {
		t.Error("HEAD moved")
	}
	if read(t, filepath.Join(dir, "tinyagent.yaml")) != "CFG-WORK\n" || read(t, filepath.Join(dir, "keep.txt")) != "KEEP-WORK\n" {
		t.Error("working copy changed")
	}
}

func TestDecideAddExcludeKeepsStagedBlob(t *testing.T) {
	dir := testRepo(t)
	os.WriteFile(filepath.Join(dir, ".git", "info", "exclude"), []byte("!tinyagent.yaml\n"), 0o644)
	write(t, dir, "keep.txt", "KEEP-INDEX\n")
	write(t, dir, "tinyagent.yaml", "CFG-INDEX\n")
	gitT(t, dir, "add", "keep.txt", "tinyagent.yaml")
	write(t, dir, "keep.txt", "KEEP-WORK\n")
	write(t, dir, "tinyagent.yaml", "CFG-WORK\n")
	write(t, dir, ".tinyagent/tinyagent.yaml", "LOCAL-UNTRACKED\n")
	root := gitT(t, dir, "rev-parse", ":tinyagent.yaml")
	_, _, rewrite, err := Decide("git", dir, []string{"add", "-A"})
	if err != nil || rewrite == nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", append([]string{"-C", dir}, rewrite...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("rewritten add: %v\n%s", err, out)
	}
	if gitT(t, dir, "rev-parse", ":tinyagent.yaml") != root {
		t.Error("staged config blob changed")
	}
	if read(t, filepath.Join(dir, "tinyagent.yaml")) != "CFG-WORK\n" || read(t, filepath.Join(dir, ".tinyagent/tinyagent.yaml")) != "LOCAL-UNTRACKED\n" {
		t.Error("working copy changed")
	}
	staged := gitT(t, dir, "diff", "--cached", "--name-only")
	if strings.Contains("\n"+staged+"\n", "\n.tinyagent/tinyagent.yaml\n") {
		t.Errorf("untracked config staged:\n%s", staged)
	}
	if gitT(t, dir, "rev-parse", ":keep.txt") == "" || !strings.Contains(gitT(t, dir, "show", ":keep.txt"), "KEEP-WORK") {
		t.Error("keep.txt was not updated to the worktree")
	}
}

func TestDecidePlainCommitAllowsUnstagedConfig(t *testing.T) {
	dir := testRepo(t)
	write(t, dir, "tinyagent.yaml", "CFG-HEAD\n")
	write(t, dir, "other.txt", "old\n")
	gitT(t, dir, "add", "tinyagent.yaml", "other.txt")
	gitT(t, dir, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "base")
	write(t, dir, "tinyagent.yaml", "CFG-WORK\n")
	write(t, dir, "other.txt", "new\n")
	gitT(t, dir, "add", "other.txt")
	block, msg, _, err := Decide("git", dir, []string{"commit", "-m", "other"})
	if err != nil || block {
		t.Fatalf("plain commit blocked: %v %q", err, msg)
	}
}
