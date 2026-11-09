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

// Commits made by a tool are judged from the repository afterward, so the
// check does not depend on parsing the command. These are the forms a shell
// function does not see.
func TestToolGitBypassesDoNotKeepConfigCommit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, command string
		process       bool
	}{
		{"nested shell", `bash -c 'git add -A && git commit -m nested'`, false},
		{"command git", `command git add -A && command git commit --no-verify -m cmd`, false},
		{"absolute git", gitPath + ` add -A && ` + gitPath + ` commit -m abs`, false},
		{"start_process absolute", gitPath + ` add -A && ` + gitPath + ` commit -m proc`, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := configRepo(t)
			os.WriteFile(filepath.Join(dir, ".git", "info", "exclude"), []byte("!tinyagent.yaml\n"), 0o644)
			os.MkdirAll(filepath.Join(dir, ".tinyagent"), 0o755)
			os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("KEEP-INDEX\n"), 0o644)
			os.WriteFile(filepath.Join(dir, "tinyagent.yaml"), []byte("CFG-INDEX\n"), 0o644)
			os.WriteFile(filepath.Join(dir, ".tinyagent", "tinyagent.yaml"), []byte("LOC-INDEX\n"), 0o644)
			gitRun(t, dir, "add", "keep.txt", "tinyagent.yaml", ".tinyagent/tinyagent.yaml")
			os.WriteFile(filepath.Join(dir, "keep.txt"), []byte("KEEP-WORK\n"), 0o644)
			os.WriteFile(filepath.Join(dir, "tinyagent.yaml"), []byte("CFG-WORK\n"), 0o644)
			os.WriteFile(filepath.Join(dir, ".tinyagent", "tinyagent.yaml"), []byte("LOC-WORK\n"), 0o644)
			os.WriteFile(filepath.Join(dir, "other.txt"), []byte("OTHER\n"), 0o644)
			before := gitOut(t, dir, "ls-files", "-s")
			head := gitOut(t, dir, "rev-parse", "HEAD")
			root := gitOut(t, dir, "rev-parse", ":tinyagent.yaml")
			var out string
			if c.process {
				out = runProcessUntil(t, dir, c.command, "undid commits")
			} else {
				out = runBash(t, dir, c.command)
			}
			if !strings.Contains(out, "undid commits that recorded tinyagent.yaml") {
				t.Fatalf("output:\n%s", out)
			}
			if gitOut(t, dir, "rev-parse", "HEAD") != head {
				t.Fatal("HEAD was left on the config commit")
			}
			if gitOut(t, dir, "ls-files", "-s") != before {
				t.Fatalf("index changed:\n%s\nwant:\n%s", gitOut(t, dir, "ls-files", "-s"), before)
			}
			if gitOut(t, dir, "rev-parse", ":tinyagent.yaml") != root || gitOut(t, dir, "cat-file", "-p", root) != "CFG-INDEX" {
				t.Fatal("staged config blob changed")
			}
			if readBytes(t, filepath.Join(dir, "tinyagent.yaml")) != "CFG-WORK\n" || readBytes(t, filepath.Join(dir, ".tinyagent", "tinyagent.yaml")) != "LOC-WORK\n" || readBytes(t, filepath.Join(dir, "keep.txt")) != "KEEP-WORK\n" {
				t.Fatal("working copy changed")
			}
			if gitOut(t, dir, "cat-file", "-p", gitOut(t, dir, "rev-parse", ":keep.txt")) != "KEEP-INDEX" {
				t.Fatal("unrelated staged blob changed")
			}
		})
	}

	t.Run("container absolute git", func(t *testing.T) {
		c, root := fakeContainer(t)
		gitInit(t, root)
		os.WriteFile(filepath.Join(root, ".git", "info", "exclude"), []byte("!tinyagent.yaml\n"), 0o644)
		os.WriteFile(filepath.Join(root, "tinyagent.yaml"), []byte("BOX\n"), 0o644)
		os.WriteFile(filepath.Join(root, "other.txt"), []byte("OTHER\n"), 0o644)
		head := gitOut(t, root, "rev-parse", "HEAD")
		before := gitOut(t, root, "ls-files", "-s")
		out, err := bashStream(context.Background(), c, root, gitPath+` add -A && `+gitPath+` commit -m box`, 20*time.Second, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, "undid commits that recorded tinyagent.yaml") {
			t.Fatalf("container output:\n%s", out)
		}
		if gitOut(t, root, "rev-parse", "HEAD") != head || gitOut(t, root, "ls-files", "-s") != before {
			t.Fatal("container commit of config was kept")
		}
		if readBytes(t, filepath.Join(root, "tinyagent.yaml")) != "BOX\n" {
			t.Fatal("container working copy changed")
		}
	})

	t.Run("new branch", func(t *testing.T) {
		dir := configRepo(t)
		os.WriteFile(filepath.Join(dir, "tinyagent.yaml"), []byte("BRANCH\n"), 0o644)
		os.WriteFile(filepath.Join(dir, "other.txt"), []byte("OTHER\n"), 0o644)
		head := gitOut(t, dir, "rev-parse", "HEAD")
		sym := gitOut(t, dir, "symbolic-ref", "HEAD")
		before := gitOut(t, dir, "ls-files", "-s")
		out := runBash(t, dir, `git checkout -b side && git add -A && git commit -m side`)
		if !strings.Contains(out, "undid commits that recorded tinyagent.yaml") {
			t.Fatalf("branch output:\n%s", out)
		}
		if gitOut(t, dir, "rev-parse", "HEAD") != head || gitOut(t, dir, "symbolic-ref", "HEAD") != sym {
			t.Fatalf("HEAD left on side: %s %s", gitOut(t, dir, "rev-parse", "HEAD"), gitOut(t, dir, "symbolic-ref", "HEAD"))
		}
		if gitOut(t, dir, "ls-files", "-s") != before {
			t.Fatal("index changed after branch commit")
		}
		if readBytes(t, filepath.Join(dir, "tinyagent.yaml")) != "BRANCH\n" {
			t.Fatal("working copy changed")
		}
	})

	t.Run("ordinary commit stays", func(t *testing.T) {
		dir := configRepo(t)
		os.WriteFile(filepath.Join(dir, "tinyagent.yaml"), []byte("CFG-WORK\n"), 0o644)
		os.WriteFile(filepath.Join(dir, "other.txt"), []byte("OTHER\n"), 0o644)
		out := runBash(t, dir, `git add other.txt && git commit -m ok`)
		if strings.Contains(out, "undid commits") || !strings.Contains(out, "<returncode>0</returncode>") {
			t.Fatalf("ordinary commit: %s", out)
		}
		if gitOut(t, dir, "show", "HEAD:other.txt") != "OTHER" {
			t.Fatal("ordinary commit was removed")
		}
		names := gitOut(t, dir, "diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD")
		if strings.Contains(names, "tinyagent.yaml") {
			t.Fatalf("config was in the ordinary commit:\n%s", names)
		}
		if readBytes(t, filepath.Join(dir, "tinyagent.yaml")) != "CFG-WORK\n" {
			t.Fatal("unstaged config was changed")
		}
	})

	t.Run("git outside tinyagent still commits", func(t *testing.T) {
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
	gitInit(t, dir)
	return dir
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	gitRun(t, dir, "init", "-q")
	gitRun(t, dir, "config", "user.email", "t@t")
	gitRun(t, dir, "config", "user.name", "t")
	gitRun(t, dir, "config", "commit.gpgsign", "false")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644)
	gitRun(t, dir, "add", "a.txt")
	gitRun(t, dir, "commit", "-q", "-m", "init")
}

func runBash(t *testing.T, dir, command string) string {
	t.Helper()
	e := &Executor{Root: dir}
	defer e.Close()
	return runTool(t, e, "bash", map[string]any{"command": command})
}

func runProcessUntil(t *testing.T, dir, command, needle string) string {
	t.Helper()
	e := &Executor{Root: dir}
	defer e.Close()
	id, out := startID(t, e, command)
	waitFor(t, func() bool {
		out += runTool(t, e, "read_process", map[string]any{"id": id, "timeout": 2000})
		return strings.Contains(out, needle) || strings.Contains(out, "exited")
	})
	if !strings.Contains(out, needle) {
		out += runTool(t, e, "read_process", map[string]any{"id": id, "timeout": 2000})
	}
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
