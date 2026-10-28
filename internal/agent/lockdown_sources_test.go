package agent

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/vicgupta/tinyagent/internal/lockdown"
	"github.com/vicgupta/tinyagent/internal/model"
)

// With untrusted_content.sources leaving mcp out, an MCP result doesn't
// taint, so a later command runs; MCP tools are still refused once the run
// is tainted some other way.
func TestLockdownSourcesExcludeMCP(t *testing.T) {
	m := &scripted{replies: []model.Message{
		assistantCalls(call("1", "docs__search", `{}`)),
		assistantCalls(call("2", "bash", `{"command":"true"}`)),
		submit,
	}}
	a := newAgent(t, m)
	var calls atomic.Int32
	withFakeMCP(a, &calls)
	a.Lockdown = newLockdown(t)
	if err := a.Lockdown.SetSources([]string{lockdown.SourceWebFetch}); err != nil {
		t.Fatal(err)
	}
	var events eventLog
	a.OnEvent = events.on
	tr := a.Run(context.Background(), "task")
	res := toolResults(tr.Messages)
	if len(res) != 2 || strings.Contains(res[0], lockdown.Notice) || strings.HasPrefix(res[1], "error: lockdown") {
		t.Errorf("results = %q", res)
	}
	if a.Lockdown.Tainted() || len(events.of("taint")) != 0 || len(events.of("lockdown.denied")) != 0 {
		t.Errorf("tainted=%v events=%v", a.Lockdown.Tainted(), events.kinds)
	}

	m = &scripted{replies: []model.Message{
		assistantCalls(call("1", "docs__search", `{}`)),
		submit,
	}}
	a = newAgent(t, m)
	calls.Store(0)
	withFakeMCP(a, &calls)
	a.Lockdown = newLockdown(t)
	if err := a.Lockdown.SetSources([]string{lockdown.SourceWebFetch}); err != nil {
		t.Fatal(err)
	}
	a.Lockdown.Taint(lockdown.Source{Source: lockdown.SourceUntrustedTask})
	tr = a.Run(context.Background(), "task")
	if res := toolResults(tr.Messages); len(res) != 1 || !strings.HasPrefix(res[0], "error: lockdown: docs__search refused") || calls.Load() != 0 {
		t.Errorf("results = %q, calls %d", res, calls.Load())
	}
}
