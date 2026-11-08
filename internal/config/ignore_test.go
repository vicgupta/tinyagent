package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEnsureIgnored(t *testing.T) {
	// No .tinyagent/: nothing is created.
	dir := t.TempDir()
	if changed, err := EnsureIgnored(dir); err != nil || changed {
		t.Fatalf("no .tinyagent: changed %v, err %v", changed, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".tinyagent")); err == nil {
		t.Fatal(".tinyagent created")
	}

	// Missing .gitignore: created with the rule; a second call is a no-op.
	os.Mkdir(filepath.Join(dir, ".tinyagent"), 0o755)
	if changed, err := EnsureIgnored(dir); err != nil || !changed {
		t.Fatalf("create: changed %v, err %v", changed, err)
	}
	ign := filepath.Join(dir, IgnoreFile)
	b, _ := os.ReadFile(ign)
	if !strings.Contains(string(b), "\n/tinyagent.yaml\n") {
		t.Fatalf(".gitignore = %q", b)
	}
	if changed, err := EnsureIgnored(dir); err != nil || changed {
		t.Fatalf("repeat: changed %v, err %v", changed, err)
	}
	if b2, _ := os.ReadFile(ign); string(b2) != string(b) {
		t.Fatalf("repeat changed the file: %q", b2)
	}

	// An existing file keeps every line; the rule is appended.
	os.WriteFile(ign, []byte("memory/\n!keep.md"), 0o644)
	if changed, err := EnsureIgnored(dir); err != nil || !changed {
		t.Fatalf("append: changed %v, err %v", changed, err)
	}
	b, _ = os.ReadFile(ign)
	if !strings.HasPrefix(string(b), "memory/\n!keep.md\n") || !strings.HasSuffix(string(b), "\n/tinyagent.yaml\n") {
		t.Fatalf("append: %q", b)
	}

	// The user's own decision about the file, or a catch-all, is left alone.
	for _, body := range []string{"!tinyagent.yaml\n", "tinyagent.yaml\n", "*\n", "/*\n"} {
		os.WriteFile(ign, []byte(body), 0o644)
		if changed, err := EnsureIgnored(dir); err != nil || changed {
			t.Errorf("%q: changed %v, err %v", body, changed, err)
		}
		if b, _ := os.ReadFile(ign); string(b) != body {
			t.Errorf("%q rewritten to %q", body, b)
		}
	}
}

func TestEnsureIgnoredRefusesSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	dir, other := t.TempDir(), t.TempDir()
	if err := os.Symlink(other, filepath.Join(dir, ".tinyagent")); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureIgnored(dir); err == nil {
		t.Error("symlinked .tinyagent accepted")
	}
	dir2 := t.TempDir()
	os.Mkdir(filepath.Join(dir2, ".tinyagent"), 0o755)
	target := filepath.Join(other, "gi")
	os.WriteFile(target, []byte("x\n"), 0o644)
	if err := os.Symlink(target, filepath.Join(dir2, IgnoreFile)); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureIgnored(dir2); err == nil {
		t.Error("symlinked .gitignore accepted")
	}
	if b, _ := os.ReadFile(target); string(b) != "x\n" {
		t.Errorf("symlink target written: %q", b)
	}
}

// Broad staging (git add . and git add -A) leaves the local config out once
// EnsureIgnored has run, while other files are still staged.
func TestEnsureIgnoredStopsBroadStaging(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	for _, add := range [][]string{{"add", "."}, {"add", "-A"}} {
		dir := t.TempDir()
		git := func(args ...string) string {
			cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
			cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("git %v: %v\n%s", args, err, out)
			}
			return string(out)
		}
		git("init", "-q")
		os.MkdirAll(filepath.Join(dir, ".tinyagent"), 0o755)
		os.WriteFile(filepath.Join(dir, ProjectFile), []byte("model: openai/gpt-4o\n"), 0o600)
		os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644)
		if _, err := EnsureIgnored(dir); err != nil {
			t.Fatal(err)
		}
		git(add...)
		staged := git("diff", "--cached", "--name-only")
		if strings.Contains(staged, "tinyagent.yaml") || !strings.Contains(staged, "main.go") {
			t.Errorf("git %v staged:\n%s", add, staged)
		}
		if _, err := os.Stat(filepath.Join(dir, ProjectFile)); err != nil {
			t.Errorf("config removed: %v", err)
		}
	}
}
