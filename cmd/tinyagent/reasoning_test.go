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
)

// fakeOpenAIBodies serves one submitting reply per request and records each
// request body.
func fakeOpenAIBodies(t *testing.T, bodies *[]map[string]any) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		*bodies = append(*bodies, b)
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestReasoningEffortFlagOverridesConfig(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(cfg, []byte("reasoning_effort: low\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	traj := filepath.Join(dir, "run.json")
	for _, c := range []struct {
		name string
		args []string
		want any
	}{
		{"unset", nil, nil},
		{"config", []string{"-c", cfg}, "low"},
		{"flag wins", []string{"-c", cfg, "--reasoning-effort", "medium", "-o", traj}, "medium"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var bodies []map[string]any
			srv := fakeOpenAIBodies(t, &bodies)
			t.Setenv("OPENAI_BASE_URL", srv.URL)
			t.Setenv("OPENAI_API_KEY", "fake")
			args := append([]string{"-t", "x", "--no-stream", "--cwd", dir, "-m", "openai/gpt-5"}, c.args...)
			if c.want == nil {
				args = append(args, "-c", filepath.Join(dir, "missing.yaml"))
				os.WriteFile(filepath.Join(dir, "missing.yaml"), nil, 0o644)
			}
			var code int
			stderr := captureStderr(t, func() { code, _, _ = runWith(t, args...) })
			if code != 0 || len(bodies) != 1 {
				t.Fatalf("exit %d, %d requests; stderr:\n%s", code, len(bodies), stderr)
			}
			if got := bodies[0]["reasoning_effort"]; got != c.want {
				t.Errorf("reasoning_effort = %v, want %v", got, c.want)
			}
			if _, ok := bodies[0]["reasoning"]; ok {
				t.Errorf("unexpected reasoning key: %v", bodies[0])
			}
		})
	}
	data, err := os.ReadFile(traj)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"reasoning_effort": "medium"`) {
		t.Errorf("trajectory does not record the effort:\n%s", data)
	}
}

func TestBadReasoningEffortExits2(t *testing.T) {
	dir := t.TempDir()
	for _, c := range []struct {
		name, want string
		args       []string
	}{
		{"bogus", "--reasoning-effort", []string{"-m", "openai/gpt-5", "--reasoning-effort", "bogus"}},
		{"ollama", "ollama provider", []string{"-m", "ollama/llama3.1", "--reasoning-effort", "low"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			var requests atomic.Int32
			srv := fakeOpenAI(t, &requests)
			t.Setenv("OPENAI_BASE_URL", srv.URL)
			t.Setenv("OLLAMA_HOST", srv.URL)
			t.Setenv("OPENAI_API_KEY", "fake")
			var code int
			stderr := captureStderr(t, func() {
				code, _, _ = runWith(t, append([]string{"-t", "x", "--no-stream", "--cwd", dir}, c.args...)...)
			})
			if code != 2 || requests.Load() != 0 || !strings.Contains(stderr, c.want) {
				t.Fatalf("exit %d, %d requests, stderr %q; want 2, 0, containing %q", code, requests.Load(), stderr, c.want)
			}
		})
	}
}
