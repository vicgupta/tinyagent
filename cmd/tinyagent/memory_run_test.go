package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vicgupta/tinyagent/internal/agent"
	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/memory"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// memoryRunEnv starts a fake model that submits at once and records the
// last request body, isolates config and state, and returns a project
// directory, its store path and the recorded body.
func memoryRunEnv(t *testing.T) (cwd, store string, body func() string) {
	t.Helper()
	var mu sync.Mutex
	var last string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		last = string(b)
		mu.Unlock()
		io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"done\nCOMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}))
	t.Cleanup(srv.Close)
	cwd, store = memoryEnv(t)
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	t.Setenv("OPENAI_API_KEY", "fake")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	return cwd, store, func() string {
		mu.Lock()
		defer mu.Unlock()
		return last
	}
}

func runMemoryTask(t *testing.T, cwd string, args ...string) (code int, stderr string) {
	t.Helper()
	code, _, stderr = runMemoryTaskOut(t, cwd, args...)
	return
}

func runMemoryTaskOut(t *testing.T, cwd string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	stdout = captureStdout(t, func() {
		stderr = captureStderr(t, func() {
			var flagErr string
			code, _, flagErr = runWith(t, append([]string{"-t", "x", "-m", "openai/gpt-4o", "--no-stream", "--cwd", cwd}, args...)...)
			os.Stderr.WriteString(flagErr)
		})
	})
	return
}

func writeMemoryConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestMemoryRunLoadsActiveEntries(t *testing.T) {
	cwd, store, body := memoryRunEnv(t)
	at := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	seedMemory(t, store,
		testEntry("act1", memory.StatusActive, "use make test-fast for unit tests", at),
		testEntry("pen1", memory.StatusPending, "pending-note-should-not-load", at))
	on := writeMemoryConfig(t, "memory:\n  enabled: true\n")
	for _, c := range []struct {
		args []string
		want bool
	}{
		{nil, false},
		{[]string{"--memory"}, true},
		{[]string{"-c", on}, true},
		{[]string{"-c", on, "--memory=false"}, false},
	} {
		code, stderr := runMemoryTask(t, cwd, c.args...)
		got := body()
		if code != 0 {
			t.Fatalf("%v: exit %d, stderr %q", c.args, code, stderr)
		}
		if strings.Contains(got, "use make test-fast") != c.want || strings.Contains(got, "# Project memory") != c.want {
			t.Errorf("%v: memory in prompt = %v, want %v", c.args, !c.want, c.want)
		}
		if strings.Contains(got, "pending-note-should-not-load") {
			t.Errorf("%v: pending entry reached the prompt", c.args)
		}
		if c.want && !strings.Contains(got, "[act1, 2026-10-20]") {
			t.Errorf("%v: entry line missing id and date", c.args)
		}
	}
}

func TestMemoryRunOffIsUnchanged(t *testing.T) {
	cwd, store, body := memoryRunEnv(t)
	if code, stderr := runMemoryTask(t, cwd, "--read-only"); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	off := body()
	// With memory on but an empty store, the request is identical too. Read-only
	// keeps the remember tool out of both catalogs.
	if code, stderr := runMemoryTask(t, cwd, "--memory", "--read-only"); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if body() != off {
		t.Error("memory on with an empty store changed the request")
	}
	// Outside read-only, memory on adds remember, memory_search and memory_list.
	if code, stderr := runMemoryTask(t, cwd, "--memory"); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if !strings.Contains(body(), `"name":"remember"`) {
		t.Error("memory on did not offer the remember tool")
	}
	if _, err := os.Stat(store); !os.IsNotExist(err) {
		t.Errorf("store was created (err %v)", err)
	}
}

func TestMemoryRunOmittedWarningAndVerbose(t *testing.T) {
	cwd, store, body := memoryRunEnv(t)
	at := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	seedMemory(t, store,
		testEntry("old1", memory.StatusActive, "older note "+strings.Repeat("x", 300), at),
		testEntry("new1", memory.StatusActive, "newest note", at.Add(time.Hour)))
	cfg := writeMemoryConfig(t, "memory:\n  enabled: true\n  max_bytes: 400\n")
	code, stdout, stderr := runMemoryTaskOut(t, cwd, "-c", cfg, "-v")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if !strings.Contains(stdout+stderr, "project memory: loaded 1 entry (1 omitted) from "+store) {
		t.Errorf("-v lacks the loaded line: stdout %q stderr %q", stdout, stderr)
	}
	if !strings.Contains(stderr, "1 older entry omitted") {
		t.Errorf("stderr lacks the omitted warning: %q", stderr)
	}
	if strings.Count(stderr, "omitted to stay within memory.max_bytes") != 1 {
		t.Errorf("omitted warning not printed exactly once: %q", stderr)
	}
	got := body()
	if !strings.Contains(got, "newest note") || strings.Contains(got, "older note") {
		t.Error("prompt should hold the newest entry only")
	}
	if !strings.Contains(got, "1 older memory entry omitted") {
		t.Error("prompt section lacks the omitted note")
	}
}

func TestMemoryRunCorruptStoreContinues(t *testing.T) {
	cwd, store, body := memoryRunEnv(t)
	os.MkdirAll(filepath.Dir(store), 0o700)
	if err := os.WriteFile(store, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stderr := runMemoryTask(t, cwd, "--memory")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if strings.Count(stderr, "project memory:") != 1 || !strings.Contains(stderr, "continuing without memory") {
		t.Errorf("want one corrupt-store warning, stderr %q", stderr)
	}
	if strings.Contains(body(), "# Project memory") {
		t.Error("corrupt store produced a memory section")
	}
	if b, _ := os.ReadFile(store); string(b) != "{not json" {
		t.Error("corrupt store was rewritten")
	}
}

func TestMemoryRunInvalidConfigExits2(t *testing.T) {
	cwd, _, _ := memoryRunEnv(t)
	cfg := writeMemoryConfig(t, "memory:\n  approval: always\n")
	if code, stderr := runMemoryTask(t, cwd, "-c", cfg); code != 2 || !strings.Contains(stderr, "memory.approval") {
		t.Errorf("exit %d, stderr %q", code, stderr)
	}
}

func TestMemorySnapshotSurvivesStoreChanges(t *testing.T) {
	cwd, store, _ := memoryRunEnv(t)
	at := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	seedMemory(t, store, testEntry("act1", memory.StatusActive, "snapshot note", at))
	out := filepath.Join(t.TempDir(), "run.json")
	if code, stderr := runMemoryTask(t, cwd, "--memory", "-o", out); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	tr, err := trajectory.Load(out)
	if err != nil {
		t.Fatal(err)
	}
	if !tr.MemoryEnabled || len(tr.Memory) != 1 || tr.Memory[0].ID != "act1" || tr.Memory[0].Date != "2026-10-20" || tr.Memory[0].Text != "snapshot note" {
		t.Fatalf("snapshot = %v %+v", tr.MemoryEnabled, tr.Memory)
	}
	if !strings.Contains(tr.SystemPrompt, "snapshot note") {
		t.Fatal("system prompt lacks the memory entry")
	}
	// Changing or wiping the store doesn't change the resume prompt: it
	// is rebuilt from the snapshot.
	seedMemory(t, store, testEntry("act2", memory.StatusActive, "later note", at.Add(time.Hour)))
	cfg := config.Default()
	// The checkpoint records the canonical root; on macOS the temp dir
	// is under /var, a symlink to /private/var.
	if cwd, err = filepath.EvalSymlinks(cwd); err != nil {
		t.Fatal(err)
	}
	if err := agent.ValidateResume(tr, cfg, cwd); err != nil {
		t.Errorf("resume after store change: %v", err)
	}
	os.Remove(store)
	if err := agent.ValidateResume(tr, cfg, cwd); err != nil {
		t.Errorf("resume after store removal: %v", err)
	}
	// A tampered snapshot is caught.
	tr.Memory[0].Text = "tampered"
	if err := agent.ValidateResume(tr, cfg, cwd); err == nil {
		t.Error("resume accepted a snapshot that doesn't match the prompt")
	}

	// A run with memory off records nothing.
	off := filepath.Join(t.TempDir(), "off.json")
	if code, stderr := runMemoryTask(t, cwd, "-o", off); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	b, _ := os.ReadFile(off)
	if strings.Contains(string(b), `"memory`) {
		t.Error("memory-off trajectory has memory fields")
	}
}

func TestMemoryResumeConflict(t *testing.T) {
	cwd, store, _ := memoryRunEnv(t)
	seedMemory(t, store, testEntry("act1", memory.StatusActive, "note", time.Now()))
	on := filepath.Join(t.TempDir(), "on.json")
	if code, stderr := runMemoryTask(t, cwd, "--memory", "-o", on); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	off := filepath.Join(t.TempDir(), "off.json")
	if code, stderr := runMemoryTask(t, cwd, "-o", off); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	for _, c := range []struct {
		src  string
		flag string
	}{{on, "--memory=false"}, {off, "--memory"}} {
		next := filepath.Join(t.TempDir(), "next.json")
		var code int
		var stderr string
		captureStdout(t, func() {
			stderr = captureStderr(t, func() {
				var flagErr string
				code, _, flagErr = runWith(t, "--resume", c.src, "--cwd", cwd, "-o", next, "-t", "more", "--no-stream", c.flag)
				os.Stderr.WriteString(flagErr)
			})
		})
		if code != 2 || !strings.Contains(stderr, "--memory conflicts") {
			t.Errorf("%s %s: exit %d, stderr %q", filepath.Base(c.src), c.flag, code, stderr)
		}
	}
}
