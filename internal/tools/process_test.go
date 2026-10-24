//go:build !windows

package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/vicgupta/tinyagent/internal/model"
)

func runTool(t *testing.T, e *Executor, name string, args map[string]any) string {
	t.Helper()
	b, _ := json.Marshal(args)
	return e.Run(context.Background(), model.ToolCall{ID: "1", Function: model.FunctionCall{Name: name, Arguments: string(b)}})
}

var procIDRe = regexp.MustCompile(`<process_id>(p\d+)</process_id>`)

func startID(t *testing.T, e *Executor, command string) (string, string) {
	t.Helper()
	out := runTool(t, e, "start_process", map[string]any{"command": command})
	m := procIDRe.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("start_process: no id in %q", out)
	}
	return m[1], out
}

// alive reports whether pid still exists (and isn't a zombie we can't see).
func alive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within 5s")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestProcessLifecycle(t *testing.T) {
	root := t.TempDir()
	e := &Executor{Root: root}
	defer e.Close()
	id, out := startID(t, e, `echo ready; while [ ! -f go ]; do sleep 0.05; done; echo second; sleep 300`)
	if !strings.Contains(out, "ready") || !strings.Contains(out, "<status>running</status>") {
		t.Fatalf("start output = %q", out)
	}

	// Nothing new yet: no duplicated "ready".
	out = runTool(t, e, "read_process", map[string]any{"id": id})
	if strings.Contains(out, "ready") {
		t.Fatalf("read repeated old output: %q", out)
	}

	if err := os.WriteFile(filepath.Join(root, "go"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	out = runTool(t, e, "read_process", map[string]any{"id": id, "timeout": 5000})
	if !strings.Contains(out, "second") || strings.Contains(out, "ready") {
		t.Fatalf("read after go = %q", out)
	}

	out = runTool(t, e, "stop_process", map[string]any{"id": id})
	if !strings.Contains(out, "exited") {
		t.Fatalf("stop output = %q", out)
	}
	out = runTool(t, e, "read_process", map[string]any{"id": id})
	if !strings.Contains(out, "no background process") {
		t.Fatalf("read after stop = %q", out)
	}
}

func TestProcessExitStatus(t *testing.T) {
	e := &Executor{Root: t.TempDir()}
	defer e.Close()
	id, _ := startID(t, e, "echo bye; exit 3")
	out := runTool(t, e, "read_process", map[string]any{"id": id, "timeout": 2000})
	waitFor(t, func() bool {
		out = runTool(t, e, "read_process", map[string]any{"id": id})
		return strings.Contains(out, "exited")
	})
	if !strings.Contains(out, "exited with code 3") {
		t.Fatalf("read = %q", out)
	}
	out = runTool(t, e, "stop_process", map[string]any{"id": id})
	if !strings.Contains(out, "exited with code 3") {
		t.Fatalf("stop = %q", out)
	}
}

func TestStopProcessKillsChildren(t *testing.T) {
	root := t.TempDir()
	e := &Executor{Root: root}
	defer e.Close()
	id, _ := startID(t, e, `sleep 300 & echo $! > child.pid; wait`)
	var pid int
	waitFor(t, func() bool {
		b, err := os.ReadFile(filepath.Join(root, "child.pid"))
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(b)))
		return err == nil
	})
	if !alive(pid) {
		t.Fatal("child not running before stop")
	}
	runTool(t, e, "stop_process", map[string]any{"id": id})
	waitFor(t, func() bool { return !alive(pid) })
}

func TestCloseKillsSurvivors(t *testing.T) {
	root := t.TempDir()
	e := &Executor{Root: root}
	startID(t, e, `echo one; sleep 300 & echo $! > a.pid; wait`)
	startID(t, e, `echo two; exit 0`)
	var pid int
	waitFor(t, func() bool {
		b, err := os.ReadFile(filepath.Join(root, "a.pid"))
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(b)))
		return err == nil
	})
	summary := e.Close()
	if !strings.Contains(summary, "p1") || !strings.Contains(summary, "killed at run end") || !strings.Contains(summary, "p2") {
		t.Fatalf("summary = %q", summary)
	}
	waitFor(t, func() bool { return !alive(pid) })
	if s := e.Close(); s != "" {
		t.Fatalf("second Close = %q", s)
	}
}

func TestProcessLimit(t *testing.T) {
	e := &Executor{Root: t.TempDir(), MaxProcesses: 2}
	defer e.Close()
	a, _ := startID(t, e, "sleep 300")
	startID(t, e, "sleep 300")
	out := runTool(t, e, "start_process", map[string]any{"command": "sleep 300"})
	if !strings.Contains(out, "too many background processes") {
		t.Fatalf("third start = %q", out)
	}
	runTool(t, e, "stop_process", map[string]any{"id": a})
	startID(t, e, "sleep 300")
}

func TestProcessBufferCap(t *testing.T) {
	e := &Executor{Root: t.TempDir(), ProcessBufferBytes: 100}
	defer e.Close()
	id, _ := startID(t, e, `sleep 0.6; for i in $(seq 1 200); do echo line$i; done; sleep 300`)
	var out string
	waitFor(t, func() bool {
		out += runTool(t, e, "read_process", map[string]any{"id": id, "timeout": 1000})
		return strings.Contains(out, "line200")
	})
	if !strings.Contains(out, "dropped: buffer cap") {
		t.Fatalf("no drop note in %q", out)
	}
	if strings.Contains(out, "line1\n") {
		t.Fatalf("old output kept past cap: %q", out)
	}
}

func TestReadProcessTail(t *testing.T) {
	e := &Executor{Root: t.TempDir()}
	defer e.Close()
	id, _ := startID(t, e, `sleep 0.6; printf 'a\nb\nc\nd\n'; sleep 300`)
	var out string
	waitFor(t, func() bool {
		out = runTool(t, e, "read_process", map[string]any{"id": id, "timeout": 2000, "tail": 2})
		return strings.Contains(out, "d")
	})
	if !strings.Contains(out, "c\nd\n") || strings.Contains(out, "a\n") || !strings.Contains(out, "2 earlier lines omitted") {
		t.Fatalf("tail read = %q", out)
	}
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestBashStream(t *testing.T) {
	root := t.TempDir()
	live := &syncBuf{}
	e := &Executor{Root: root, Stream: live}

	// Default: nothing streamed, result unchanged.
	out := runTool(t, e, "bash", map[string]any{"command": "echo quiet"})
	if !strings.Contains(out, "quiet") || live.String() != "" {
		t.Fatalf("unstreamed: out=%q live=%q", out, live.String())
	}

	// Streaming: the first line shows up while the command is still running.
	done := make(chan string)
	go func() {
		done <- runTool(t, e, "bash", map[string]any{"command": "echo early; while [ ! -f go ]; do sleep 0.05; done; echo late", "stream": true})
	}()
	waitFor(t, func() bool { return strings.Contains(live.String(), "early") })
	if strings.Contains(live.String(), "late") {
		t.Fatal("late output streamed before it was produced")
	}
	if err := os.WriteFile(filepath.Join(root, "go"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	out = <-done
	if !strings.Contains(out, "<returncode>0</returncode>") || !strings.Contains(out, "early\nlate") {
		t.Fatalf("streamed result = %q", out)
	}
	if !strings.Contains(live.String(), "late") {
		t.Fatalf("live = %q", live.String())
	}
}
