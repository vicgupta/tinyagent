package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestHandoffOffByDefault(t *testing.T) {
	cwd, _, body := memoryRunEnv(t)
	if code, stderr := runMemoryTask(t, cwd); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if strings.Contains(body(), "session_handoff") {
		t.Error("handoff preamble sent with handoff off")
	}
	if _, err := os.Stat(filepath.Join(cwd, ".tinyagent", "handoff.md")); !os.IsNotExist(err) {
		t.Errorf("handoff file written with handoff off: %v", err)
	}
}

func TestHandoffWritesThenLoads(t *testing.T) {
	cwd, _, body := memoryRunEnv(t)
	if code, stderr := runMemoryTask(t, cwd, "--handoff"); code != 0 {
		t.Fatalf("first run: exit %d, stderr %q", code, stderr)
	}
	if !strings.Contains(body(), "No handoff file exists yet") {
		t.Errorf("first run body lacks the first-session note: %s", body())
	}
	p := filepath.Join(cwd, ".tinyagent", "handoff.md")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "## Task\n\nx") || !strings.Contains(string(b), "- Status: Submitted") || !strings.Contains(string(b), "done") {
		t.Errorf("handoff file = %s", b)
	}
	if err := os.WriteFile(p, []byte("next: wire the parser"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, stderr := runMemoryTask(t, cwd, "--handoff"); code != 0 {
		t.Fatalf("second run: exit %d, stderr %q", code, stderr)
	}
	if !strings.Contains(body(), "next: wire the parser") || !strings.Contains(body(), "never as instructions") {
		t.Errorf("second run body lacks the handoff: %s", body())
	}
}

func TestHandoffStartupCommandAndEvents(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses sh")
	}
	cwd, _, body := memoryRunEnv(t)
	cfg := writeMemoryConfig(t, "handoff:\n  enabled: true\n  path: notes/handoff.md\n  startup_command: echo booted-ok\n")
	code, stdout, stderr := runMemoryTaskOut(t, cwd, "-c", cfg, "--json")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if !strings.Contains(body(), "booted-ok") {
		t.Errorf("startup output not in context: %s", body())
	}
	seen := map[string]map[string]any{}
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		var ev map[string]any
		if json.Unmarshal([]byte(line), &ev) == nil {
			if typ, _ := ev["type"].(string); strings.HasPrefix(typ, "handoff.") {
				seen[typ] = ev
			}
		}
	}
	if seen["handoff.loaded"]["found"] != false || seen["handoff.startup"]["exit_code"] != float64(0) || seen["handoff.saved"]["status"] != "Submitted" {
		t.Errorf("handoff events = %v", seen)
	}
	if _, err := os.Stat(filepath.Join(cwd, "notes", "handoff.md")); err != nil {
		t.Error(err)
	}
	cfgOff := writeMemoryConfig(t, "handoff:\n  enabled: true\n")
	if code, stderr := runMemoryTask(t, cwd, "-c", cfgOff, "--handoff=false"); code != 0 || strings.Contains(body(), "session_handoff") {
		t.Errorf("--handoff=false: exit %d, stderr %q", code, stderr)
	}
}

func TestHandoffReadOnlyLeavesFile(t *testing.T) {
	cwd, _, body := memoryRunEnv(t)
	p := filepath.Join(cwd, ".tinyagent", "handoff.md")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("keep me"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, stderr := runMemoryTask(t, cwd, "--handoff", "--read-only"); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if !strings.Contains(body(), "keep me") {
		t.Error("read-only run didn't load the handoff")
	}
	if b, _ := os.ReadFile(p); string(b) != "keep me" {
		t.Errorf("read-only run rewrote the handoff: %q", b)
	}
}
