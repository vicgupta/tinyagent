package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// TestMemoryPersistsAcrossRuns is the end-to-end cross-session check: run 1
// saves a fact with remember (approval auto), run 2 is a fresh process-level
// run in the same directory that loads it into its system prompt at start and
// finds it with memory_search.
func TestMemoryPersistsAcrossRuns(t *testing.T) {
	const fact = "integration tests need TA_FAKE_DB=1"
	var mu sync.Mutex
	var bodies []string
	step := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		n := step
		step++
		mu.Unlock()
		var msg string
		switch n {
		case 0: // run 1: save the fact
			msg = `{"role":"assistant","content":"","tool_calls":[{"id":"r1","type":"function","function":{"name":"remember","arguments":"{\"op\":\"add\",\"text\":\"` + fact + `\"}"}}]}`
		case 2: // run 2: look it up through the tool
			msg = `{"role":"assistant","content":"","tool_calls":[{"id":"s1","type":"function","function":{"name":"memory_search","arguments":"{\"query\":\"TA_FAKE_DB\"}"}}]}`
		default:
			msg = `{"role":"assistant","content":"done\nCOMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT"}`
		}
		io.WriteString(w, `{"choices":[{"message":`+msg+`,"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	t.Cleanup(srv.Close)
	cwd, _ := memoryEnv(t)
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	t.Setenv("OPENAI_API_KEY", "fake")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	cfg := writeMemoryConfig(t, "memory:\n  enabled: true\n  approval: auto\n")

	if code, stderr := runMemoryTask(t, cwd, "-c", cfg); code != 0 {
		t.Fatalf("run 1: exit %d, %q", code, stderr)
	}
	mu.Lock()
	if len(bodies) != 2 || strings.Contains(bodies[0], fact) {
		t.Fatalf("run 1: %d requests, fact in first prompt = %v", len(bodies), strings.Contains(bodies[0], fact))
	}
	if !strings.Contains(bodies[1], "saved memory entry") || !strings.Contains(bodies[1], "status: active") {
		t.Fatalf("run 1: remember did not save an active entry: %s", bodies[1])
	}
	mu.Unlock()

	if code, stderr := runMemoryTask(t, cwd, "-c", cfg); code != 0 {
		t.Fatalf("run 2: exit %d, %q", code, stderr)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 4 {
		t.Fatalf("run 2: %d requests total, want 4", len(bodies))
	}
	if !strings.Contains(bodies[2], "# Project memory") || !strings.Contains(bodies[2], fact) {
		t.Errorf("run 2: fact not in the startup prompt")
	}
	i := strings.LastIndex(bodies[3], "memory notes (data saved by earlier runs")
	if i < 0 || !strings.Contains(bodies[3][i:], fact) {
		t.Errorf("run 2: memory_search did not return the fact")
	}
}
