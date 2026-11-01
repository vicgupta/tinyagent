package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/vicgupta/tinyagent/internal/agent"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// envOpenAI is a fake OpenAI server: each request pops the next reply
// (a bash command, or "" to submit) and records the request bodies and
// Authorization headers.
type envOpenAI struct {
	mu      sync.Mutex
	replies []string
	bodies  []string
	auth    []string
}

func newEnvOpenAI(t *testing.T, key string, replies ...string) *envOpenAI {
	t.Helper()
	f := &envOpenAI{replies: replies}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.bodies = append(f.bodies, string(b))
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		cmd := ""
		if len(f.replies) > 0 {
			cmd, f.replies = f.replies[0], f.replies[1:]
		}
		msg := map[string]any{"role": "assistant", "content": "done\n" + agent.SubmitMarker}
		if cmd != "" {
			args, _ := json.Marshal(map[string]string{"command": cmd})
			msg = map[string]any{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{
				"id": "c" + string(rune('a'+len(f.bodies))), "type": "function",
				"function": map[string]any{"name": "bash", "arguments": string(args)}}}}
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": msg, "finish_reason": "stop"}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1}})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	t.Setenv("OPENAI_API_KEY", key)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir()) // os.UserConfigDir ignores XDG_CONFIG_HOME on macOS
	return f
}

func writeConfig(t *testing.T, data string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// runCLI runs the CLI and returns the exit code, stdout and stderr.
func runCLI(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	stderr = captureStderr(t, func() {
		stdout = captureStdout(t, func() {
			var flagErr string
			code, _, flagErr = runWith(t, args...)
			os.Stderr.WriteString(flagErr)
		})
	})
	return code, stdout, stderr
}

func TestCommandEnvCLI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("runs env through bash")
	}
	const key, withheld, setVal = "sk-provider-sentinel-1234", "withheld-sentinel-5678", "set-sentinel-9012"
	t.Setenv("MY_SERVICE_TOKEN", withheld)
	t.Setenv("FOO", "bar")
	t.Setenv("GITHUB_TOKEN", "gh-value")
	t.Setenv("AWS_REGION", "us-east-1")
	dir := t.TempDir()
	cfg := writeConfig(t, "command_env:\n  exclude_secrets: true\n  exclude: [\"AWS_*\"]\n  pass: [GITHUB_TOKEN, NOT_SET_ANYWHERE]\n  set: {CI: \"true\", SET_SENTINEL: \""+setVal+"\"}\n")

	// The model's command sees the filtered environment; the provider still
	// gets the key.
	f := newEnvOpenAI(t, key, "env")
	code, _, stderr := runCLI(t, "-t", "x", "-m", "openai/gpt-4o", "--no-stream", "--cwd", dir, "-c", cfg)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if len(f.bodies) != 2 || f.auth[0] != "Bearer "+key {
		t.Fatalf("requests %d, auth %q", len(f.bodies), f.auth)
	}
	out := f.bodies[1]
	for _, s := range []string{"FOO=bar", "GITHUB_TOKEN=gh-value", "CI=true"} {
		if !strings.Contains(out, s) {
			t.Errorf("command env lacks %s", s)
		}
	}
	for _, s := range []string{key, withheld, "AWS_REGION", "NOT_SET_ANYWHERE"} {
		if strings.Contains(out, s) {
			t.Errorf("command env has %s", s)
		}
	}

	// Names only, never values, in stderr, -v, -o and --json.
	newEnvOpenAI(t, key)
	traj := filepath.Join(t.TempDir(), "run.json")
	code, stdout, stderr := runCLI(t, "-t", "x", "-m", "openai/gpt-4o", "--no-stream", "--cwd", dir, "-c", cfg, "-v", "-o", traj, "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if n := strings.Count(stderr, "command env: "); n != 1 || !strings.Contains(stderr, "command env: all, secrets excluded; ") {
		t.Errorf("stderr has %d summary lines:\n%s", n, stderr)
	}
	if !strings.Contains(stderr, "command env pass not set: NOT_SET_ANYWHERE") || !regexp.MustCompile(`command env withheld: .*MY_SERVICE_TOKEN`).MatchString(stderr) {
		t.Errorf("-v names missing:\n%s", stderr)
	}
	b, _ := os.ReadFile(traj)
	for _, s := range []string{stderr, stdout, string(b)} {
		for _, v := range []string{key, withheld, setVal} {
			if strings.Contains(s, v) {
				t.Errorf("value %s leaked into %q", v, s)
			}
		}
	}
	var tr trajectory.Trajectory
	json.Unmarshal(b, &tr)
	if ce := tr.CommandEnv; ce == nil || ce.Inherit != "all" || !ce.ExcludeSecrets || !contains(ce.Passed, "SET_SENTINEL") || contains(ce.Passed, "MY_SERVICE_TOKEN") {
		t.Errorf("trajectory command_env = %+v", tr.CommandEnv)
	}
	var started map[string]any
	json.Unmarshal([]byte(strings.SplitN(stdout, "\n", 2)[0]), &started)
	if ce, ok := started["command_env"].(map[string]any); !ok || ce["inherit"] != "all" {
		t.Errorf("run.started = %v", started)
	}

	// --command-env overrides the config's inherit.
	newEnvOpenAI(t, key)
	code, _, stderr = runCLI(t, "-t", "x", "-m", "openai/gpt-4o", "--no-stream", "--cwd", dir, "-c", writeConfig(t, "command_env: {inherit: all}\n"), "--command-env", "core", "-o", traj+"2")
	if code != 0 || !strings.Contains(stderr, "command env: core;") {
		t.Errorf("--command-env core: exit %d, %s", code, stderr)
	}
}

func TestCommandEnvDefaultUnchanged(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("runs env through bash")
	}
	f := newEnvOpenAI(t, "sk-default-key-abcdef", "env")
	t.Setenv("FOO", "bar")
	dir := t.TempDir()
	traj := filepath.Join(t.TempDir(), "run.json")
	code, stdout, stderr := runCLI(t, "-t", "x", "-m", "openai/gpt-4o", "--no-stream", "--cwd", dir, "-o", traj, "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(f.bodies[1], "OPENAI_API_KEY=sk-default-key-abcdef") || !strings.Contains(f.bodies[1], "FOO=bar") {
		t.Error("default command environment changed")
	}
	b, _ := os.ReadFile(traj)
	if strings.Contains(stderr, "command env") || strings.Contains(string(b), "command_env") || strings.Contains(stdout, "command_env") {
		t.Errorf("policy output without a policy:\n%s\n%s", stderr, stdout)
	}
}

func TestCommandEnvValidation(t *testing.T) {
	f := newEnvOpenAI(t, "k")
	dir := t.TempDir()
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--command-env", "bogus"}, "--command-env"},
		{[]string{"--command-env", ""}, "--command-env"},
		{[]string{"-c", writeConfig(t, "command_env: {inherit: bogus}\n")}, "command_env.inherit"},
		{[]string{"-c", writeConfig(t, "command_env: {unknown_key: 1}\n")}, "unknown_key"},
		{[]string{"-c", writeConfig(t, "command_env: {pass: [\"\"]}\n"), "--read-only"}, "command_env.pass"},
		{[]string{"-c", writeConfig(t, "command_env: {exclude: [\"[\"]}\n")}, "command_env.exclude"},
	} {
		code, _, stderr := runCLI(t, append([]string{"-t", "x", "-m", "openai/gpt-4o", "--no-stream", "--cwd", dir}, c.args...)...)
		if code != 2 || !strings.Contains(stderr, c.want) {
			t.Errorf("%v: exit %d, stderr %q", c.args, code, stderr)
		}
	}
	if len(f.bodies) != 0 {
		t.Errorf("%d provider requests", len(f.bodies))
	}
	// Read-only: a valid policy has no effect and prints nothing.
	newEnvOpenAI(t, "k")
	code, _, stderr := runCLI(t, "-t", "x", "-m", "openai/gpt-4o", "--no-stream", "--cwd", dir, "--read-only", "--command-env", "none")
	if code != 0 || strings.Contains(stderr, "command env") {
		t.Errorf("read-only: exit %d, %s", code, stderr)
	}
}

func TestCommandEnvResume(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("runs env through bash")
	}
	t.Setenv("FOO", "bar")
	dir := t.TempDir()
	first := filepath.Join(t.TempDir(), "run.json")
	newEnvOpenAI(t, "k", "true")
	if code, _, stderr := runCLI(t, "-t", "x", "-m", "openai/gpt-4o", "--no-stream", "--cwd", dir, "--command-env", "core", "--step-limit", "1", "-o", first); code == 0 {
		t.Fatalf("first run finished: %s", stderr)
	}
	f := newEnvOpenAI(t, "k", "env")
	second := filepath.Join(t.TempDir(), "run2.json")
	code, _, stderr := runCLI(t, "--resume", first, "-m", "openai/gpt-4o", "--no-stream", "--cwd", dir, "--command-env", "none", "-o", second)
	if code != 0 {
		t.Fatalf("resume exit %d: %s", code, stderr)
	}
	if strings.Contains(f.bodies[1], "FOO=") || strings.Contains(f.bodies[1], "PATH=") {
		t.Error("resumed command did not use the current policy")
	}
	b, _ := os.ReadFile(second)
	var tr trajectory.Trajectory
	json.Unmarshal(b, &tr)
	if tr.CommandEnv == nil || tr.CommandEnv.Inherit != "none" {
		t.Errorf("resumed trajectory command_env = %+v", tr.CommandEnv)
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
