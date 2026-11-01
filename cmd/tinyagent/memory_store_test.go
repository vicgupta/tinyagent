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
	user := filepath.Join(cwd, "settings.toml") // TINYAGENT_MEMORY_CONFIG must be inside --cwd (#336)
	if err := os.WriteFile(user, []byte("backend = \"sqlite\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TINYAGENT_MEMORY_CONFIG", user)
	code, out, errOut := runMemory(t, "--memory-path", "--cwd", cwd)
	if want := filepath.Join(root, ".tinyagent", "memory", "memory.sqlite"); code != 0 || strings.TrimSpace(out) != want {
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

// TestMemoryNeverLeavesCwd (issue #335): data in every old location
// outside --cwd (XDG_STATE_HOME, XDG_CONFIG_HOME, XDG_CACHE_HOME, HOME and
// the user config dir) is never read, imported or hinted at; the store is
// ./.tinyagent/memory and nothing is written elsewhere.
func TestMemoryNeverLeavesCwd(t *testing.T) {
	cwd, store, body := memoryRunEnv(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	root, _ := canonicalRoot(cwd)
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	key := memory.ProjectKey(root) + ".json"
	var old []string
	for _, d := range []string{
		filepath.Join(home, "state", "tinyagent", "memory"),
		filepath.Join(home, ".local", "state", "tinyagent", "memory"),
		filepath.Join(home, "config", "tinyagent", "memory"),
		filepath.Join(home, "Library", "Application Support", "tinyagent", "memory"),
		filepath.Join(home, "cache", "tinyagent", "memory"),
	} {
		p := filepath.Join(d, key)
		seedMemory(t, p, testEntry("maaaa", memory.StatusActive, "old note", at))
		old = append(old, p)
	}
	if ucd, err := os.UserConfigDir(); err == nil && strings.HasPrefix(ucd, home) {
		p := filepath.Join(ucd, "tinyagent", "memory", key)
		seedMemory(t, p, testEntry("maaaa", memory.StatusActive, "old note", at))
		old = append(old, p)
	}
	snap := func() map[string]string {
		m := map[string]string{}
		filepath.Walk(home, func(p string, fi os.FileInfo, err error) error {
			if err == nil {
				m[p] = fi.ModTime().String()
				if !fi.IsDir() {
					b, _ := os.ReadFile(p)
					m[p] += string(b)
				}
			}
			return nil
		})
		return m
	}
	before := snap()
	if want := filepath.Join(root, ".tinyagent", "memory"); store != want {
		t.Fatalf("store %s, want %s", store, want)
	}
	code, out, errOut := runMemory(t, "--memory-list", "--cwd", cwd)
	if code != 0 || strings.Contains(out, "old note") || errOut != "" {
		t.Fatalf("old data used: exit %d, %q, %q", code, out, errOut)
	}
	if code, stderr := runMemoryTask(t, cwd, "--memory"); code != 0 || strings.Contains(stderr, "old note") || strings.Contains(stderr, "legacy") || strings.Contains(body(), "old note") {
		t.Fatalf("run: exit %d, %q", code, stderr)
	}
	if code, out, _ := runMemory(t, "--memory-path", "--cwd", cwd); code != 0 || strings.TrimSpace(out) != store {
		t.Fatalf("--memory-path %q", out)
	}
	after := snap()
	if len(after) != len(before) {
		t.Fatalf("files outside cwd changed: %d before, %d after", len(before), len(after))
	}
	for p, v := range before {
		if after[p] != v {
			t.Errorf("%s changed", p)
		}
	}
	if len(old) == 0 {
		t.Fatal("no old locations seeded")
	}
}

// TestOldCwdMemoryHint: a store at the previous default (./memory) is not
// read; one hint line names the new location.
func TestOldCwdMemoryHint(t *testing.T) {
	cwd, store := memoryEnv(t)
	root, _ := canonicalRoot(cwd)
	seedMemory(t, filepath.Join(root, "memory"), testEntry("maaaa", memory.StatusActive, "old note", time.Now()))
	code, out, errOut := runMemory(t, "--memory-list", "--cwd", cwd)
	if code != 0 || strings.Contains(out, "old note") {
		t.Fatalf("exit %d, %q", code, out)
	}
	if strings.Count(errOut, "\n") != 1 || !strings.Contains(errOut, "hint: project memory now lives in ./.tinyagent/memory") {
		t.Fatalf("want one hint line, got %q", errOut)
	}
	if memory.Exists(store) {
		t.Error("store created by --memory-list")
	}
	seedMemory(t, store, testEntry("mbbbb", memory.StatusActive, "new note", time.Now()))
	if _, _, errOut := runMemory(t, "--memory-list", "--cwd", cwd); errOut != "" {
		t.Errorf("hint after the new store exists: %q", errOut)
	}
}

// TestMemoryPathEscapingCwdRefused: absolute, ".." and symlinked store paths
// are refused with a warning and the run continues without memory.
func TestMemoryPathEscapingCwdRefused(t *testing.T) {
	outside := t.TempDir()
	for name, path := range map[string]string{
		"absolute": filepath.Join(outside, "mem"),
		"dotdot":   "../mem",
		"symlink":  "link/mem",
	} {
		t.Run(name, func(t *testing.T) {
			cwd, _, _ := memoryRunEnv(t)
			if name == "symlink" {
				if err := os.Symlink(outside, filepath.Join(cwd, "link")); err != nil {
					t.Skip(err)
				}
			}
			t.Setenv("TINYAGENT_MEMORY_PATH", path)
			code, _, errOut := runMemory(t, "--memory-list", "--cwd", cwd)
			if code == 0 || !strings.Contains(errOut, "must stay inside --cwd") {
				t.Fatalf("exit %d, %q", code, errOut)
			}
			if code, stderr := runMemoryTask(t, cwd, "--memory"); code != 0 || !strings.Contains(stderr, "must stay inside --cwd") {
				t.Fatalf("run: exit %d, %q", code, stderr)
			}
			if ents, _ := os.ReadDir(outside); len(ents) != 0 && name != "symlink" {
				t.Errorf("wrote outside cwd: %v", ents)
			}
			if _, err := os.Stat(filepath.Join(outside, "mem")); err == nil {
				t.Error("store created outside cwd")
			}
		})
	}
}
