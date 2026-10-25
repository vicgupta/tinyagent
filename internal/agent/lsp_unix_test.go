//go:build !windows

package agent

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/vicgupta/tinyagent/internal/lsp/lsptest"
	"github.com/vicgupta/tinyagent/internal/model"
)

// TestLSPNoServerLeftAfterRun: every terminal status stops the servers
// and their children, including ones that ignore shutdown (AC13).
func TestLSPNoServerLeftAfterRun(t *testing.T) {
	for _, final := range []model.Message{submit, {Role: "assistant", Content: "plain answer"}} {
		m := &scripted{replies: []model.Message{
			assistantCalls(call("1", "write_file", `{"path":"a.go","content":"package a\n"}`)),
			final,
		}}
		a := newAgent(t, m)
		pidfile := filepath.Join(t.TempDir(), "pids")
		withLSP(t, a, lsptest.Server(t, "go", []string{".go"}, "child", "ignore-shutdown", "pidfile="+pidfile))
		tr := a.Run(context.Background(), "task")
		pids := lsptest.ReadPids(t, pidfile, 2)
		lsptest.AssertGone(t, pids)
		if tr.ExitStatus == "" {
			t.Errorf("no exit status")
		}
	}
	// Interrupted before the model answers.
	ctx, cancel := context.WithCancel(context.Background())
	a := newAgent(t, &cancelAfter{cancel: cancel, m: &scripted{replies: []model.Message{
		assistantCalls(call("1", "write_file", `{"path":"a.go","content":"package a\n"}`)),
	}}})
	pidfile := filepath.Join(t.TempDir(), "pids")
	withLSP(t, a, lsptest.Server(t, "go", []string{".go"}, "child", "pidfile="+pidfile))
	tr := a.Run(ctx, "task")
	lsptest.AssertGone(t, lsptest.ReadPids(t, pidfile, 2))
	if tr.ExitStatus != StatusInterrupted {
		t.Errorf("status %s", tr.ExitStatus)
	}
}

// cancelAfter cancels the run once its scripted replies run out.
type cancelAfter struct {
	cancel context.CancelFunc
	m      *scripted
	n      int
}

func (c *cancelAfter) Query(ctx context.Context, msgs []model.Message, specs []model.ToolSpec) (model.Message, model.Cost, error) {
	if c.n >= len(c.m.replies) {
		c.cancel()
		<-ctx.Done()
		return model.Message{}, model.Cost{}, ctx.Err()
	}
	c.n++
	return c.m.replies[c.n-1], model.Cost{}, nil
}
