//go:build !windows

package tools

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Vic's scope: bash, start_process, command tools, and the container
// environment are unrestricted. A git commit they run, including one that
// records tinyagent.yaml, is left as the command made it.
func TestShellGitCommitIsNotIntercepted(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("config", "user.email", "t@t")
	git("config", "user.name", "t")
	git("config", "commit.gpgsign", "false")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644)
	git("add", "a.txt")
	git("commit", "-q", "-m", "init")
	os.WriteFile(filepath.Join(dir, ".git", "info", "exclude"), []byte("!tinyagent.yaml\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, ".tinyagent"), 0o755)
	os.WriteFile(filepath.Join(dir, "tinyagent.yaml"), []byte("ROOT\n"), 0o644)
	os.WriteFile(filepath.Join(dir, ".tinyagent", "tinyagent.yaml"), []byte("LOCAL\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "other.txt"), []byte("OTHER\n"), 0o644)
	head := git("rev-parse", "HEAD")

	e := &Executor{Root: dir}
	defer e.Close()
	out := runTool(t, e, "bash", map[string]any{"command": "git add -A && git commit -m shell"})
	if strings.Contains(out, "undid commits") || strings.Contains(out, "refusing to commit tinyagent.yaml") || !strings.Contains(out, "<returncode>0</returncode>") {
		t.Fatalf("bash git commit was intercepted:\n%s", out)
	}
	if git("rev-parse", "HEAD") == head {
		t.Fatal("shell commit did not move HEAD")
	}
	names := git("diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD")
	if !strings.Contains(names, "tinyagent.yaml") || !strings.Contains(names, "other.txt") {
		t.Fatalf("shell commit files:\n%s", names)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "tinyagent.yaml")); err != nil || string(b) != "ROOT\n" {
		t.Fatalf("working copy = %q, %v", b, err)
	}
}
