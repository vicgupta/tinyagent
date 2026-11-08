//go:build !windows

package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Shell git run by bash and start_process must not record tinyagent.yaml,
// including when a !tinyagent.yaml negation would otherwise stage it.
// Staged blobs of the config and of unrelated files stay byte-for-byte, and
// git run outside TinyAgent is not intercepted.
func TestShellGitDoesNotCommitConfig(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}

	t.Run("bash commit -a", func(t *testing.T) {
		dir := configRepo(t)
		stageDistinct(t, dir)
		before := gitOut(t, dir, "ls-files", "-s")
		head := gitOut(t, dir, "rev-parse", "HEAD")
		keep := gitOut(t, dir, "rev-parse", ":keep.txt")
		root := gitOut(t, dir, "rev-parse", ":tinyagent.yaml")
		local := gitOut(t, dir, "rev-parse", ":.tinyagent/tinyagent.yaml")
		out := runBash(t, dir, "git commit -am worker")
		if !strings.Contains(out, "refusing to commit tinyagent.yaml") || !strings.Contains(out, "<returncode>1</returncode>") {
			t.Fatalf("bash commit -a: %s", out)
		}
		if gitOut(t, dir, "ls-files", "-s") != before || gitOut(t, dir, "rev-parse", "HEAD") != head {
			t.Fatalf("index or HEAD changed:\n%s", gitOut(t, dir, "ls-files", "-s"))
		}
		if gitOut(t, dir, "rev-parse", ":keep.txt") != keep || gitOut(t, dir, "rev-parse", ":tinyagent.yaml") != root || gitOut(t, dir, "rev-parse", ":.tinyagent/tinyagent.yaml") != local {
			t.Fatal("staged blob changed")
		}
		if gitOut(t, dir, "cat-file", "-p", root) != "CFG-INDEX" || readBytes(t, filepath.Join(dir, "tinyagent.yaml")) != "CFG-WORK\n" {
			t.Fatal("config content changed")
		}
		if readBytes(t, filepath.Join(dir, "keep.txt")) != "KEEP-WORK\n" || gitOut(t, dir, "cat-file", "-p", keep) != "KEEP-INDEX" {
			t.Fatal("unrelated staged content changed")
		}
	})

	t.Run("start_process commit -a", func(t *testing.T) {
		dir := configRepo(t)
		stageDistinct(t, dir)
		before := gitOut(t, dir, "ls-files", "-s")
		head := gitOut(t, dir, "rev-parse", "HEAD")
		root := gitOut(t, dir, "rev-parse", ":tinyagent.yaml")
		out := runProcess(t, dir, "git commit --no-verify -am worker")
		if !strings.Contains(out, "refusing to commit tinyagent.yaml") {
			t.Fatalf("start_process commit: %s", out)
		}
		if gitOut(t, dir, "ls-files", "-s") != before || gitOut(t, dir, "rev-parse", "HEAD") != head || gitOut(t, dir, "rev-parse", ":tinyagent.yaml") != root {
			t.Fatal("index changed after start_process")
		}
		if readBytes(t, filepath.Join(dir, "tinyagent.yaml")) != "CFG-WORK\n" {
			t.Fatal("working config changed")
		}
	})

	t.Run("bash add and commit with negation", func(t *testing.T) {
		dir := configRepo(t)
		os.WriteFile(filepath.Join(dir, ".git", "info", "exclude"), []byte("!tinyagent.yaml\n"), 0o644)
		os.WriteFile(filepath.Join(dir, "other.txt"), []byte("OTHER\n"), 0o644)
		os.WriteFile(filepath.Join(dir, "tinyagent.yaml"), []byte("ROOT-SECRET\n"), 0o644)
		os.MkdirAll(filepath.Join(dir, ".tinyagent"), 0o755)
		os.WriteFile(filepath.Join(dir, ".tinyagent", "tinyagent.yaml"), []byte("LOCAL-SECRET\n"), 0o644)
		out := runBash(t, dir, "git add -A && git commit -m worker")
		if !strings.Contains(out, "<returncode>0</returncode>") {
			t.Fatalf("add+commit: %s", out)
		}
		names := gitOut(t, dir, "diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD")
		if strings.Contains(names, "tinyagent.yaml") || !strings.Contains(names, "other.txt") {
			t.Fatalf("commit names:\n%s", names)
		}
		if readBytes(t, filepath.Join(dir, "tinyagent.yaml")) != "ROOT-SECRET\n" || readBytes(t, filepath.Join(dir, ".tinyagent", "tinyagent.yaml")) != "LOCAL-SECRET\n" {
			t.Fatal("working config changed")
		}
		status := gitOut(t, dir, "status", "--porcelain")
		if !strings.Contains(status, "tinyagent.yaml") {
			t.Fatalf("config was committed or deleted:\n%s", status)
		}
	})

	t.Run("outside tinyagent still commits", func(t *testing.T) {
		dir := configRepo(t)
		os.WriteFile(filepath.Join(dir, "tinyagent.yaml"), []byte("HUMAN\n"), 0o644)
		cmd := exec.Command("git", "-C", dir, "add", "-A")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("human add: %v\n%s", err, out)
		}
		cmd = exec.Command("git", "-C", dir, "commit", "-q", "-m", "human")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("human commit: %v\n%s", err, out)
		}
		if gitOut(t, dir, "show", "HEAD:tinyagent.yaml") != "HUMAN" {
			t.Fatal("git outside TinyAgent was intercepted")
		}
	})
}

func configRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q")
	gitRun(t, dir, "config", "user.email", "t@t")
	gitRun(t, dir, "config", "user.name", "t")
	gitRun(t, dir, "config", "commit.gpgsign", "false")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644)
	gitRun(t, dir, "add", "a.txt")
	gitRun(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func stageDistinct(t *testing.T, dir string) {
	t.Helper()
	os.MkdirAll(filepath.Join(dir, ".tinyagent"), 0o755)
	os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("KEEP-HEAD\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "tinyagent.yaml"), []byte("CFG-HEAD\n"), 0o644)
	os.WriteFile(filepath.Join(dir, ".tinyagent", "tinyagent.yaml"), []byte("LOC-HEAD\n"), 0o644)
	gitRun(t, dir, "add", "keep.txt", "tinyagent.yaml", ".tinyagent/tinyagent.yaml")
	gitRun(t, dir, "commit", "-q", "-m", "base")
	os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("KEEP-INDEX\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "tinyagent.yaml"), []byte("CFG-INDEX\n"), 0o644)
	os.WriteFile(filepath.Join(dir, ".tinyagent", "tinyagent.yaml"), []byte("LOC-INDEX\n"), 0o644)
	gitRun(t, dir, "add", "keep.txt", "tinyagent.yaml", ".tinyagent/tinyagent.yaml")
	os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("KEEP-WORK\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "tinyagent.yaml"), []byte("CFG-WORK\n"), 0o644)
	os.WriteFile(filepath.Join(dir, ".tinyagent", "tinyagent.yaml"), []byte("LOC-WORK\n"), 0o644)
}

func runBash(t *testing.T, dir, command string) string {
	t.Helper()
	e := &Executor{Root: dir}
	defer e.Close()
	return runTool(t, e, "bash", map[string]any{"command": command})
}

func runProcess(t *testing.T, dir, command string) string {
	t.Helper()
	e := &Executor{Root: dir}
	defer e.Close()
	id, out := startID(t, e, command)
	waitFor(t, func() bool {
		out += runTool(t, e, "read_process", map[string]any{"id": id, "timeout": 2000})
		return strings.Contains(out, "exited")
	})
	return out
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func readBytes(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestBashStillRunsOrdinaryCommands(t *testing.T) {
	out, err := bashStream(context.Background(), Local{}, t.TempDir(), "echo hello-guard", 10*time.Second, nil)
	if err != nil || !strings.Contains(out, "hello-guard") || !strings.Contains(out, "<returncode>0</returncode>") {
		t.Fatalf("echo: %v %s", err, out)
	}
}
