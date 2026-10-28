package agent

import (
	"context"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vicgupta/tinyagent/internal/model"
)

// sandbox_network commands are confined from the first taint, but
// background processes started before it keep their direct network, so
// lockdown stops them (issue #171).
func TestLockdownDirectUntilTaintStopsProcesses(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs sh")
	}
	m := &scripted{replies: []model.Message{
		assistantCalls(call("1", "start_process", `{"command":"sleep 30"}`)),
		assistantCalls(call("2", "docs__search", `{"q":"x"}`)),
		assistantCalls(call("3", "bash", `{"command":"echo ran"}`)),
		submit,
	}}
	a := newAgent(t, m)
	t.Cleanup(func() { a.Tools.Close() })
	var mcpCalls atomic.Int32
	withFakeMCP(a, &mcpCalls)
	a.Lockdown = newLockdown(t)
	a.Lockdown.ConfineCommands(false, func([]string) {})
	a.Lockdown.SetCommandsDirectUntilTaint()
	var ev eventLog
	a.OnEvent = ev.on

	tr := a.Run(context.Background(), "task")
	if tr.ExitStatus != StatusSubmitted {
		t.Fatalf("status %s", tr.ExitStatus)
	}
	res := toolResults(tr.Messages)
	if len(res) != 3 {
		t.Fatalf("results = %q", res)
	}
	if len(ev.of("lockdown.processes_stopped")) != 1 {
		t.Errorf("processes_stopped events = %v (tainting result %q)", ev.of("lockdown.processes_stopped"), res[1])
	}
	for _, p := range a.Tools.Processes() {
		if p.Running {
			t.Error("pre-taint background process still running")
		}
	}
	if strings.HasPrefix(res[2], "error: lockdown") {
		t.Errorf("confined bash refused after taint: %q", res[2])
	}
}
