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

func TestEnsureGitExclude(t *testing.T) {
	dir := t.TempDir()
	if changed, err := EnsureGitExclude(dir); err != nil || changed {
		t.Fatalf("not a repo: changed %v, err %v", changed, err)
	}

	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := t.TempDir()
	git := gitCmd(t, repo)
	git("init", "-q")
	gi := filepath.Join(repo, ".gitignore")
	os.WriteFile(gi, []byte("build/\n"), 0o644)
	exclude := gitExcludePath(t, repo)
	os.Remove(exclude)
	if changed, err := EnsureGitExclude(repo); err != nil || !changed {
		t.Fatalf("create: changed %v, err %v", changed, err)
	}
	b, err := os.ReadFile(exclude)
	if err != nil || !strings.Contains(string(b), "\ntinyagent.yaml\n") {
		t.Fatalf("exclude = %q, %v", b, err)
	}
	if changed, err := EnsureGitExclude(repo); err != nil || changed {
		t.Fatalf("repeat: changed %v, err %v", changed, err)
	}
	if b2, _ := os.ReadFile(exclude); string(b2) != string(b) {
		t.Fatalf("repeat changed exclude: %q", b2)
	}
	if got, _ := os.ReadFile(gi); string(got) != "build/\n" {
		t.Errorf("repository .gitignore changed: %q", got)
	}

	// A negation, or any existing mention, is the user's decision.
	os.WriteFile(exclude, []byte("!tinyagent.yaml\n"), 0o644)
	if changed, err := EnsureGitExclude(repo); err != nil || changed {
		t.Fatalf("negation: changed %v, err %v", changed, err)
	}
	if got, _ := os.ReadFile(exclude); string(got) != "!tinyagent.yaml\n" {
		t.Fatalf("negation rewritten: %q", got)
	}
}

// Untracked tinyagent.yaml paths stay out of git add . and git add -A.
// A tracked copy is still staged: the exclude rule does not cover it.
func TestEnsureGitExcludeBroadStaging(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	for _, add := range [][]string{{"add", "."}, {"add", "-A"}} {
		dir := t.TempDir()
		git := gitCmd(t, dir)
		git("init", "-q")
		os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("build/\n"), 0o644)
		for _, p := range []string{"tinyagent.yaml", ".tinyagent/tinyagent.yaml", "sub/tinyagent.yaml"} {
			os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755)
			os.WriteFile(filepath.Join(dir, p), []byte("model: local\n"), 0o600)
		}
		os.WriteFile(filepath.Join(dir, "tinyagent.yaml.bak"), []byte("keep\n"), 0o644)
		os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644)
		if _, err := EnsureGitExclude(dir); err != nil {
			t.Fatal(err)
		}
		git(add...)
		staged := git("diff", "--cached", "--name-only")
		for _, p := range []string{"tinyagent.yaml", ".tinyagent/tinyagent.yaml", "sub/tinyagent.yaml"} {
			if hasStaged(staged, p) {
				t.Errorf("git %v staged untracked %s:\n%s", add, p, staged)
			}
			if b, err := os.ReadFile(filepath.Join(dir, p)); err != nil || string(b) != "model: local\n" {
				t.Errorf("%s working copy = %q, %v", p, b, err)
			}
		}
		if !hasStaged(staged, "main.go") || !hasStaged(staged, "tinyagent.yaml.bak") {
			t.Errorf("git %v staged:\n%s", add, staged)
		}
		if got, _ := os.ReadFile(filepath.Join(dir, ".gitignore")); string(got) != "build/\n" {
			t.Errorf(".gitignore changed: %q", got)
		}
	}

	// Already tracked: git add . still stages the modification.
	dir := t.TempDir()
	git := gitCmd(t, dir)
	git("init", "-q")
	os.MkdirAll(filepath.Join(dir, ".tinyagent"), 0o755)
	os.WriteFile(filepath.Join(dir, "tinyagent.yaml"), []byte("step_limit: 1\n"), 0o600)
	os.WriteFile(filepath.Join(dir, ".tinyagent", "tinyagent.yaml"), []byte("step_limit: 1\n"), 0o600)
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n"), 0o644)
	if _, err := EnsureGitExclude(dir); err != nil {
		t.Fatal(err)
	}
	git("add", "-f", "tinyagent.yaml", ".tinyagent/tinyagent.yaml", "main.go")
	git("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "track")
	os.WriteFile(filepath.Join(dir, "tinyagent.yaml"), []byte("step_limit: 2\n"), 0o600)
	os.WriteFile(filepath.Join(dir, ".tinyagent", "tinyagent.yaml"), []byte("step_limit: 2\n"), 0o600)
	git("add", ".")
	staged := git("diff", "--cached", "--name-only")
	if !hasStaged(staged, "tinyagent.yaml") || !hasStaged(staged, ".tinyagent/tinyagent.yaml") {
		t.Errorf("tracked config was not staged:\n%s", staged)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "tinyagent.yaml")); string(b) != "step_limit: 2\n" {
		t.Errorf("tracked working copy = %q", b)
	}
}

func TestEnsureGitExcludeRefusesSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	gitCmd(t, dir)("init", "-q")
	exclude := gitExcludePath(t, dir)
	other := filepath.Join(t.TempDir(), "exclude")
	os.WriteFile(other, []byte("keep\n"), 0o644)
	os.Remove(exclude)
	if err := os.Symlink(other, exclude); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureGitExclude(dir); err == nil {
		t.Fatal("symlinked exclude accepted")
	}
	if b, _ := os.ReadFile(other); string(b) != "keep\n" {
		t.Errorf("symlink target written: %q", b)
	}
}

func gitCmd(t *testing.T, dir string) func(...string) string {
	t.Helper()
	return func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
}

func gitExcludePath(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--git-common-dir")
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	p := strings.TrimSpace(string(out))
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}
	return filepath.Join(p, "info", "exclude")
}

func hasStaged(staged, path string) bool {
	for _, l := range strings.Split(staged, "\n") {
		if strings.TrimSpace(l) == path {
			return true
		}
	}
	return false
}
