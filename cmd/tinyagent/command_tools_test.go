package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/vicgupta/tinyagent/internal/agent"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// toolCallOpenAI is a fake OpenAI server: each request pops the next reply
// (a [name, arguments] tool call, or none to submit) and records the
// request bodies.
type toolCallOpenAI struct {
	mu      sync.Mutex
	replies [][2]string
	bodies  []string
}

func newToolCallOpenAI(t *testing.T, replies ...[2]string) *toolCallOpenAI {
	t.Helper()
	f := &toolCallOpenAI{replies: replies}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.bodies = append(f.bodies, string(b))
		msg := map[string]any{"role": "assistant", "content": "done\n" + agent.SubmitMarker}
		if len(f.replies) > 0 {
			c := f.replies[0]
			f.replies = f.replies[1:]
			msg = map[string]any{"role": "assistant", "content": "", "tool_calls": []any{map[string]any{
				"id": "c" + string(rune('a'+len(f.bodies))), "type": "function",
				"function": map[string]any{"name": c[0], "arguments": c[1]}}}}
		}
		json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": msg, "finish_reason": "stop"}},
			"usage": map[string]any{"prompt_tokens": 1, "completion_tokens": 1}})
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	t.Setenv("OPENAI_API_KEY", "sk-test")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	return f
}

// runTestsConfig declares run_tests running a script that prints its argv.
func runTestsConfig(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake executable is a shell script")
	}
	exe := filepath.Join(t.TempDir(), "fakego")
	if err := os.WriteFile(exe, []byte("#!/bin/sh\nfor a in \"$@\"; do printf '<%s>' \"$a\"; done\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return writeConfig(t, `command_tools:
  - name: run_tests
    description: Run Go tests for the given packages.
    parameters:
      type: object
      properties:
        packages: {type: array, items: {type: string, pattern: "^\\./"}, minItems: 1}
      required: [packages]
      additionalProperties: false
    argv: [`+exe+`, test, -count=1, "{{packages}}"]
`)
}

func TestCommandToolsCLI(t *testing.T) {
	cfg := runTestsConfig(t)
	dir := t.TempDir()
	f := newToolCallOpenAI(t, [2]string{"run_tests", `{"packages":["./pkg/a","./pkg/b"]}`})
	traj := filepath.Join(t.TempDir(), "run.json")
	code, stdout, stderr := runCLI(t, "-t", "x", "-m", "openai/gpt-4o", "--no-stream", "--cwd", dir, "-c", cfg, "-v", "-o", traj, "--json")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if len(f.bodies) != 2 {
		t.Fatalf("requests: %d", len(f.bodies))
	}
	if !strings.Contains(f.bodies[0], `"name":"run_tests"`) || !strings.Contains(f.bodies[0], "Run Go tests for the given packages.") {
		t.Errorf("run_tests not offered: %s", f.bodies[0])
	}
	if !strings.Contains(strings.NewReplacer(`\u003c`, "<", `\u003e`, ">").Replace(f.bodies[1]), "<test><-count=1><./pkg/a><./pkg/b>") {
		t.Errorf("argv not as expected: %s", f.bodies[1])
	}
	if !strings.Contains(stderr, `command tool run_tests: [`) {
		t.Errorf("-v argv line missing: %s", stderr)
	}
	var started, call map[string]any
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		var ev map[string]any
		json.Unmarshal([]byte(line), &ev)
		switch ev["type"] {
		case "run.started":
			started = ev
		case "tool.call":
			call = ev
		}
	}
	if names, _ := json.Marshal(started["command_tools"]); string(names) != `["run_tests"]` {
		t.Errorf("run.started command_tools = %s", names)
	}
	if call["name"] != "run_tests" {
		t.Errorf("tool.call = %v", call)
	}
	tr, err := trajectory.Load(traj)
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.CommandTools) != 1 || tr.CommandTools[0].Name != "run_tests" || tr.CommandTools[0].Argv[3] != "{{packages}}" || tr.CommandTools[0].SchemaDigest == "" {
		t.Errorf("trajectory command_tools = %+v", tr.CommandTools)
	}

	// Read-only runs are not offered command tools.
	f = newToolCallOpenAI(t)
	code, _, stderr = runCLI(t, "-t", "x", "-m", "openai/gpt-4o", "--no-stream", "--cwd", dir, "-c", cfg, "--read-only")
	if code != 0 || strings.Contains(f.bodies[0], "run_tests") || !strings.Contains(stderr, "command tools are not offered") {
		t.Errorf("read-only: exit %d, %s", code, stderr)
	}

	// --check counts them.
	code, stdout, _ = runCLI(t, "--check", "-m", "openai/gpt-4o", "--cwd", dir, "-c", cfg)
	if code != 0 || !strings.Contains(stdout, "1 command tools (run_tests)") {
		t.Errorf("--check: %d %s", code, stdout)
	}
}

func TestCommandToolsInvalidConfigExits2(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []struct{ yaml, want string }{
		{"command_tools:\n  - {name: bash, description: d, argv: [ls]}\n", "built-in"},
		{"command_tools:\n  - {name: t, description: d, argv: [\"{{x}}\"]}\n", "argv[0]"},
		{"command_tools:\n  - {name: t, description: d, argv: [ls], dir: ../..}\n", "dir"},
	} {
		f := newToolCallOpenAI(t)
		code, _, stderr := runCLI(t, "-t", "x", "-m", "openai/gpt-4o", "--no-stream", "--cwd", dir, "-c", writeConfig(t, c.yaml))
		if code != 2 || !strings.Contains(stderr, c.want) || len(f.bodies) != 0 {
			t.Errorf("%s: exit %d, %d requests, %s", c.yaml, code, len(f.bodies), stderr)
		}
	}
	// Unknown keys are rejected like any other unknown config key.
	code, _, stderr := runCLI(t, "-t", "x", "-m", "openai/gpt-4o", "--cwd", dir, "-c", writeConfig(t, "command_tools:\n  - {name: t, description: d, argv: [ls], bogus: 1}\n"))
	if code == 0 || !strings.Contains(stderr, "bogus") {
		t.Errorf("unknown key: exit %d, %s", code, stderr)
	}
}
