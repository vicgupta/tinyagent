package agent

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/hooks"
	"github.com/vicgupta/tinyagent/internal/hooks/hookstest"
	"github.com/vicgupta/tinyagent/internal/lsp/lsptest"
	"github.com/vicgupta/tinyagent/internal/model"
)

func TestMain(m *testing.M) {
	hookstest.Run()
	lsptest.Run()
	os.Exit(m.Run())
}

func withHooks(t *testing.T, a *Agent, cfg config.Hooks) {
	t.Helper()
	r, err := hooks.New(cfg, "test.yaml")
	if err != nil {
		t.Fatal(err)
	}
	a.Hooks = r
}

// toolResults returns the content of every tool message in the trajectory.
func toolResults(msgs []model.Message) []string {
	var out []string
	for _, m := range msgs {
		if m.Role == "tool" {
			out = append(out, m.Content)
		}
	}
	return out
}

func TestPreToolHookDenies(t *testing.T) {
	m := &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{call("1", "bash", `{"command":"touch ran"}`)}},
		{Role: "assistant", Content: "ok"},
	}}
	a := newAgent(t, m)
	withHooks(t, a, config.Hooks{PreTool: []config.Hook{hookstest.Hook(t, "bash", "exit2", "no network commands")}})
	var kinds []string
	var hookEvent, resultEvent map[string]any
	a.OnEvent = func(kind string, fields map[string]any) {
		kinds = append(kinds, kind)
		switch kind {
		case "hook":
			hookEvent = fields
		case "tool.result":
			resultEvent = fields
		}
	}
	var errBuf bytes.Buffer
	a.Err, a.Verbose = &errBuf, true

	tr := a.Run(context.Background(), "task")
	if tr.ExitStatus != StatusCompleted || tr.Steps != 2 {
		t.Fatalf("trajectory = %+v", tr)
	}
	if _, err := os.Stat(filepath.Join(a.Tools.Root, "ran")); err == nil {
		t.Error("denied bash command ran")
	}
	res := toolResults(tr.Messages)
	if len(res) != 1 || !strings.HasPrefix(res[0], "error: denied by pre_tool hook ") || !strings.HasSuffix(res[0], ": no network commands") {
		t.Errorf("tool results = %q", res)
	}
	if resultEvent["is_error"] != true {
		t.Errorf("tool.result = %v", resultEvent)
	}
	if strings.Join(kinds, ",") != "model.request,tool.call,hook,tool.result,model.request,assistant.message" {
		t.Errorf("events = %v", kinds)
	}
	if hookEvent["event"] != "pre_tool" || hookEvent["tool_call_id"] != "1" || hookEvent["tool_name"] != "bash" ||
		hookEvent["decision"] != "deny" || hookEvent["command"] == "" || hookEvent["duration_ms"] == nil {
		t.Errorf("hook event = %v", hookEvent)
	}
	if !strings.Contains(errBuf.String(), "hook pre_tool ") || !strings.Contains(errBuf.String(), "for bash: deny") {
		t.Errorf("stderr = %q", errBuf.String())
	}
}

func TestPreToolHookAllowsAndPostToolFeedback(t *testing.T) {
	m := &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{
			call("1", "write_file", `{"path":"main.go","content":"package main"}`),
			call("2", "read_file", `{"path":"main.go"}`),
		}},
		{Role: "assistant", Content: "ok"},
	}}
	a := newAgent(t, m)
	withHooks(t, a, config.Hooks{
		PreTool: []config.Hook{hookstest.Hook(t, "", "stdout", "")},
		PostTool: []config.Hook{
			hookstest.Hook(t, "write_file|edit_file", "stdout", `{"feedback":"main.go is not gofmt-formatted"}`),
			hookstest.Hook(t, "write_file", "exit2", "second"),
			hookstest.Hook(t, "write_file", "exit", "3"),
		},
	})
	var errBuf bytes.Buffer
	a.Err = &errBuf

	tr := a.Run(context.Background(), "task")
	res := toolResults(tr.Messages)
	if len(res) != 2 {
		t.Fatalf("tool results = %q", res)
	}
	if !strings.HasPrefix(res[0], "ok\n[post_tool hook ") || !strings.Contains(res[0], "]\nmain.go is not gofmt-formatted\n[post_tool hook ") || !strings.HasSuffix(res[0], "]\nsecond") {
		t.Errorf("write_file result = %q", res[0])
	}
	if res[1] != "package main" {
		t.Errorf("read_file result = %q, want it unchanged", res[1])
	}
	if !strings.Contains(errBuf.String(), "warning: post_tool hook ") || !strings.Contains(errBuf.String(), "exited with status 3; result unchanged") {
		t.Errorf("stderr = %q", errBuf.String())
	}
}

func TestPostToolFeedbackHonorsMaxOutput(t *testing.T) {
	m := &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{call("1", "write_file", `{"path":"f","content":"x"}`)}},
		{Role: "assistant", Content: "ok"},
	}}
	a := newAgent(t, m)
	a.Tools.MaxOutputChars = 200
	withHooks(t, a, config.Hooks{PostTool: []config.Hook{hookstest.Hook(t, "", "exit2", strings.Repeat("y", 1000))}})
	res := toolResults(a.Run(context.Background(), "task").Messages)
	if len(res) != 1 || !strings.HasPrefix(res[0], "ok\n[post_tool hook ") || !strings.Contains(res[0], "characters elided") || len(res[0]) > 300 {
		t.Errorf("result = %q", res)
	}
}

func TestHooksSkipRejectedCalls(t *testing.T) {
	m := &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{
			call("1", "bash", `{"command":"true"}`),
			call("2", "no_such_tool", `{}`),
			call("3", "read_file", `{"path":"f"}`),
		}},
		{Role: "assistant", Content: "ok"},
	}}
	a := newAgent(t, m)
	a.Tools.ReadOnly = true
	os.WriteFile(filepath.Join(a.Tools.Root, "f"), []byte("hi"), 0o644)
	log := filepath.Join(t.TempDir(), "log")
	withHooks(t, a, config.Hooks{
		PreTool:  []config.Hook{hookstest.Hook(t, ".*", "log", log, "pre")},
		PostTool: []config.Hook{hookstest.Hook(t, ".*", "log", log, "post")},
	})
	res := toolResults(a.Run(context.Background(), "task").Messages)
	if len(res) != 3 || !strings.Contains(res[0], "unavailable in read-only mode") || !strings.HasPrefix(res[1], "format error: unknown tool") || res[2] != "hi" {
		t.Errorf("results = %q", res)
	}
	if b, _ := os.ReadFile(log); string(b) != "pre\npost\n" {
		t.Errorf("hook log = %q, want hooks only for read_file", b)
	}
}

func TestPreToolHookInterrupted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("signal 0 process probe is Unix-only")
	}
	m := &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{call("1", "bash", `{"command":"touch ran"}`)}},
		{Role: "assistant", Content: "ok"},
	}}
	a := newAgent(t, m)
	pidFile := filepath.Join(t.TempDir(), "pid")
	withHooks(t, a, config.Hooks{PreTool: []config.Hook{hookstest.Hook(t, "", "sleep", pidFile)}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pid := make(chan int, 1)
	go func() {
		for range 500 {
			if b, err := os.ReadFile(pidFile); err == nil && len(b) > 0 {
				n, _ := strconv.Atoi(string(b))
				pid <- n
				cancel()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		pid <- 0
		cancel()
	}()
	start := time.Now()
	tr := a.Run(ctx, "task")
	if tr.ExitStatus != StatusInterrupted || time.Since(start) > 20*time.Second {
		t.Errorf("status = %s after %s", tr.ExitStatus, time.Since(start))
	}
	n := <-pid
	if n == 0 {
		t.Fatal("hook never started")
	}
	if p, _ := os.FindProcess(n); p.Signal(syscall.Signal(0)) == nil {
		t.Errorf("hook process %d still running", n)
	}
	if _, err := os.Stat(filepath.Join(a.Tools.Root, "ran")); err == nil {
		t.Error("tool ran after interrupted pre_tool hook")
	}
}

// TestPreToolHookDeniesMCPResourceRead: hooks match the resource tool names
// like any other tool, and a denied read never reaches the MCP registry.
func TestPreToolHookDeniesMCPResourceRead(t *testing.T) {
	m := &scripted{replies: []model.Message{
		{Role: "assistant", ToolCalls: []model.ToolCall{call("1", "read_mcp_resource", `{"server":"docs","uri":"docs://a"}`)}},
		{Role: "assistant", ToolCalls: []model.ToolCall{call("2", "list_mcp_resources", `{}`)}},
		{Role: "assistant", Content: "ok"},
	}}
	a := newAgent(t, m)
	a.Tools.ExternalSpecs = []model.ToolSpec{{Type: "function", Function: model.FunctionSpec{Name: "list_mcp_resources"}}, {Type: "function", Function: model.FunctionSpec{Name: "read_mcp_resource"}}}
	var called []string
	a.Tools.ExternalCall = func(_ context.Context, name, _ string) (string, bool, bool) {
		called = append(called, name)
		return "server=docs uri=docs://a", false, name == "list_mcp_resources" || name == "read_mcp_resource"
	}
	withHooks(t, a, config.Hooks{PreTool: []config.Hook{hookstest.Hook(t, "read_mcp_resource", "exit2", "no resource reads")}})
	var kinds []string
	a.OnEvent = func(kind string, _ map[string]any) { kinds = append(kinds, kind) }

	tr := a.Run(context.Background(), "task")
	if tr.ExitStatus != StatusCompleted {
		t.Fatalf("trajectory = %+v", tr)
	}
	if strings.Join(called, ",") != "list_mcp_resources" {
		t.Errorf("registry calls = %v, want only the list", called)
	}
	res := toolResults(tr.Messages)
	if len(res) != 2 || !strings.HasSuffix(res[0], ": no resource reads") || res[1] != "server=docs uri=docs://a" {
		t.Errorf("tool results = %q", res)
	}
	if n := strings.Count(strings.Join(kinds, ","), "tool.result"); n != 2 {
		t.Errorf("events = %v, want 2 tool.result", kinds)
	}
}
