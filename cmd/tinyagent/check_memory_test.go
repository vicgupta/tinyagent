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

func TestCheckMemoryStatus(t *testing.T) {
	hits := checkEnv(t)
	cwd, store := memoryEnv(t)
	t.Setenv("ANTHROPIC_API_KEY", "k")
	base := []string{"--check", "-m", "anthropic/claude-sonnet-4-5", "--cwd", cwd}

	code, stdout, stderr := runCLI(t, base...)
	if code != 0 {
		t.Fatalf("exit %d; stderr:\n%s", code, stderr)
	}
	if strings.Contains(stdout, "memory:") {
		t.Errorf("memory line with memory off:\n%s", stdout)
	}
	if _, err := os.Stat(store); !os.IsNotExist(err) {
		t.Errorf("--check created the store: %v", err)
	}

	code, stdout, _ = runCLI(t, append(base, "--memory")...)
	if code != 0 {
		t.Fatalf("--memory: exit %d", code)
	}
	if got, want := reportLine(t, stdout, "memory"), "on, markdown backend, approval pending (from --memory), 0 active, 0 pending: "+store; got != want {
		t.Errorf("empty store line %q, want %q", got, want)
	}
	if _, err := os.Stat(store); !os.IsNotExist(err) {
		t.Errorf("--check --memory created the store: %v", err)
	}

	at := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	seedMemory(t, store,
		testEntry("a1", memory.StatusActive, "one", at),
		testEntry("a2", memory.StatusActive, "two", at),
		testEntry("p1", memory.StatusPending, "three", at))
	cfg := writeMemoryConfig(t, "memory:\n  enabled: true\n  approval: auto\n")
	code, stdout, _ = runCLI(t, append(base, "-c", cfg)...)
	if code != 0 {
		t.Fatalf("config: exit %d", code)
	}
	if got, want := reportLine(t, stdout, "memory"), "on, markdown backend, approval auto (from config), 2 active, 1 pending: "+store; got != want {
		t.Errorf("config line %q, want %q", got, want)
	}
	if strings.Contains(stdout, "three") {
		t.Error("--check printed entry text")
	}

	code, stdout, _ = runCLI(t, append(base, "-c", cfg, "--memory=false")...)
	if code != 0 || reportLine(t, stdout, "memory") != "off (from --memory)" {
		t.Errorf("--memory=false: exit %d\n%s", code, stdout)
	}

	if err := os.WriteFile(filepath.Join(store, "a1.md"), []byte("no front matter"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, _ = runCLI(t, append(base, "--memory")...)
	if code != 0 || !strings.Contains(reportLine(t, stdout, "memory"), "store corrupt (runs continue without memory)") {
		t.Errorf("corrupt store: exit %d\n%s", code, stdout)
	}

	bad := writeMemoryConfig(t, "memory:\n  enabled: true\n  approval: sometimes\n")
	if code, _, stderr := runCLI(t, append(base, "-c", bad)...); code != 2 || !strings.Contains(stderr, "memory.approval") {
		t.Errorf("invalid memory config: exit %d, stderr %q", code, stderr)
	}
	if hits.Load() != 0 {
		t.Errorf("--check made %d requests", hits.Load())
	}
}

func TestStarterConfigMemoryBlockParses(t *testing.T) {
	var out []string
	in := false
	for _, l := range strings.Split(starterConfig, "\n") {
		if strings.HasPrefix(l, "# memory:") {
			in = true
		} else if in && !strings.HasPrefix(l, "#   ") {
			in = false
		}
		if in {
			out = append(out, strings.TrimPrefix(l, "# "))
		}
	}
	if len(out) == 0 {
		t.Fatal("starter config has no memory block")
	}
	p := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(p, []byte(strings.Join(out, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadProfile(p, "")
	if err != nil {
		t.Fatalf("uncommented memory block: %v\n%s", err, strings.Join(out, "\n"))
	}
	if cfg.Memory.Enabled || cfg.Memory.Approval != "pending" || cfg.Memory.MaxWritesPerRun != 5 || cfg.Memory.MaxEntries != 50 || cfg.Memory.MaxBytes != 8192 {
		t.Errorf("memory = %+v", cfg.Memory)
	}
}
