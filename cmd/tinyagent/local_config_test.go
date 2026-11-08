package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/agent"
)

// localConfigWorkspace returns a workspace whose only model setting that
// counts is model: in .tinyagent/tinyagent.yaml. A .env and an old root
// tinyagent.yaml name other models; neither may be read.
func localConfigWorkspace(t *testing.T) string {
	t.Helper()
	root := acpWorkspace(t)
	writeFile(t, filepath.Join(root, ".tinyagent", "tinyagent.yaml"), "model: openai/gpt-4o-mini\n")
	writeFile(t, filepath.Join(root, "tinyagent.yaml"), "model: openai/legacy-root-model\n")
	writeFile(t, filepath.Join(root, ".env"), "AGENT_MODEL=openai/dotenv-model\nmodel=openai/dotenv-model\nOPENAI_API_KEY=sk-dotenv-sentinel\n")
	return root
}

func lastModel(t *testing.T, p *acpProvider) any {
	t.Helper()
	reqs := p.requests()
	if len(reqs) == 0 {
		t.Fatal("no model requests")
	}
	return reqs[len(reqs)-1]["model"]
}

// #406: .tinyagent/tinyagent.yaml in --cwd is the config in every mode
// (one-shot, chat, web, ACP); a cwd .env and ./tinyagent.yaml are ignored.
func TestLocalConfigFirstAcrossModes(t *testing.T) {
	t.Run("one-shot", func(t *testing.T) {
		p := newACPProvider(t)
		root := localConfigWorkspace(t)
		code, _, stderr := runCLI(t, "-t", "hi", "--cwd", root, "--no-stream")
		if code != 0 {
			t.Fatalf("exit %d: %s", code, stderr)
		}
		if got := lastModel(t, p); got != "gpt-4o-mini" {
			t.Errorf("model = %v, want gpt-4o-mini from .tinyagent/tinyagent.yaml", got)
		}
		if !strings.Contains(stderr, "is no longer read") {
			t.Errorf("no hint for the old ./tinyagent.yaml: %q", stderr)
		}
	})
	t.Run("chat", func(t *testing.T) {
		p := newACPProvider(t, acpReply{text: "hi.\n" + agent.SubmitMarker})
		root := localConfigWorkspace(t)
		r := runChatTest(t, root, strings.NewReader("hello\n/exit\n"), nil)
		if r.code != 0 {
			t.Fatalf("exit %d\n%s", r.code, r.stderr)
		}
		if got := lastModel(t, p); got != "gpt-4o-mini" {
			t.Errorf("chat model = %v", got)
		}
	})
	t.Run("web", func(t *testing.T) {
		newACPProvider(t)
		root := localConfigWorkspace(t)
		c := startWebArgs(t, root)
		if st := c.state(); st["model"] != "openai/gpt-4o-mini" {
			t.Errorf("web model = %v", st["model"])
		}
	})
	t.Run("acp", func(t *testing.T) {
		p := newACPProvider(t, acpReply{text: "ok\n" + agent.SubmitMarker})
		root := localConfigWorkspace(t)
		c := startACP(t)
		c.wait(c.call("initialize", map[string]any{"protocolVersion": 1}))
		sess := c.newSession(root)
		if stop, r, _ := c.prompt(sess, "hi"); stop != "end_turn" {
			t.Fatalf("prompt: %q %v", stop, r)
		}
		if got := lastModel(t, p); got != "gpt-4o-mini" {
			t.Errorf("acp model = %v", got)
		}
	})
}

// #406: .env support is gone. A cwd .env sets nothing (not the model, not a
// key), a malformed one is not an error, and -v never mentions it.
func TestDotenvFileNotRead(t *testing.T) {
	checkEnv(t)
	t.Setenv("OPENAI_API_KEY", "k") // the default model's key, from the real environment
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, ".env"), "AGENT_MODEL=anthropic/claude-sonnet-4-5\nANTHROPIC_API_KEY=sk-ant-DOTENV\nmodel=anthropic/claude-haiku-4-5\n")
	code, stdout, stderr := runCLI(t, "--check", "--cwd", dir, "-v")
	if code != 0 || strings.Contains(stdout+stderr, "anthropic") || strings.Contains(stdout+stderr, ".env") {
		t.Fatalf("exit %d: .env was read:\n%s%s", code, stdout, stderr)
	}
	if got := reportLine(t, stdout, "model"); got != builtinModel+" (default)" {
		t.Errorf("model line %q", got)
	}
	for _, k := range []string{"AGENT_MODEL", "ANTHROPIC_API_KEY", "model"} {
		if v := os.Getenv(k); v != "" {
			t.Errorf("%s set from .env: %q", k, v)
		}
	}
	writeFile(t, filepath.Join(dir, ".env"), "oops not a dotenv line\n")
	if code, _, stderr := runCLI(t, "--check", "--cwd", dir); code != 0 || strings.Contains(stderr, ".env") {
		t.Errorf("malformed .env: exit %d, %q", code, stderr)
	}
	if b, err := os.ReadFile(filepath.Join(dir, ".env")); err != nil || len(b) == 0 {
		t.Errorf("user's .env touched: %v", err)
	}
}

// #406: loading .tinyagent/tinyagent.yaml, and tinyagent init, keep it out of
// broad staging through .tinyagent/.gitignore, leaving the repository's own
// ignore files alone.
func TestLocalConfigKeptOutOfCommits(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	cwdEnv(t)
	gitIn := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	newRepo := func() string {
		dir := t.TempDir()
		gitIn(dir, "init", "-q")
		writeFile(t, filepath.Join(dir, ".gitignore"), "build/\n")
		writeFile(t, filepath.Join(dir, "main.go"), "package main\n")
		return dir
	}

	// An existing config: any command that loads it adds the ignore rule.
	dir := newRepo()
	writeFile(t, filepath.Join(dir, ".tinyagent", "tinyagent.yaml"), "step_limit: 9\n")
	if code, _, stderr := runCLI(t, "--check", "--cwd", dir); code != 0 {
		t.Fatalf("check: exit %d %s", code, stderr)
	}
	gitIn(dir, "add", ".")
	if staged := gitIn(dir, "diff", "--cached", "--name-only"); strings.Contains(staged, "tinyagent.yaml") || !strings.Contains(staged, "main.go") {
		t.Errorf("git add . staged:\n%s", staged)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, ".gitignore")); string(b) != "build/\n" {
		t.Errorf("repository .gitignore changed: %q", b)
	}

	// init writes the starter and the ignore rule, and says so.
	dir = newRepo()
	code, _, stderr := runCLI(t, "init", "--cwd", dir)
	if code != 0 || !strings.Contains(stderr, "never commit it") || !strings.Contains(stderr, ".tinyagent/.gitignore") {
		t.Fatalf("init: exit %d, %q", code, stderr)
	}
	gitIn(dir, "add", "-A")
	if staged := gitIn(dir, "diff", "--cached", "--name-only"); strings.Contains(staged, "tinyagent.yaml") {
		t.Errorf("git add -A after init staged:\n%s", staged)
	}
	if strings.Contains(starterConfig, "may be committed") || !strings.Contains(starterConfig, "never commit it") {
		t.Errorf("starter config text does not say to keep it out of commits")
	}
}
