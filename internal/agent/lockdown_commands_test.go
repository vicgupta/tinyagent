package agent

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vicgupta/tinyagent/internal/lockdown"
	"github.com/vicgupta/tinyagent/internal/model"
)

// Commands confined by the sandbox (issue #171) keep running after taint;
// the other outbound channels are still refused.
func TestLockdownConfinedCommandsRunAfterTaint(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs sh")
	}
	m := &scripted{replies: []model.Message{
		assistantCalls(call("1", "start_process", `{"command":"sleep 30"}`)),
		assistantCalls(call("2", "docs__search", `{"q":"x"}`)),
		assistantCalls(
			call("3", "bash", `{"command":"touch ran"}`),
			call("4", "docs__search", `{"q":"y"}`),
		),
		submit,
	}}
	a := newAgent(t, m)
	t.Cleanup(func() { a.Tools.Close() })
	var mcpCalls atomic.Int32
	withFakeMCP(a, &mcpCalls)
	a.Lockdown = newLockdown(t)
	var narrowed atomic.Int32
	a.Lockdown.ConfineCommands(true, func([]string) { narrowed.Add(1) })
	var ev eventLog
	a.OnEvent = ev.on
	// The model is asked for its last reply after taint: check then that
	// the background process survived (Run stops everything at the end).
	var runningAfterTaint atomic.Bool
	a.Model = queryHook{m, func() {
		if !a.Lockdown.Tainted() {
			return
		}
		for _, p := range a.Tools.Processes() {
			if p.Running {
				runningAfterTaint.Store(true)
			}
		}
	}}

	tr := a.Run(context.Background(), "task")
	if tr.ExitStatus != StatusSubmitted {
		t.Fatalf("status %s", tr.ExitStatus)
	}
	res := toolResults(tr.Messages)
	if len(res) != 4 {
		t.Fatalf("results = %q", res)
	}
	if strings.Contains(res[1], "stopped background processes") || len(ev.of("lockdown.processes_stopped")) != 0 {
		t.Errorf("confined processes were stopped: %q", res[1])
	}
	if !runningAfterTaint.Load() {
		t.Error("confined background process not running after taint")
	}
	if strings.HasPrefix(res[2], "error: lockdown") {
		t.Errorf("confined bash refused: %q", res[2])
	}
	if _, err := os.Stat(filepath.Join(a.Tools.Root, "ran")); err != nil {
		t.Error("confined bash didn't run after taint")
	}
	if !strings.HasPrefix(res[3], "error: lockdown: docs__search refused") {
		t.Errorf("MCP after taint = %q", res[3])
	}
	if narrowed.Load() != 1 {
		t.Errorf("narrowed %d times", narrowed.Load())
	}
	if d := ev.of("lockdown.denied"); len(d) != 1 || d[0]["channel"] != "mcp" {
		t.Errorf("denied = %v", d)
	}
}

func TestLockdownConfinedCommandsRunOnUntrustedTask(t *testing.T) {
	m := &scripted{replies: []model.Message{
		assistantCalls(call("1", "bash", `{"command":"touch ran"}`)),
		submit,
	}}
	a := newAgent(t, m)
	a.Lockdown = newLockdown(t)
	a.Lockdown.Taint(lockdown.Source{Source: lockdown.SourceUntrustedTask})
	a.Lockdown.ConfineCommands(false, nil)
	a.Run(context.Background(), "untrusted issue body")
	if _, err := os.Stat(filepath.Join(a.Tools.Root, "ran")); err != nil {
		t.Error("confined bash refused on an untrusted task")
	}
}

// queryHook calls before ahead of each query to m.
type queryHook struct {
	m      model.Model
	before func()
}

func (q queryHook) Query(ctx context.Context, msgs []model.Message, specs []model.ToolSpec) (model.Message, model.Cost, error) {
	q.before()
	return q.m.Query(ctx, msgs, specs)
}
