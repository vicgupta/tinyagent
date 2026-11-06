package main

import (
	"io"
	"strings"
	"sync"
	"time"

	"github.com/vicgupta/tinyagent/internal/agent"
)

// streamToolProgress keeps short, implicit tool calls quiet in one-shot human
// mode. Explicit stream:true retains the streamTo/output-schema contract; other
// progress goes to stderr, never into the final answer on stdout.
func streamToolProgress(a *agent.Agent, o options, explicit, progress io.Writer) {
	if o.jsonOutput || o.noStream {
		return
	}
	p := &toolProgress{explicit: explicit, progress: progress, verbose: o.verbose}
	logged := a.OnEvent
	a.OnEvent = func(kind string, fields map[string]any) {
		// Stop the timer before forwarding the result, so no preview can be
		// written after it or race with the next model response.
		if kind == "tool.result" {
			p.finish()
		}
		if logged != nil {
			logged(kind, fields)
		}
		switch kind {
		case "tool.call":
			args, _ := fields["arguments"].(map[string]any)
			requested, _ := args["stream"].(bool)
			p.start(requested)
		case "tool.output":
			chunk, _ := fields["output"].(string)
			p.chunk(chunk)
		}
	}
}

// The timer only reveals the bounded preview already supplied by the executor.
// Each result synchronizes with it before any later terminal output.
type toolProgress struct {
	mu                 sync.Mutex
	explicit, progress io.Writer
	verbose, visible   bool
	to                 io.Writer
	pending            strings.Builder
	timer              *time.Timer
	generation         uint64
}

func (p *toolProgress) start(requested bool) {
	p.finish()
	p.mu.Lock()
	defer p.mu.Unlock()
	p.to = p.progress
	if requested && p.explicit != nil {
		p.to = p.explicit
	}
	p.visible = requested || p.verbose
	if !p.visible {
		generation := p.generation
		p.timer = time.AfterFunc(time.Second, func() {
			p.mu.Lock()
			defer p.mu.Unlock()
			if generation == p.generation {
				p.reveal()
			}
		})
	}
}

func (p *toolProgress) show() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.reveal()
}

// reveal is called with mu held.
func (p *toolProgress) reveal() {
	if p.to == nil {
		return
	}
	p.visible = true
	_, _ = io.WriteString(p.to, p.pending.String())
	p.pending.Reset()
}

func (p *toolProgress) chunk(s string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.to == nil {
		// A handoff CLI reports its own tools without a tinyagent tool.call.
		// Show that preview immediately; bash still waits on tool.call.
		p.to = p.progress
		p.visible = true
	}
	if p.to == nil {
		return
	}
	if p.visible {
		_, _ = io.WriteString(p.to, s)
	} else {
		p.pending.WriteString(s)
	}
}

func (p *toolProgress) finish() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.timer != nil {
		p.timer.Stop()
		p.timer = nil
	}
	p.to = nil
	p.generation++
	p.pending.Reset()
}
