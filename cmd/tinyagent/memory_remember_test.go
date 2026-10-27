package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/vicgupta/tinyagent/internal/memory"
)

// rememberServer is a fake model: the first request of each run answers with
// a remember call, later requests finish the task. Every request body is kept.
func rememberServer(t *testing.T, text string) (bodies func() []string, reset func()) {
	t.Helper()
	var mu sync.Mutex
	var got []string
	args, _ := json.Marshal(map[string]string{"op": "add", "text": text})
	call, _ := json.Marshal(string(args))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, string(b))
		first := len(got) == 1
		mu.Unlock()
		if first && strings.Contains(string(b), `"name":"remember"`) {
			io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"remember","arguments":`+string(call)+`}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
			return
		}
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"done\nCOMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	t.Setenv("OPENAI_API_KEY", "fake")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	return func() []string {
			mu.Lock()
			defer mu.Unlock()
			return append([]string(nil), got...)
		}, func() {
			mu.Lock()
			got = nil
			mu.Unlock()
		}
}

func TestRememberToolSavesForNextRun(t *testing.T) {
	cwd, store := memoryEnv(t)
	const note = "integration tests need TINY_FAKE=1"
	bodies, reset := rememberServer(t, note)
	cfg := writeMemoryConfig(t, "memory:\n  enabled: true\n  approval: auto\n")

	if code, stderr := runMemoryTask(t, cwd, "-c", cfg); code != 0 {
		t.Fatalf("run A: exit %d, stderr %q", code, stderr)
	}
	reqs := bodies()
	if len(reqs) < 2 {
		t.Fatalf("run A made %d requests, want the remember call and a follow-up", len(reqs))
	}
	if !strings.Contains(reqs[1], "saved memory entry") {
		t.Error("run A: remember result missing from the follow-up request")
	}
	if strings.Contains(reqs[1], "# Project memory") {
		t.Error("run A: the new entry reached this run's system prompt")
	}
	entries, err := memory.Load(store)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Text != note || entries[0].Status != memory.StatusActive {
		t.Fatalf("store = %+v, want one active entry", entries)
	}

	reset()
	if code, stderr := runMemoryTask(t, cwd, "-c", cfg); code != 0 {
		t.Fatalf("run B: exit %d, stderr %q", code, stderr)
	}
	if reqs := bodies(); len(reqs) == 0 || !strings.Contains(reqs[0], note) {
		t.Error("run B: saved entry missing from the system prompt")
	}
}

func TestRememberToolPendingByDefault(t *testing.T) {
	cwd, store := memoryEnv(t)
	bodies, _ := rememberServer(t, "prefer table-driven tests")
	if code, stderr := runMemoryTask(t, cwd, "--memory"); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if reqs := bodies(); len(reqs) < 2 || !strings.Contains(reqs[1], "pending") {
		t.Error("remember result does not say the entry is pending")
	}
	entries, err := memory.Load(store)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Status != memory.StatusPending {
		t.Fatalf("store = %+v, want one pending entry", entries)
	}
}

func TestRememberToolNotOfferedReadOnly(t *testing.T) {
	cwd, store := memoryEnv(t)
	bodies, _ := rememberServer(t, "should not be saved")
	if code, stderr := runMemoryTask(t, cwd, "--memory", "--read-only"); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	for _, b := range bodies() {
		if strings.Contains(b, `"name":"remember"`) {
			t.Fatal("read-only run offered the remember tool")
		}
	}
	if entries, _ := memory.Load(store); len(entries) != 0 {
		t.Fatalf("read-only run wrote memory: %+v", entries)
	}
}
