package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vicgupta/tinyagent/internal/agent"
)

// malformedOpenAI always answers with a read_file call whose arguments are
// not valid JSON.
func malformedOpenAI(t *testing.T, requests *atomic.Int32) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requests.Add(1)
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"c`+string(rune('a'+n))+`","type":"function","function":{"name":"read_file","arguments":"{\"path\":"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	t.Setenv("OPENAI_API_KEY", "fake")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}

func TestMaxFormatErrorsFlagValidation(t *testing.T) { // AC8
	var requests atomic.Int32
	malformedOpenAI(t, &requests)
	for _, v := range []string{"-1", "0", "abc", "1.5"} {
		code, _, stderr := runWith(t, "--max-format-errors", v, "-t", "x", "-m", "openai/gpt-4o", "--no-stream")
		if code != 2 || !strings.Contains(stderr, "max-format-errors") {
			t.Errorf("--max-format-errors %s: exit %d, stderr %q; want 2", v, code, stderr)
		}
	}
	dir := t.TempDir()
	for _, v := range []string{"-1", "0", "abc", "1.5"} {
		cfg := filepath.Join(dir, "c.yaml")
		os.WriteFile(cfg, []byte("max_format_errors: "+v+"\n"), 0o644)
		var code int
		stderr := captureStderr(t, func() {
			code, _, _ = runWith(t, "-c", cfg, "-t", "x", "-m", "openai/gpt-4o", "--no-stream", "--cwd", dir)
		})
		if code == 0 || !strings.Contains(stderr, "max_format_errors") {
			t.Errorf("config max_format_errors: %s: exit %d, stderr %q", v, code, stderr)
		}
	}
	if n := requests.Load(); n != 0 {
		t.Errorf("%d provider requests, want 0", n)
	}
}

func runMalformed(t *testing.T, args ...string) (int, int32, string, string) {
	t.Helper()
	var requests atomic.Int32
	malformedOpenAI(t, &requests)
	dir := t.TempDir()
	var code int
	var stderr string
	stdout := captureStdout(t, func() {
		stderr = captureStderr(t, func() {
			code, _, _ = runWith(t, append([]string{"-t", "x", "-m", "openai/gpt-4o", "--no-stream", "--cwd", dir, "--step-limit", "20"}, args...)...)
		})
	})
	return code, requests.Load(), stdout, stderr
}

func TestRepeatedFormatErrorCLI(t *testing.T) { // AC5, AC7
	code, n, _, stderr := runMalformed(t)
	if code == 0 || n != 3 || !strings.Contains(stderr, agent.StatusFormatError+" after 3 steps") || !strings.Contains(stderr, "3 format errors") {
		t.Errorf("default: exit %d, %d requests, stderr %q", code, n, stderr)
	}
	if code, n, _, _ := runMalformed(t, "--max-format-errors", "1"); code == 0 || n != 1 {
		t.Errorf("--max-format-errors 1: exit %d, %d requests", code, n)
	}
	cfg := filepath.Join(t.TempDir(), "c.yaml")
	os.WriteFile(cfg, []byte("max_format_errors: 5\n"), 0o644)
	if code, n, _, _ := runMalformed(t, "-c", cfg); code == 0 || n != 5 {
		t.Errorf("config 5: exit %d, %d requests", code, n)
	}
	if code, n, _, _ := runMalformed(t, "-c", cfg, "--max-format-errors", "2"); code == 0 || n != 2 {
		t.Errorf("flag overrides config: exit %d, %d requests", code, n)
	}
}

func TestRepeatedFormatErrorJSON(t *testing.T) { // AC10
	_, _, stdout, _ := runMalformed(t, "--json")
	var formatResults int
	var last map[string]any
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("stdout line is not JSON: %q", line)
		}
		if ev["type"] == "tool.result" && ev["format_error"] == true {
			formatResults++
		}
		last = ev
	}
	if formatResults != 3 || last["type"] != "result" || last["status"] != agent.StatusFormatError || last["format_errors"] != float64(3) {
		t.Errorf("format tool.results %d, last event %v", formatResults, last)
	}
}
