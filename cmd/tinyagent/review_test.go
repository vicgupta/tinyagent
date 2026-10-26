package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/tools"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// reviewUser returns the first user message of a Chat Completions request
// body.
func reviewUser(t *testing.T, body string) string {
	t.Helper()
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content any    `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatal(err)
	}
	for _, m := range req.Messages {
		if m.Role == "user" {
			if s, ok := m.Content.(string); ok {
				return s
			}
			b, _ := json.Marshal(m.Content)
			return string(b)
		}
	}
	t.Fatalf("no user message in %s", body)
	return ""
}

// loadTrajectoryJSON decodes a saved trajectory without checkpoint
// validation.
func loadTrajectoryJSON(t *testing.T, path string) *trajectory.Trajectory {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var tr trajectory.Trajectory
	if err := json.Unmarshal(b, &tr); err != nil {
		t.Fatal(err)
	}
	return &tr
}

// commitT commits everything staged in dir with message msg.
func commitT(t *testing.T, dir, msg string) string {
	t.Helper()
	gitT(t, dir, "commit", "-q", "-m", msg)
	return gitT(t, dir, "rev-parse", "HEAD")
}

func writeT(t *testing.T, dir, name, data string) {
	t.Helper()
	p := filepath.Join(dir, name)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReviewSelectionErrors(t *testing.T) {
	repo := worktreeRepo(t)
	f := newEnvOpenAI(t, "sk-x")
	for _, c := range []struct {
		args []string
		want []string
	}{
		{[]string{"--review", "--review-base", "main"}, []string{"--review", "--review-base"}},
		{[]string{"--review-base", "main", "--review-commit", "HEAD"}, []string{"--review-base", "--review-commit"}},
		{[]string{"--review", "--read-only=false"}, []string{"--review", "--read-only=false"}},
		{[]string{"--review", "--resume", "x.json"}, []string{"--review", "--resume"}},
		{[]string{"--review-commit", "HEAD", "--rewind", "x.json"}, []string{"--review-commit", "--rewind"}},
		{[]string{"--review", "--worktree"}, []string{"--review", "--worktree"}},
		{[]string{"--review", "--snapshots", "-o", "x.json"}, []string{"--review", "--snapshots"}},
		{[]string{"--review", "--check"}, []string{"--review", "--check"}},
		{[]string{"--review", "--init-config"}, []string{"--review", "--init-config"}},
		{[]string{"--review", "--update"}, []string{"--review", "--update"}},
		{[]string{"--review-base", "--evil"}, []string{"--review-base", "can't start with -"}},
		{[]string{"--review-commit", ""}, []string{"--review-commit", "needs a Git ref"}},
	} {
		args := append([]string{"-m", "openai/gpt-4o", "--cwd", repo}, c.args...)
		code, _, stderr := runCLI(t, args...)
		if code != 2 {
			t.Errorf("%v: exit %d, want 2; stderr %s", c.args, code, stderr)
		}
		for _, w := range c.want {
			if !strings.Contains(stderr, w) {
				t.Errorf("%v: stderr lacks %q: %s", c.args, w, stderr)
			}
		}
	}
	if len(f.bodies) != 0 {
		t.Errorf("%d provider requests", len(f.bodies))
	}
}

func TestReviewRepositoryErrors(t *testing.T) {
	repo := worktreeRepo(t)
	empty, _ := filepath.EvalSymlinks(t.TempDir())
	gitT(t, empty, "init", "-q")
	// An orphan branch has no merge base with the default branch.
	gitT(t, repo, "branch", "-M", "main")
	gitT(t, repo, "checkout", "-q", "--orphan", "other")
	commitT(t, repo, "orphan")
	f := newEnvOpenAI(t, "sk-x")
	for _, c := range []struct {
		cwd  string
		args []string
		want string
	}{
		{t.TempDir(), []string{"--review"}, "not inside a Git work tree"},
		{empty, []string{"--review"}, "has no commits"},
		{repo, []string{"--review-base", "nope"}, `--review-base "nope": not a commit`},
		{repo, []string{"--review-commit", "nope"}, `--review-commit "nope": not a commit`},
		{repo, []string{"--review-base", "main"}, `--review-base "main": no merge base`},
	} {
		args := append([]string{"-m", "openai/gpt-4o", "--cwd", c.cwd}, c.args...)
		code, _, stderr := runCLI(t, args...)
		if code != 2 || !strings.Contains(stderr, c.want) {
			t.Errorf("%v in %s: exit %d, want 2 and %q; stderr %s", c.args, c.cwd, code, c.want, stderr)
		}
	}
	if len(f.bodies) != 0 {
		t.Errorf("%d provider requests", len(f.bodies))
	}
}

// TestReviewUncommitted covers --review end to end: staged, unstaged and
// untracked changes are sent, ignored files are not, focus instructions
// follow the review instructions, and the model's write attempts are
// refused, leaving the working tree and index untouched.
func TestReviewUncommitted(t *testing.T) {
	repo := worktreeRepo(t)
	writeT(t, repo, ".gitignore", "ignored.txt\n")
	gitT(t, repo, "add", ".gitignore")
	commitT(t, repo, "ignore")
	writeT(t, repo, "staged.txt", "STAGED-LINE\n")
	gitT(t, repo, "add", "staged.txt")
	writeT(t, repo, "sub/dir/keep.txt", "keep\nUNSTAGED-LINE\n")
	writeT(t, repo, "untracked.txt", "UNTRACKED-LINE\n")
	writeT(t, repo, "ignored.txt", "IGNORED-LINE\n")
	status, cached := gitT(t, repo, "status", "--porcelain"), gitT(t, repo, "diff", "--cached")

	f := newEnvOpenAI(t, "sk-x", "echo pwned > pwned.txt")
	traj := filepath.Join(t.TempDir(), "run.json")
	code, stdout, stderr := runCLI(t, "-m", "openai/gpt-4o", "--no-stream", "--cwd", repo, "--review", "-t", "focus on FOCUS-TEXT", "--json", "-o", traj)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	msg := reviewUser(t, f.bodies[0])
	for _, w := range []string{"STAGED-LINE", "UNSTAGED-LINE", "UNTRACKED-LINE", "A staged.txt (+1 -0)", "M sub/dir/keep.txt (+1 -0)", "A untracked.txt (+1 -0)", "file:line"} {
		if !strings.Contains(msg, w) {
			t.Errorf("first user message lacks %q:\n%s", w, msg)
		}
	}
	if strings.Contains(msg, "ignored.txt") || strings.Contains(msg, "IGNORED-LINE") {
		t.Errorf("ignored file sent:\n%s", msg)
	}
	if i, j := strings.Index(msg, "Review the code changes"), strings.Index(msg, "Additional review instructions:\nfocus on FOCUS-TEXT"); i != 0 || j < 0 || j < strings.Index(msg, "--- END DIFF ---") {
		t.Errorf("focus not after the review instructions and diff:\n%s", msg)
	}
	if !strings.Contains(f.bodies[1], "read-only mode") {
		t.Errorf("bash not refused: %s", f.bodies[1])
	}
	if _, err := os.Stat(filepath.Join(repo, "pwned.txt")); err == nil {
		t.Error("bash ran")
	}
	if s, c := gitT(t, repo, "status", "--porcelain"), gitT(t, repo, "diff", "--cached"); s != status || c != cached {
		t.Errorf("working tree or index changed:\n%s\n%s", s, c)
	}

	head := gitT(t, repo, "rev-parse", "HEAD")
	ev := runStarted(t, stdout)
	rv, _ := ev["review"].(map[string]any)
	if rv["target"] != "uncommitted" || rv["head_commit"] != head || rv["files_changed"] != float64(3) || rv["truncated"] != false || ev["read_only"] != true {
		t.Errorf("run.started = %v", ev)
	}
	tr := loadTrajectoryJSON(t, traj)
	if r := tr.Review; r == nil || r.Target != "uncommitted" || r.HeadCommit != head || r.DiffBytes != r.DiffBytesSent || r.DiffBytes == 0 || !tr.ReadOnly {
		t.Errorf("trajectory review = %+v", tr.Review)
	}
}

func TestReviewBaseAndCommit(t *testing.T) {
	repo := worktreeRepo(t)
	gitT(t, repo, "branch", "-M", "main")
	root := gitT(t, repo, "rev-parse", "HEAD")
	gitT(t, repo, "checkout", "-q", "-b", "feature")
	writeT(t, repo, "a.txt", "FEATURE-ONE\n")
	gitT(t, repo, "add", "a.txt")
	commitT(t, repo, "feature one")
	writeT(t, repo, "b.txt", "FEATURE-TWO\n")
	gitT(t, repo, "add", "b.txt")
	second := commitT(t, repo, "feature two subject")
	gitT(t, repo, "checkout", "-q", "main")
	writeT(t, repo, "main.txt", "MAIN-LATER\n")
	gitT(t, repo, "add", "main.txt")
	commitT(t, repo, "main later")
	gitT(t, repo, "checkout", "-q", "feature")
	writeT(t, repo, "dirty.txt", "DIRTY\n")

	f := newEnvOpenAI(t, "sk-x")
	traj := filepath.Join(t.TempDir(), "run.json")
	code, _, stderr := runCLI(t, "-m", "openai/gpt-4o", "--no-stream", "--cwd", repo, "--review-base", "main", "-o", traj, "-v")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	msg := reviewUser(t, f.bodies[0])
	if !strings.Contains(msg, "FEATURE-ONE") || !strings.Contains(msg, "FEATURE-TWO") || strings.Contains(msg, "MAIN-LATER") || strings.Contains(msg, "DIRTY") {
		t.Errorf("base diff:\n%s", msg)
	}
	if !strings.Contains(stderr, "uncommitted changes are not part of --review-base") || !strings.Contains(stderr, "review: target=base ref=main") {
		t.Errorf("stderr:\n%s", stderr)
	}
	tr := loadTrajectoryJSON(t, traj)
	if r := tr.Review; r == nil || r.BaseCommit != root || r.HeadCommit != second || r.Ref != "main" || r.FilesChanged != 2 {
		t.Errorf("review = %+v", tr.Review)
	}

	// A non-root commit, then the root commit (against the empty tree).
	for _, c := range []struct{ rev, want, not, subject string }{
		{second, "FEATURE-TWO", "FEATURE-ONE", "feature two subject"},
		{root, "WT-AGENTS-COMMITTED", "FEATURE", "init"},
	} {
		f := newEnvOpenAI(t, "sk-x")
		code, _, stderr := runCLI(t, "-m", "openai/gpt-4o", "--no-stream", "--cwd", repo, "--review-commit", c.rev)
		if code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		msg := reviewUser(t, f.bodies[0])
		show := gitT(t, repo, "show", "--format=", "--no-color", c.rev)
		if !strings.Contains(msg, c.want) || strings.Contains(msg, c.not) || !strings.Contains(msg, "Commit subject: "+c.subject) || !strings.Contains(msg, strings.TrimSpace(show)) {
			t.Errorf("commit %s:\n%s\ngit show:\n%s", c.rev, msg, show)
		}
	}
}

// TestReviewNoRepoPrograms checks that collecting each target runs none of
// the programs repository config can name: an external diff, a textconv
// driver, a filter driver and an fsmonitor hook.
func TestReviewNoRepoPrograms(t *testing.T) {
	repo := worktreeRepo(t)
	marker := filepath.Join(t.TempDir(), "marker")
	writeT(t, repo, ".gitattributes", "*.txt diff=evil filter=evil\n")
	gitT(t, repo, "add", ".gitattributes")
	writeT(t, repo, "x.txt", "one\n")
	gitT(t, repo, "add", "x.txt")
	commitT(t, repo, "attrs")
	for k, v := range map[string]string{
		"diff.external":       "touch " + marker + " #",
		"diff.evil.textconv":  "touch " + marker + "; cat",
		"diff.evil.command":   "touch " + marker + " #",
		"filter.evil.clean":   "touch " + marker + "; cat",
		"filter.evil.smudge":  "touch " + marker + "; cat",
		"filter.evil.process": "touch " + marker,
		"core.fsmonitor":      "touch " + marker + "; false #",
	} {
		gitT(t, repo, "config", k, v)
	}
	writeT(t, repo, "x.txt", "one\ntwo\n")
	writeT(t, repo, "new.txt", "new\n")
	for _, target := range []*reviewTarget{{kind: reviewUncommitted}, {kind: reviewBase, ref: "HEAD~1"}, {kind: reviewCommit, ref: "HEAD"}} {
		msg, _, err := collectReview(target, repo, "", nil, io.Discard, false)
		if err != nil {
			t.Fatalf("%s: %v", target.kind, err)
		}
		if _, err := os.Stat(marker); err == nil {
			t.Fatalf("%s: a repository-configured program ran", target.kind)
		}
		if !strings.Contains(msg, "diff --git a/x.txt b/x.txt") {
			t.Errorf("%s: no built-in diff:\n%s", target.kind, msg)
		}
	}
}

func TestReviewFileAccessAndCap(t *testing.T) {
	repo := worktreeRepo(t)
	writeT(t, repo, ".env", "SECRET=old\n")
	gitT(t, repo, "add", ".env")
	commitT(t, repo, "env")
	writeT(t, repo, ".env", "SECRET=NEW-SECRET-VALUE\n")
	writeT(t, repo, "small.txt", "small\n")
	writeT(t, repo, "big.txt", strings.Repeat("BIG-LINE\n", 200))

	cfg := writeConfig(t, "file_access:\n  deny_read: [\".env\"]\n")
	f := newEnvOpenAI(t, "sk-x")
	traj := filepath.Join(t.TempDir(), "run.json")
	code, _, stderr := runCLI(t, "-m", "openai/gpt-4o", "--no-stream", "-c", cfg, "--cwd", repo, "--review", "-o", traj)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	msg := reviewUser(t, f.bodies[0])
	if strings.Contains(msg, "SECRET") || strings.Contains(msg, ".env") {
		t.Errorf(".env content sent:\n%s", msg)
	}
	if !strings.Contains(msg, "1 changed path is hidden by file_access.deny_read") || !strings.Contains(stderr, "review: 1 changed path hidden by file_access.deny_read") {
		t.Errorf("hidden count not reported:\n%s\n%s", msg, stderr)
	}
	tr := loadTrajectoryJSON(t, traj)
	if r := tr.Review; r == nil || r.HiddenPaths != 1 || r.FilesChanged != 2 {
		t.Errorf("review = %+v", tr.Review)
	}

	// Over the cap: whole per-file diffs only, every file in the summary,
	// left-out files named.
	defer func(n int) { reviewMaxDiffBytes = n }(reviewMaxDiffBytes)
	reviewMaxDiffBytes = 500
	fa, err := tools.NewFileAccess(repo, []string{".env"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var errOut strings.Builder
	msg, info, err := collectReview(&reviewTarget{kind: reviewUncommitted}, repo, "", fa, &errOut, false)
	if err != nil {
		t.Fatal(err)
	}
	diff := msg[strings.Index(msg, "--- BEGIN DIFF ---\n")+19 : strings.Index(msg, "--- END DIFF ---")]
	if !info.Truncated || info.DiffBytesSent > 500 || len(diff) != info.DiffBytesSent || info.DiffBytes <= 500 {
		t.Errorf("info = %+v, %d bytes sent", info, len(diff))
	}
	if !strings.Contains(msg, "A big.txt (+200 -0)") || !strings.Contains(msg, "A small.txt (+1 -0)") || !strings.Contains(msg, "read them with read_file:\n- big.txt\n") || strings.Contains(diff, "BIG-LINE") || !strings.Contains(diff, "+small") {
		t.Errorf("truncated message:\n%s", msg)
	}
	if !strings.Contains(errOut.String(), "review: diff truncated") {
		t.Errorf("stderr: %s", errOut.String())
	}
}

func TestReviewNothingAndSubdir(t *testing.T) {
	repo := worktreeRepo(t)
	f := newEnvOpenAI(t, "sk-x")
	code, _, stderr := runCLI(t, "-m", "openai/gpt-4o", "--cwd", repo, "--review")
	if code != 0 || !strings.Contains(stderr, "nothing to review") || len(f.bodies) != 0 {
		t.Errorf("clean tree: exit %d, %d requests, stderr %s", code, len(f.bodies), stderr)
	}

	// With --cwd a subdirectory, only its changes are reviewed, with paths
	// relative to it; focus can come from stdin.
	writeT(t, repo, "AGENTS.md", "OUTSIDE-CHANGE\n")
	writeT(t, repo, "sub/dir/keep.txt", "keep\nINSIDE-CHANGE\n")
	r, w, _ := os.Pipe()
	w.WriteString("STDIN-FOCUS\n")
	w.Close()
	defer func(f *os.File) { os.Stdin = f }(os.Stdin)
	os.Stdin = r
	code, _, stderr = runCLI(t, "-m", "openai/gpt-4o", "--no-stream", "--cwd", filepath.Join(repo, "sub"), "--review", "-t", "-")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	msg := reviewUser(t, f.bodies[0])
	if strings.Contains(msg, "OUTSIDE-CHANGE") || !strings.Contains(msg, "M dir/keep.txt (+1 -0)") || !strings.Contains(msg, "Only changes under sub are included.") || !strings.HasSuffix(msg, "Additional review instructions:\nSTDIN-FOCUS\n") {
		t.Errorf("subdirectory review:\n%s", msg)
	}
}
