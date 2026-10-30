package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/memory"
)

func TestMemoryHelpFlag(t *testing.T) {
	memoryEnv(t)
	code, out, errOut := runMemory(t, "--memory-help")
	if code != 0 || errOut != "" || out != config.MemoryHelp() {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	for _, k := range config.MemoryKeys {
		if !strings.Contains(out, "# "+k.Name+" = ") || !strings.Contains(out, k.EnvVar()) {
			t.Errorf("--memory-help lacks %s", k.Name)
		}
	}
	if code, _, _ := runMemory(t, "--memory-help", "--memory-list"); code != 2 {
		t.Errorf("--memory-help with --memory-list: exit %d", code)
	}
}

func TestMemoryTOMLSelectsBackend(t *testing.T) {
	cwd, _ := memoryEnv(t)
	root, _ := canonicalRoot(cwd)
	user := filepath.Join(t.TempDir(), "memory.toml")
	if err := os.WriteFile(user, []byte("backend = \"sqlite\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TINYAGENT_MEMORY_CONFIG", user)
	code, out, errOut := runMemory(t, "--memory-path", "--cwd", cwd)
	if want := filepath.Join(root, "memory", "memory.sqlite"); code != 0 || strings.TrimSpace(out) != want {
		t.Fatalf("exit %d, %q (stderr %q), want %s", code, out, errOut, want)
	}
	t.Setenv("TINYAGENT_MEMORY_BACKEND", "markdown")
	t.Setenv("TINYAGENT_MEMORY_PATH", "notes")
	if code, out, _ := runMemory(t, "--memory-path", "--cwd", cwd); code != 0 || strings.TrimSpace(out) != filepath.Join(root, "notes") {
		t.Fatalf("env override: exit %d, %q", code, out)
	}
	t.Setenv("TINYAGENT_MEMORY_BACKEND", "redis")
	if code, _, errOut := runMemory(t, "--memory-path", "--cwd", cwd); code != 2 || !strings.Contains(errOut, "backend") {
		t.Fatalf("bad backend: exit %d, %q", code, errOut)
	}
}

func TestRepoMemoryTOMLCannotEnableMemory(t *testing.T) {
	cwd, store, body := memoryRunEnv(t)
	seedMemory(t, store, testEntry("act1", memory.StatusActive, "planted-note", time.Now()))
	if err := os.WriteFile(filepath.Join(cwd, "memory.toml"), []byte("auto_write = true\nrequire_approval = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stderr := runMemoryTask(t, cwd)
	if code != 0 || strings.Contains(body(), "planted-note") || strings.Contains(body(), `"name":"remember"`) {
		t.Fatalf("repo memory.toml turned memory on: exit %d", code)
	}
	if !strings.Contains(stderr, "auto_write = true ignored") || !strings.Contains(stderr, "require_approval = false ignored") {
		t.Errorf("no warnings: %q", stderr)
	}
}

func TestMemoryRunOffersSearchTools(t *testing.T) {
	cwd, store, body := memoryRunEnv(t)
	seedMemory(t, store, testEntry("act1", memory.StatusActive, "note", time.Now()))
	if code, stderr := runMemoryTask(t, cwd, "--memory"); code != 0 {
		t.Fatalf("exit %d, %q", code, stderr)
	}
	for _, name := range []string{"remember", "memory_search", "memory_list"} {
		if !strings.Contains(body(), `"name":"`+name+`"`) {
			t.Errorf("%s not offered", name)
		}
	}
	if code, stderr := runMemoryTask(t, cwd, "--memory", "--read-only"); code != 0 || strings.Contains(body(), `"name":"memory_search"`) {
		t.Errorf("read-only: exit %d, %q", code, stderr)
	}
}

func TestLegacyJSONImportedOnce(t *testing.T) {
	cwd, store := memoryEnv(t)
	root, _ := canonicalRoot(cwd)
	legacy := legacyMemoryPath(root)
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	seedMemory(t, legacy, testEntry("maaaa", memory.StatusActive, "old note", at), testEntry("mbbbb", memory.StatusPending, "old pending", at))
	before, _ := os.ReadFile(legacy)
	if code, _, _ := runMemory(t, "--memory-path", "--cwd", cwd); code != 0 || memory.Exists(store) {
		t.Fatal("--memory-path imported")
	}
	code, out, errOut := runMemory(t, "--memory-list", "--cwd", cwd)
	if code != 0 || !strings.Contains(out, "old note") || !strings.Contains(errOut, "imported 2 entries from legacy store") {
		t.Fatalf("exit %d, %q, %q", code, out, errOut)
	}
	if after, _ := os.ReadFile(legacy); string(after) != string(before) {
		t.Error("legacy store changed")
	}
	if code, _, _ := runMemory(t, "--memory-forget", "all", "--memory-yes", "--cwd", cwd); code != 0 {
		t.Fatal("forget all failed")
	}
	if _, out, errOut := runMemory(t, "--memory-list", "--cwd", cwd); strings.Contains(out, "old note") || errOut != "" {
		t.Errorf("re-imported after forget all: %q %q", out, errOut)
	}
	t.Setenv("TINYAGENT_MEMORY_IMPORT_LEGACY", "false")
	cwd2 := t.TempDir()
	root2, _ := canonicalRoot(cwd2)
	seedMemory(t, legacyMemoryPath(root2), testEntry("maaaa", memory.StatusActive, "old note", at))
	if _, out, _ := runMemory(t, "--memory-list", "--cwd", cwd2); strings.Contains(out, "old note") {
		t.Error("imported with import_legacy = false")
	}
}
