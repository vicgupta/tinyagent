package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestTranscriptOfRealRun renders a trajectory written by a real run, with
// the provider gone and no API key set.
func TestTranscriptOfRealRun(t *testing.T) { // AC1, AC4, AC10
	if runtime.GOOS == "windows" {
		t.Skip("runs echo through bash")
	}
	const secret = "supersecretvalue"
	f := newEnvOpenAI(t, "test", "echo "+secret+" ghp_abcdefghijklmnopqrstuvwxyz0123")
	dir := t.TempDir()
	traj := filepath.Join(t.TempDir(), "run.json")
	if code, _, stderr := runCLI(t, "-t", "say hi", "-m", "openai/gpt-4o", "--no-stream", "--cwd", dir, "-o", traj); code != 0 {
		t.Fatalf("run exit %d: %s", code, stderr)
	}
	requests := len(f.bodies)
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("OPENAI_BASE_URL", "http://127.0.0.1:1")
	t.Setenv("FOO_TOKEN", secret)
	before, _ := os.ReadDir(dir)

	code, out, stderr := runCLI(t, "--transcript", traj)
	if code != 0 || stderr != "" {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	for _, s := range []string{"say hi", "`openai/gpt-4o`", "`Submitted`", "**Steps:** 2", "**Tokens:**",
		"### Step 1\n", "### Step 2\n", "**Tool call:** `bash`", "## Submission", "echo [REDACTED] [REDACTED]"} {
		if !strings.Contains(out, s) {
			t.Errorf("transcript lacks %q:\n%s", s, out)
		}
	}
	if strings.Contains(out, secret) || strings.Contains(out, "ghp_") {
		t.Errorf("secret leaked:\n%s", out)
	}
	if len(f.bodies) != requests {
		t.Errorf("--transcript made %d model requests", len(f.bodies)-requests)
	}
	if after, _ := os.ReadDir(dir); len(after) != len(before) {
		t.Errorf("--transcript wrote to the workspace: %v", after)
	}
	if _, again, _ := runCLI(t, "--transcript", traj); again != out {
		t.Error("output differs between renders")
	}
}

func TestTranscriptErrorsExit2(t *testing.T) { // AC8
	dir := t.TempDir()
	write := func(name, data string) string {
		p := filepath.Join(dir, name)
		mustWrite(t, p, data, 0o600)
		return p
	}
	big := filepath.Join(dir, "big.json")
	if err := os.WriteFile(big, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(big, 64<<20+1); err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"missing": filepath.Join(dir, "nope.json"),
		"dir":     dir,
		"empty":   write("empty.json", ""),
		"invalid": write("bad.json", `{"task": "sk-leakedcontent`),
		"big":     big,
		"v99":     write("v99.json", `{"schema_version": 99}`),
	}
	for name, p := range cases {
		code, stdout, stderr := runCLI(t, "--transcript", p)
		if code != 2 || stdout != "" || strings.Count(stderr, "\n") != 1 || !strings.HasPrefix(stderr, "error: transcript: ") || strings.Contains(stderr, "leakedcontent") {
			t.Errorf("%s: exit %d, stdout %q, stderr %q", name, code, stdout, stderr)
		}
	}
}

func TestTranscriptRejectsRunFlags(t *testing.T) { // AC9
	p := filepath.Join(t.TempDir(), "run.json")
	mustWrite(t, p, `{"task":"x","messages":[]}`, 0o600)
	for _, args := range [][]string{{"-t", "x"}, {"--resume", "r.json"}, {"--rewind", "r.json"}, {"--check"},
		{"--json"}, {"-o", "out.json"}, {"--init-config"}, {"--update"}, {"--completion", "bash"}} {
		code, stdout, stderr := runCLI(t, append([]string{"--transcript", p}, args...)...)
		if code != 2 || stdout != "" || !strings.Contains(stderr, "can't be combined with "+args[0]) {
			t.Errorf("%v: exit %d, stdout %q, stderr %q", args, code, stdout, stderr)
		}
	}
}
