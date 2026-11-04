package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/agent"
)

// dotenvDir writes body to <dir>/.env and makes sure the variables it names
// are unset now and restored after the test.
func dotenvDir(t *testing.T, body string, keys ...string) string {
	t.Helper()
	for _, k := range keys {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	return dotenvDirAt(t, t.TempDir(), body)
}

func dotenvDirAt(t *testing.T, dir, body string, keys ...string) string {
	t.Helper()
	for _, k := range keys {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestDotenvChatUsesModelFromFile runs a real chat session whose model,
// base URL and key come only from ./.env.
func TestDotenvChatUsesModelFromFile(t *testing.T) {
	root := acpWorkspace(t)
	p := newACPProvider(t, acpReply{text: "hi.\n" + agent.SubmitMarker})
	url := os.Getenv("OPENAI_BASE_URL")
	const sentinel = "sk-dotenv-chat-sentinel"
	dotenvDirAt(t, root, "AGENT_MODEL=openai/gpt-4o-mini\nOPENAI_BASE_URL="+url+"\nOPENAI_API_KEY="+sentinel+"\n", "AGENT_MODEL", "OPENAI_BASE_URL", "OPENAI_API_KEY")
	if err := loadDotenv(root, false); err != nil {
		t.Fatal(err)
	}
	r := runChatTest(t, root, strings.NewReader("hello\n/exit\n"), nil)
	if r.code != 0 {
		t.Fatalf("exit %d\n%s", r.code, r.stderr)
	}
	reqs := p.requests()
	if len(reqs) == 0 {
		t.Fatal("no model requests")
	}
	if got := reqs[len(reqs)-1]["model"]; got != "gpt-4o-mini" {
		t.Errorf("chat model = %v, want gpt-4o-mini from .env", got)
	}
	if strings.Contains(r.out+r.stderr, sentinel) {
		t.Errorf(".env secret leaked")
	}
}

func TestDotenvSuppliesModelAndKeyToCheck(t *testing.T) {
	checkEnv(t)
	const sentinel = "sk-ant-DOTENV-SENTINEL"
	dir := dotenvDir(t, "AGENT_MODEL=anthropic/claude-sonnet-4-5\nANTHROPIC_API_KEY="+sentinel+"\n", "AGENT_MODEL", "ANTHROPIC_API_KEY")
	for _, args := range [][]string{{"--check"}} {
		code, stdout, stderr := runCLI(t, append(args, "--cwd", dir, "-v")...)
		if code != 0 {
			t.Fatalf("%v: exit %d; stderr:\n%s", args, code, stderr)
		}
		if strings.Contains(stdout+stderr, sentinel) {
			t.Fatalf("%v: .env secret leaked:\n%s%s", args, stdout, stderr)
		}
		if got := reportLine(t, stdout, "model"); got != "anthropic/claude-sonnet-4-5 (from $AGENT_MODEL)" {
			t.Errorf("%v: model line %q", args, got)
		}
		if got := reportLine(t, stdout, "provider"); got != "anthropic, ANTHROPIC_API_KEY is set" {
			t.Errorf("%v: provider line %q", args, got)
		}
		os.Unsetenv("AGENT_MODEL")
		os.Unsetenv("ANTHROPIC_API_KEY")
	}
}

func TestDotenvRealEnvAndFlagsWin(t *testing.T) {
	checkEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "real")
	dir := dotenvDir(t, "AGENT_MODEL=openai/gpt-4o-mini\n", "AGENT_MODEL")
	t.Setenv("AGENT_MODEL", "anthropic/claude-sonnet-4-5")
	code, stdout, stderr := runCLI(t, "--check", "--cwd", dir)
	if code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, stderr)
	}
	if got := reportLine(t, stdout, "model"); got != "anthropic/claude-sonnet-4-5 (from $AGENT_MODEL)" {
		t.Errorf("real env should win over .env; model line %q", got)
	}
	code, stdout, stderr = runCLI(t, "--check", "--cwd", dir, "-m", "anthropic/claude-haiku-4-5")
	if code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, stderr)
	}
	if got := reportLine(t, stdout, "model"); got != "anthropic/claude-haiku-4-5 (from -m)" {
		t.Errorf("-m should win; model line %q", got)
	}
}

func TestDotenvMissingIsFine(t *testing.T) {
	checkEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "real")
	code, _, stderr := runCLI(t, "--check", "-m", "anthropic/claude-sonnet-4-5", "--cwd", t.TempDir())
	if code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, stderr)
	}
}

func TestDotenvMalformedFailsWithoutValue(t *testing.T) {
	checkEnv(t)
	dir := dotenvDir(t, "oops secretvalue\n")
	code, _, stderr := runCLI(t, "--check", "--cwd", dir)
	if code != 2 || strings.Contains(stderr, "secretvalue") || !strings.Contains(stderr, ".env:1") {
		t.Fatalf("exit %d; stderr:\n%s", code, stderr)
	}
}
