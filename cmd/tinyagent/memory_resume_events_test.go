package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vicgupta/tinyagent/internal/memory"
)

// resumeMemoryJSON follows up checkpoint src with --json and returns the
// emitted event types and the memory.loaded event, if any.
func resumeMemoryJSON(t *testing.T, cwd, src string) (types []string, loaded map[string]any, stdout string) {
	t.Helper()
	next := filepath.Join(t.TempDir(), "next.json")
	var code int
	var stderr string
	stdout = captureStdout(t, func() {
		stderr = captureStderr(t, func() {
			var flagErr string
			code, _, flagErr = runWith(t, "--resume", src, "--cwd", cwd, "-o", next, "-t", "more", "--no-stream", "--json")
			os.Stderr.WriteString(flagErr)
		})
	})
	if code != 0 {
		t.Fatalf("resume exit %d, stderr %q", code, stderr)
	}
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		var ev map[string]any
		if json.Unmarshal([]byte(line), &ev) != nil {
			continue
		}
		typ, _ := ev["type"].(string)
		types = append(types, typ)
		if typ == "memory.loaded" {
			loaded = ev
		}
	}
	return types, loaded, stdout
}

func TestMemoryLoadedOnFollowUp(t *testing.T) {
	cwd, store, _ := memoryRunEnv(t)
	at := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	seedMemory(t, store, testEntry("act1", memory.StatusActive, "replayed-note-text", at))
	src := filepath.Join(t.TempDir(), "run.json")
	if code, stderr := runMemoryTask(t, cwd, "--memory", "-o", src); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	// The follow-up replays the checkpoint snapshot, not the store.
	seedMemory(t, store, testEntry("act2", memory.StatusActive, "later note", at.Add(time.Hour)))

	types, loaded, stdout := resumeMemoryJSON(t, cwd, src)
	if loaded == nil {
		t.Fatalf("events = %v, want memory.loaded", types)
	}
	if loaded["path"] != store || loaded["loaded"] != float64(1) || loaded["omitted"] != float64(0) || loaded["from_checkpoint"] != true {
		t.Errorf("memory.loaded = %v", loaded)
	}
	if ids, _ := json.Marshal(loaded["ids"]); string(ids) != `["act1"]` {
		t.Errorf("memory.loaded ids = %s, want the checkpoint snapshot", ids)
	}
	if strings.Contains(stdout, "replayed-note-text") {
		t.Error("memory.loaded leaked entry text")
	}
	n, started, at1 := 0, -1, -1
	for i, typ := range types {
		switch typ {
		case "memory.loaded":
			n++
			at1 = i
		case "run.started":
			if started < 0 {
				started = i
			}
		}
	}
	if n != 1 || started < 0 || started > at1 {
		t.Errorf("event order = %v, want one memory.loaded after run.started", types)
	}
}

func TestMemoryLoadedAbsentOnFollowUpWithMemoryOff(t *testing.T) {
	cwd, store, _ := memoryRunEnv(t)
	seedMemory(t, store, testEntry("act1", memory.StatusActive, "note", time.Now()))
	src := filepath.Join(t.TempDir(), "off.json")
	if code, stderr := runMemoryTask(t, cwd, "-o", src); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if types, loaded, _ := resumeMemoryJSON(t, cwd, src); loaded != nil {
		t.Errorf("memory.loaded on a memory-off follow-up: %v", types)
	}
}
