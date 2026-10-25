package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// gitBin is git's absolute path, so tests still reach it after hiding git
// from PATH.
var gitBin = "git"

// gitT runs git in dir for a test and returns its trimmed output.
func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(gitBin, append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Env = gitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// worktreeRepo returns a canonical temp Git repository with one commit
// containing AGENTS.md and sub/dir/keep.txt. It skips without git.
func worktreeRepo(t *testing.T) string {
	t.Helper()
	bin, lerr := exec.LookPath("git")
	if lerr != nil {
		t.Skip("git not available")
	}
	gitBin = bin
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	gitT(t, repo, "init", "-q")
	os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("WT-AGENTS-COMMITTED\n"), 0o644)
	os.MkdirAll(filepath.Join(repo, "sub", "dir"), 0o755)
	os.WriteFile(filepath.Join(repo, "sub", "dir", "keep.txt"), []byte("keep\n"), 0o644)
	gitT(t, repo, "add", "AGENTS.md", "sub/dir/keep.txt")
	gitT(t, repo, "commit", "-q", "-m", "init")
	return repo
}

func TestWorktreeNames(t *testing.T) {
	for _, n := range []string{"a", "fix-login", "A.b_c-9", strings.Repeat("x", 64)} {
		if err := validWorktreeName(n); err != nil {
			t.Errorf("%q rejected: %v", n, err)
		}
	}
	for _, n := range []string{"", ".a", "-a", "a/b", "a b", "a..b", "a.", "a.lock", strings.Repeat("x", 65), "ä"} {
		if validWorktreeName(n) == nil {
			t.Errorf("%q accepted", n)
		}
	}
	now := time.Date(2026, 10, 25, 16, 25, 12, 0, time.UTC)
	re := regexp.MustCompile(`^run-20261025-162512-[0-9a-f]{6}$`)
	a, b := generateWorktreeName(now), generateWorktreeName(now)
	if !re.MatchString(a) || validWorktreeName(a) != nil {
		t.Errorf("generated name %q doesn't match the documented pattern", a)
	}
	if a == b {
		t.Errorf("two names in the same second are equal: %q", a)
	}
	var f worktreeFlag
	for _, c := range []struct {
		v    string
		on   bool
		name string
	}{{"true", true, ""}, {"x", true, "x"}, {"false", false, ""}} {
		if err := f.Set(c.v); err != nil || f.on != c.on || f.name != c.name {
			t.Errorf("Set(%q) = %+v, %v", c.v, f, err)
		}
	}
	if f.Set("") == nil {
		t.Error("empty name accepted")
	}
}

const responsesWriteNew = `{"status":"completed","output":[{"type":"function_call","call_id":"c1","name":"write_file","arguments":"{\"path\":\"new.txt\",\"content\":\"from-run\"}"}],"usage":{"input_tokens":1,"output_tokens":1}}`

// TestWorktreeRun covers a --worktree run end to end: the worktree and
// branch, isolation from a dirty main checkout, a subdirectory --cwd,
// AGENTS.md discovery, run.started, the trajectory, and resuming and
// rewinding through the worktree path.
func TestWorktreeRun(t *testing.T) {
	repo := worktreeRepo(t)
	_, emptyCfg := openaiAPISetup(t)
	base := gitT(t, repo, "rev-parse", "HEAD")
	branchBefore := gitT(t, repo, "symbolic-ref", "HEAD")
	// A dirty main checkout: a modified tracked file, a staged file and an
	// untracked one.
	os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("MAIN-AGENTS-DIRTY\n"), 0o644)
	os.WriteFile(filepath.Join(repo, "staged.txt"), []byte("s\n"), 0o644)
	gitT(t, repo, "add", "staged.txt")
	os.WriteFile(filepath.Join(repo, "untracked.txt"), []byte("u\n"), 0o644)
	statusBefore := gitT(t, repo, "status", "--porcelain")
	cachedBefore := gitT(t, repo, "diff", "--cached")

	f := &fakeResponses{replies: []string{responsesWriteNew, responsesSubmit}}
	t.Setenv("OPENAI_BASE_URL", f.start(t).URL)
	out := filepath.Join(t.TempDir(), "run.json")
	var code int
	var stdout string
	stderr := captureStderr(t, func() {
		stdout = captureStdout(t, func() {
			code, _, _ = runWith(t, "-t", "x", "-m", "openai/gpt-5", "--openai-api", "responses", "-c", emptyCfg, "--cwd", filepath.Join(repo, "sub", "dir"), "--worktree=a", "--json", "--snapshots", "-o", out)
		})
	})
	if code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, stderr)
	}
	wt := filepath.Join(repo, ".tinyagent", "worktrees", "a")
	wtRoot := filepath.Join(wt, "sub", "dir")
	if !strings.Contains(gitT(t, repo, "worktree", "list"), wt) {
		t.Errorf("git worktree list lacks %s", wt)
	}
	if got := gitT(t, repo, "rev-parse", "tinyagent/a"); got != base {
		t.Errorf("branch tinyagent/a at %s, want %s", got, base)
	}
	if got := gitT(t, wt, "symbolic-ref", "HEAD"); got != "refs/heads/tinyagent/a" {
		t.Errorf("worktree HEAD = %s", got)
	}
	if b, err := os.ReadFile(filepath.Join(wtRoot, "new.txt")); err != nil || string(b) != "from-run" {
		t.Errorf("new.txt in worktree: %q, %v", b, err)
	}
	if _, err := os.Stat(filepath.Join(repo, "sub", "dir", "new.txt")); err == nil {
		t.Error("new.txt written to the main checkout")
	}
	for _, p := range []string{"staged.txt", "untracked.txt"} {
		if _, err := os.Stat(filepath.Join(wt, p)); err == nil {
			t.Errorf("uncommitted %s carried into the worktree", p)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(wt, "AGENTS.md")); string(b) != "WT-AGENTS-COMMITTED\n" {
		t.Errorf("worktree AGENTS.md = %q", b)
	}
	if got := gitT(t, repo, "status", "--porcelain"); got != statusBefore {
		t.Errorf("main status changed:\n%s\nwant:\n%s", got, statusBefore)
	}
	if got := gitT(t, repo, "diff", "--cached"); got != cachedBefore {
		t.Error("main index changed")
	}
	if got := gitT(t, repo, "symbolic-ref", "HEAD"); got != branchBefore {
		t.Errorf("main branch %s, want %s", got, branchBefore)
	}
	if b, _ := os.ReadFile(filepath.Join(repo, "AGENTS.md")); string(b) != "MAIN-AGENTS-DIRTY\n" {
		t.Error("main AGENTS.md changed")
	}
	if n := strings.Count(stderr, "uncommitted changes"); n != 1 {
		t.Errorf("dirty warning printed %d times:\n%s", n, stderr)
	}
	if !strings.Contains(stderr, "worktree: "+wt+" (branch tinyagent/a from "+base[:7]+")") || !strings.Contains(stderr, "worktree kept: "+wt+" (branch tinyagent/a)") {
		t.Errorf("stderr lacks worktree lines:\n%s", stderr)
	}
	body, _ := json.Marshal(f.bodies[0])
	if !strings.Contains(string(body), "WT-AGENTS-COMMITTED") || strings.Contains(string(body), "MAIN-AGENTS-DIRTY") {
		t.Error("AGENTS.md not discovered from the worktree")
	}
	want := map[string]any{"path": wt, "branch": "tinyagent/a", "base_commit": base, "repo_root": repo}
	ev := runStarted(t, stdout)
	got, _ := ev["worktree"].(map[string]any)
	for k, v := range want {
		if got[k] != v {
			t.Errorf("run.started worktree.%s = %v, want %v", k, got[k], v)
		}
	}
	if ev["root"] != wtRoot {
		t.Errorf("run.started root = %v, want %s", ev["root"], wtRoot)
	}
	tr, err := trajectory.Load(out)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Worktree == nil || *tr.Worktree != (trajectory.WorktreeInfo{Path: wt, Branch: "tinyagent/a", BaseCommit: base, RepoRoot: repo}) || tr.Root != wtRoot {
		t.Errorf("trajectory worktree %+v root %s", tr.Worktree, tr.Root)
	}

	// Rewind and resume through the worktree path, without --worktree.
	stderr = captureStderr(t, func() {
		code, _, _ = runWith(t, "--rewind", out, "--cwd", wtRoot, "--dry-run")
	})
	if code != 0 {
		t.Fatalf("rewind via worktree path: exit %d\n%s", code, stderr)
	}
	f.replies = []string{responsesSubmit}
	next := filepath.Join(t.TempDir(), "next.json")
	stderr = captureStderr(t, func() {
		code, _, _ = runWith(t, "--resume", out, "-t", "more", "-m", "openai/gpt-5", "--openai-api", "responses", "--no-stream", "-c", emptyCfg, "--cwd", wtRoot, "--snapshots", "-o", next)
	})
	if code != 0 {
		t.Fatalf("follow-up via worktree path: exit %d\n%s", code, stderr)
	}
	if tr, err := trajectory.Load(next); err != nil || tr.Worktree == nil || tr.Worktree.Branch != "tinyagent/a" {
		t.Errorf("continued trajectory worktree: %+v, %v", tr, err)
	}
}

// TestWorktreeKeptOnError checks the worktree survives a failed run.
func TestWorktreeKeptOnError(t *testing.T) {
	repo := worktreeRepo(t)
	_, emptyCfg := openaiAPISetup(t)
	f := &fakeResponses{replies: []string{`400 {"error":{"message":"bad"}}`}}
	t.Setenv("OPENAI_BASE_URL", f.start(t).URL)
	var code int
	stderr := captureStderr(t, func() {
		code, _, _ = runWith(t, "-t", "x", "-m", "openai/gpt-5", "--openai-api", "responses", "-c", emptyCfg, "--cwd", repo, "--worktree=err")
	})
	wt := filepath.Join(repo, ".tinyagent", "worktrees", "err")
	if code == 0 {
		t.Fatalf("exit 0; stderr:\n%s", stderr)
	}
	if fi, err := os.Stat(wt); err != nil || !fi.IsDir() {
		t.Errorf("worktree removed after an error: %v", err)
	}
	gitT(t, repo, "rev-parse", "--verify", "tinyagent/err")
	if !strings.Contains(stderr, "worktree kept: "+wt) {
		t.Errorf("summary lacks the worktree:\n%s", stderr)
	}
}

// TestWorktreeSetupErrorsExit2 checks each refused setup exits 2 before any
// provider request and leaves no new directory or branch.
func TestWorktreeSetupErrorsExit2(t *testing.T) {
	repo := worktreeRepo(t)
	_, emptyCfg := openaiAPISetup(t)
	// An existing worktree directory and an existing branch.
	os.MkdirAll(filepath.Join(repo, ".tinyagent", "worktrees", "taken"), 0o755)
	gitT(t, repo, "branch", "tinyagent/dup")
	untracked := filepath.Join(repo, "untracked-dir")
	os.MkdirAll(untracked, 0o755)
	plain := t.TempDir()
	empty, _ := filepath.EvalSymlinks(t.TempDir())
	gitT(t, empty, "init", "-q")
	noGit := t.TempDir()
	traj := filepath.Join(t.TempDir(), "t.json")
	os.WriteFile(traj, []byte("{}"), 0o644)
	for _, c := range []struct {
		name, cwd, path, want string
		args                  []string
	}{
		{"not a repo", plain, "", "not inside a Git work tree", []string{"--worktree=a"}},
		{"no commits", empty, "", "has no commits", []string{"--worktree=a"}},
		{"git missing", repo, noGit, "requires git on PATH", []string{"--worktree=a"}},
		{"invalid name", repo, "", "is invalid", []string{"--worktree=-x"}},
		{"existing directory", repo, "", "already exists", []string{"--worktree=taken"}},
		{"existing branch", repo, "", "branch tinyagent/dup already exists", []string{"--worktree=dup"}},
		{"subdirectory missing at HEAD", untracked, "", "doesn't exist at HEAD", []string{"--worktree=sub"}},
		{"with resume", repo, "", "can't be combined", []string{"--worktree", "--resume", traj}},
		{"with rewind", repo, "", "can't be combined", []string{"--worktree=a", "--rewind", traj}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeResponses{replies: []string{responsesSubmit}}
			t.Setenv("OPENAI_BASE_URL", f.start(t).URL)
			if c.path != "" {
				t.Setenv("PATH", c.path)
			}
			branches := gitT(t, repo, "branch", "--list")
			entries, _ := os.ReadDir(filepath.Join(repo, ".tinyagent", "worktrees"))
			var code int
			stderr := captureStderr(t, func() {
				code, _, _ = runWith(t, append([]string{"-t", "x", "-m", "openai/gpt-5", "--openai-api", "responses", "-c", emptyCfg, "--cwd", c.cwd}, c.args...)...)
			})
			if code != 2 || len(f.bodies) != 0 || !strings.Contains(stderr, c.want) {
				t.Fatalf("exit %d, %d requests, want 2, 0 and %q; stderr:\n%s", code, len(f.bodies), c.want, stderr)
			}
			if got := gitT(t, repo, "branch", "--list"); got != branches {
				t.Errorf("branches changed:\n%s", got)
			}
			after, _ := os.ReadDir(filepath.Join(repo, ".tinyagent", "worktrees"))
			if len(after) != len(entries) {
				t.Errorf("worktrees dir changed: %v -> %v", entries, after)
			}
			for _, d := range []string{plain, empty} {
				if _, err := os.Stat(filepath.Join(d, ".tinyagent")); err == nil {
					t.Errorf("%s/.tinyagent created", d)
				}
			}
		})
	}
}

// TestWorktreeSetupFailureRollsBack checks a failing git worktree add leaves
// nothing behind.
func TestWorktreeSetupFailureRollsBack(t *testing.T) {
	repo := worktreeRepo(t)
	// A ref that makes branch creation fail: tinyagent/x/... can't coexist
	// with a branch tinyagent/x, so tinyagent/x conflicts with tinyagent/x/y.
	gitT(t, repo, "branch", "tinyagent/x/y")
	_, _, err := setupWorktree(repo, "x", &strings.Builder{}, false)
	if err == nil || !strings.Contains(err.Error(), "creating the worktree failed") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo, ".tinyagent")); err == nil {
		t.Error(".tinyagent left behind")
	}
	if strings.Contains(gitT(t, repo, "worktree", "list"), ".tinyagent") {
		t.Error("worktree still registered")
	}
}

// TestWorktreeConcurrent creates two worktrees at once and checks their
// files stay separate.
func TestWorktreeConcurrent(t *testing.T) {
	repo := worktreeRepo(t)
	var wg sync.WaitGroup
	roots := make([]string, 2)
	errs := make([]error, 2)
	for i, name := range []string{"one", "two"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			roots[i], _, errs[i] = setupWorktree(repo, name, &strings.Builder{}, false)
		}()
	}
	wg.Wait()
	for i := range roots {
		if errs[i] != nil {
			t.Fatalf("setup %d: %v", i, errs[i])
		}
		os.WriteFile(filepath.Join(roots[i], "only.txt"), []byte(roots[i]), 0o644)
	}
	for i := range roots {
		b, _ := os.ReadFile(filepath.Join(roots[i], "only.txt"))
		if string(b) != roots[i] {
			t.Errorf("worktree %d sees %q", i, b)
		}
	}
	if _, err := os.Stat(filepath.Join(repo, "only.txt")); err == nil {
		t.Error("main checkout changed")
	}
	if got := gitT(t, repo, "status", "--porcelain"); got != "" {
		t.Errorf("git status in main lists:\n%s", got)
	}
}

// TestNoWorktreeStartsNoGit runs without --worktree and with no git on
// PATH: the run succeeds and records no worktree.
func TestNoWorktreeStartsNoGit(t *testing.T) {
	dir, emptyCfg := openaiAPISetup(t)
	f := &fakeResponses{replies: []string{responsesSubmit}}
	t.Setenv("OPENAI_BASE_URL", f.start(t).URL)
	t.Setenv("PATH", t.TempDir())
	out := filepath.Join(t.TempDir(), "run.json")
	var code int
	var stdout string
	stderr := captureStderr(t, func() {
		stdout = captureStdout(t, func() {
			code, _, _ = runWith(t, "-t", "x", "-m", "openai/gpt-5", "--openai-api", "responses", "-c", emptyCfg, "--cwd", dir, "--json", "-o", out)
		})
	})
	if code != 0 {
		t.Fatalf("exit %d\n%s", code, stderr)
	}
	if _, ok := runStarted(t, stdout)["worktree"]; ok || strings.Contains(stderr, "worktree") {
		t.Error("worktree reported without --worktree")
	}
	b, _ := os.ReadFile(out)
	if strings.Contains(string(b), `"worktree"`) {
		t.Error("trajectory has a worktree key")
	}
}
