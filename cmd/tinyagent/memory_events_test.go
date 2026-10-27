package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/vicgupta/tinyagent/internal/memory"
)

func TestMemoryEventsInJSON(t *testing.T) {
	cwd, store := memoryEnv(t)
	at := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	seedMemory(t, store, testEntry("act1", memory.StatusActive, "secret-free note", at))
	rememberServer(t, "integration tests need TINY_FAKE=1")
	cfg := writeMemoryConfig(t, "memory:\n  enabled: true\n  approval: auto\n")

	code, stdout, stderr := runMemoryTaskOut(t, cwd, "-c", cfg, "--json")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	var types []string
	var loaded, saved map[string]any
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		var ev map[string]any
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		typ, _ := ev["type"].(string)
		types = append(types, typ)
		switch typ {
		case "memory.loaded":
			loaded = ev
		case "memory.saved":
			saved = ev
		}
	}
	if loaded == nil || saved == nil {
		t.Fatalf("events = %v, want memory.loaded and memory.saved", types)
	}
	if loaded["path"] != store || loaded["loaded"] != float64(1) || loaded["omitted"] != float64(0) {
		t.Errorf("memory.loaded = %v", loaded)
	}
	if ids, _ := json.Marshal(loaded["ids"]); string(ids) != `["act1"]` {
		t.Errorf("memory.loaded ids = %s", ids)
	}
	if saved["op"] != "add" || saved["status"] != memory.StatusActive || saved["untrusted_context"] != false || saved["id"] == "" {
		t.Errorf("memory.saved = %v", saved)
	}
	if strings.Contains(stdout, "secret-free note") {
		t.Error("memory.loaded leaked entry text")
	}
	idx := func(s string) int {
		for i, v := range types {
			if v == s {
				return i
			}
		}
		return -1
	}
	if !(idx("run.started") < idx("memory.loaded") && idx("tool.call") < idx("memory.saved") && idx("memory.saved") < idx("tool.result")) {
		t.Errorf("event order = %v", types)
	}
}

func TestMemoryEventsAbsentWhenOff(t *testing.T) {
	cwd, _, _ := memoryRunEnv(t)
	code, stdout, stderr := runMemoryTaskOut(t, cwd, "--json")
	if code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if strings.Contains(stdout, `"memory.`) {
		t.Errorf("memory events with memory off: %s", stdout)
	}
}
