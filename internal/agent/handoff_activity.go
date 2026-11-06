package agent

import (
	"context"
	"io"
	"sync"

	"github.com/vicgupta/tinyagent/internal/eventlog"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/tools"
)

// queryModel is Model.Query with live handoff activity attached while
// streaming. Each CLI tool or command gets its own preview, so secret
// redaction and the 256 KiB cap apply per activity the way they do for bash.
func (a *Agent) queryModel(ctx context.Context, msgs []model.Message, specs []model.ToolSpec) (model.Message, model.Cost, error) {
	ctx, done := a.trackHandoff(ctx)
	defer done()
	return a.Model.Query(ctx, msgs, specs)
}

// trackHandoff installs the activity callback when this run streams events.
// The closer flushes a trailing partial line (so a secret split from its
// newline is still redacted) after the CLI exits.
func (a *Agent) trackHandoff(ctx context.Context) (context.Context, func()) {
	if !a.Streamed || a.OnEvent == nil {
		return ctx, func() {}
	}
	var red *eventlog.Redactor
	if a.Tools != nil {
		red = a.Tools.Redactor
	}
	var mu sync.Mutex
	previews := map[string]io.WriteCloser{}
	ctx = model.WithActivity(ctx, func(id, name, chunk string) {
		if chunk == "" {
			return
		}
		if id == "" {
			id = name
		}
		if id == "" {
			id = "handoff"
		}
		mu.Lock()
		w := previews[id]
		if w == nil {
			eid, ename := id, name
			w = tools.NewLivePreview(func(s string) {
				if s == "" {
					return
				}
				a.event("tool.output", map[string]any{"id": eid, "name": ename, "output": s})
			}, red)
			previews[id] = w
		}
		mu.Unlock()
		_, _ = w.Write([]byte(chunk))
	})
	return ctx, func() {
		mu.Lock()
		defer mu.Unlock()
		for _, w := range previews {
			_ = w.Close()
		}
	}
}
