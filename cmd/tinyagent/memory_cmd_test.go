package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/memory"
)

// memoryEnv isolates the store directory and clears provider credentials,
// returning a project directory and its store path.
func memoryEnv(t *testing.T) (cwd, store string) {
	t.Helper()
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	for _, k := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "OPENROUTER_API_KEY", "GEMINI_API_KEY", "AGENT_MODEL"} {
		t.Setenv(k, "")
	}
	isolateMemoryConfig(t)
	cwd = t.TempDir()
	return cwd, testStorePath(t, cwd)
}

// isolateMemoryConfig keeps a user memory.toml and TINYAGENT_MEMORY_*
// variables from leaking into a test.
func isolateMemoryConfig(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	for _, k := range config.MemoryKeys {
		t.Setenv(k.EnvVar(), "")
	}
	t.Setenv("TINYAGENT_MEMORY_CONFIG", "")
}

// testStorePath is the default (markdown) store for cwd.
func testStorePath(t *testing.T, cwd string) string {
	t.Helper()
	root, err := canonicalRoot(cwd)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, ".tinyagent", "memory")
}

func seedMemory(t *testing.T, store string, entries ...memory.Entry) {
	t.Helper()
	if err := memory.Update(store, func([]memory.Entry) ([]memory.Entry, error) { return entries, nil }); err != nil {
		t.Fatal(err)
	}
}

func testEntry(id, status, text string, at time.Time) memory.Entry {
	return memory.Entry{ID: id, Text: text, Status: status, CreatedAt: at, UpdatedAt: at,
		Source: memory.Source{RunID: "run-" + id, Model: "fake/model"}}
}

// runMemory parses args like the CLI and runs the memory command.
func runMemory(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var o options
	fs := newFlagSet(&o)
	var out, errOut strings.Builder
	fs.SetOutput(&errOut)
	if err := fs.Parse(args); err != nil {
		return 2, "", err.Error()
	}
	if !memoryCommandSet(fs) {
		t.Fatalf("%v: no memory command set", args)
	}
	code = runMemoryCommand(&o, fs, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestMemoryPath(t *testing.T) {
	cwd, store := memoryEnv(t)
	code, out, errOut := runMemory(t, "--memory-path", "--cwd", cwd)
	if code != 0 || strings.TrimSpace(out) != store || errOut != "" {
		t.Fatalf("exit %d, stdout %q, stderr %q; want %q", code, out, errOut, store)
	}
	if filepath.Base(store) != "memory" {
		t.Errorf("default store %s is not ./memory", store)
	}
	if _, err := os.Stat(store); !os.IsNotExist(err) {
		t.Errorf("--memory-path created the store: %v", err)
	}
}

func TestMemoryListEmptyAndText(t *testing.T) {
	cwd, store := memoryEnv(t)
	code, out, _ := runMemory(t, "--memory-list", "--cwd", cwd)
	if code != 0 || !strings.Contains(out, "no project memory entries") {
		t.Fatalf("empty list: exit %d, %q", code, out)
	}
	if _, err := os.Stat(store); !os.IsNotExist(err) {
		t.Error("listing an empty project created the store")
	}
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	untrusted := testEntry("mbbbb", memory.StatusPending, "use make db-up first", t0)
	untrusted.Source.UntrustedContext = true
	seedMemory(t, store,
		testEntry("maaaa", memory.StatusActive, "go test misses linux-tagged files", t0),
		untrusted,
		testEntry("mcccc", memory.StatusActive, "newer note", t0.Add(time.Hour)))
	code, out, errOut := runMemory(t, "--memory-list", "--cwd", cwd)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	iB, iC, iA := strings.Index(out, "mbbbb"), strings.Index(out, "mcccc"), strings.Index(out, "maaaa")
	if iB < 0 || iC < 0 || iA < 0 || !(iB < iC && iC < iA) {
		t.Errorf("want pending first, then newest first:\n%s", out)
	}
	for _, want := range []string{"pending", "active", "2026-10-01", "run run-maaaa", "model fake/model", "untrusted context", "go test misses linux-tagged files"} {
		if !strings.Contains(out, want) {
			t.Errorf("list output missing %q:\n%s", want, out)
		}
	}
}

func TestMemoryListJSON(t *testing.T) {
	cwd, store := memoryEnv(t)
	code, out, _ := runMemory(t, "--memory-list", "--json", "--cwd", cwd)
	if code != 0 || strings.TrimSpace(out) != "[]" {
		t.Fatalf("empty JSON list: exit %d, %q", code, out)
	}
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	seedMemory(t, store, testEntry("maaaa", memory.StatusActive, "note one", t0))
	code, out, _ = runMemory(t, "--memory-list", "--json", "--cwd", cwd)
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	var got []map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("not a JSON array: %v\n%s", err, out)
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries", len(got))
	}
	for _, k := range []string{"id", "status", "created_at", "source", "text"} {
		if _, ok := got[0][k]; !ok {
			t.Errorf("JSON entry missing %q: %v", k, got[0])
		}
	}
	if got[0]["created_at"] != "2026-10-01T12:00:00Z" || got[0]["text"] != "note one" {
		t.Errorf("entry = %v", got[0])
	}
}

func TestMemoryApproveAndForget(t *testing.T) {
	cwd, store := memoryEnv(t)
	t0 := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	seedMemory(t, store,
		testEntry("maaaa", memory.StatusPending, "one", t0),
		testEntry("mbbbb", memory.StatusPending, "two", t0),
		testEntry("mcccc", memory.StatusPending, "three", t0))
	if code, out, errOut := runMemory(t, "--memory-approve", "maaaa", "--cwd", cwd); code != 0 || out != "approved maaaa\n" {
		t.Fatalf("approve id: exit %d, %q, %q", code, out, errOut)
	}
	if code, out, _ := runMemory(t, "--memory-approve", "all", "--cwd", cwd); code != 0 || out != "approved 2 pending entries\n" {
		t.Fatalf("approve all: exit %d, %q", code, out)
	}
	if code, out, _ := runMemory(t, "--memory-approve", "all", "--cwd", cwd); code != 0 || out != "approved 0 pending entries\n" {
		t.Fatalf("approve all again: exit %d, %q", code, out)
	}
	entries, err := memory.Load(store)
	if err != nil || len(memory.Active(entries)) != 3 {
		t.Fatalf("after approve: %v %v", entries, err)
	}
	if code, out, _ := runMemory(t, "--memory-forget", "mbbbb", "--cwd", cwd); code != 0 || out != "forgot mbbbb\n" {
		t.Fatalf("forget id: exit %d, %q", code, out)
	}
	_, out, _ := runMemory(t, "--memory-list", "--cwd", cwd)
	if strings.Contains(out, "mbbbb") || !strings.Contains(out, "maaaa") {
		t.Errorf("list after forget:\n%s", out)
	}
	if code, _, errOut := runMemory(t, "--memory-forget", "mzzzz", "--cwd", cwd); code != 2 || !strings.Contains(errOut, `no entry with id "mzzzz"`) {
		t.Errorf("forget unknown: exit %d, %q", code, errOut)
	}
	if code, _, errOut := runMemory(t, "--memory-approve", "mzzzz", "--cwd", cwd); code != 2 || !strings.Contains(errOut, "no entry") {
		t.Errorf("approve unknown: exit %d, %q", code, errOut)
	}
	if code, _, errOut := runMemory(t, "--memory-forget", "all", "--cwd", cwd); code != 2 || !strings.Contains(errOut, "--memory-yes") {
		t.Errorf("forget all without confirmation: exit %d, %q", code, errOut)
	}
	if entries, _ := memory.Load(store); len(entries) != 2 {
		t.Fatalf("unconfirmed forget all changed the store: %v", entries)
	}
	if code, out, _ := runMemory(t, "--memory-forget", "all", "--memory-yes", "--cwd", cwd); code != 0 || out != "forgot 2 entries\n" {
		t.Fatalf("forget all: exit %d, %q", code, out)
	}
	if entries, _ := memory.Load(store); len(entries) != 0 {
		t.Errorf("store not empty: %v", entries)
	}
}

func TestMemoryCorruptStore(t *testing.T) {
	cwd, store := memoryEnv(t)
	if err := os.MkdirAll(store, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(store, "maaaa.md"), []byte("---\nid: [broken\n---\ntext\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"--memory-list"}, {"--memory-approve", "all"}, {"--memory-forget", "maaaa"}, {"--memory-forget", "all", "--memory-yes"},
	} {
		code, _, errOut := runMemory(t, append(args, "--cwd", cwd)...)
		if code != 1 || !strings.Contains(errOut, "error: memory:") {
			t.Errorf("%v on corrupt store: exit %d, %q", args, code, errOut)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(store, "maaaa.md")); string(b) != "---\nid: [broken\n---\ntext\n" {
		t.Errorf("corrupt store changed: %q", b)
	}
}

func TestMemoryCommandConflicts(t *testing.T) {
	cwd, _ := memoryEnv(t)
	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"--memory-list", "-t", "hi"}, "can't be combined with -t"},
		{[]string{"--memory-path", "--resume", "x.json"}, "can't be combined with --resume"},
		{[]string{"--memory-list", "--check"}, "can't be combined with --check"},
		{[]string{"--memory-list", "-m", "openai/gpt"}, "can't be combined with -m"},
		{[]string{"--memory-list", "--memory-path"}, "run one memory command at a time"},
		{[]string{"--memory-yes"}, "--memory-yes applies only"},
		{[]string{"--memory-forget", "maaaa", "--memory-yes"}, "--memory-yes applies only"},
		{[]string{"--memory-path", "--json"}, "--json applies only to --memory-list"},
		{[]string{"--memory-approve", ""}, "needs an entry id"},
		{[]string{"--memory-forget", " "}, "needs an entry id"},
		{[]string{"--memory-list", "extra"}, "unexpected positional argument"},
	} {
		code, _, errOut := runMemory(t, append(c.args, "--cwd", cwd)...)
		if code != 2 || !strings.Contains(errOut, c.want) {
			t.Errorf("%v: exit %d, stderr %q; want 2 and %q", c.args, code, errOut, c.want)
		}
	}
	if code, _, errOut := runMemory(t, "--memory-list", "--cwd", filepath.Join(cwd, "missing")); code != 2 || !strings.Contains(errOut, "not a directory") {
		t.Errorf("missing cwd: exit %d, %q", code, errOut)
	}
}

// TestMemoryCommandDispatch checks the flags are routed before any run setup:
// with no credentials, a memory command combined with -t exits 2 and a plain
// one exits 0.
func TestMemoryCommandDispatch(t *testing.T) {
	cwd, _ := memoryEnv(t)
	if code, _, _ := runWith(t, "--memory-list", "-t", "do something", "--cwd", cwd); code != 2 {
		t.Errorf("--memory-list -t: exit %d, want 2", code)
	}
	if code, _, _ := runWith(t, "--memory-approve", "all", "--cwd", cwd); code != 0 {
		t.Errorf("--memory-approve all: exit %d, want 0", code)
	}
}
