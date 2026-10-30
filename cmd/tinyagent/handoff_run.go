package main

import (
	"context"
	"flag"
	"path/filepath"
	"time"

	"github.com/vicgupta/tinyagent/internal/agent"
	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/handoff"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// registerHandoffFlags registers the session handoff flag (issue #251).
func registerHandoffFlags(fs *flag.FlagSet, on *bool) {
	fs.BoolVar(on, "handoff", false, "session handoff: read the handoff file (handoff.path, default .tinyagent/handoff.md) into the first message as data, run handoff.startup_command if set, and rewrite the file with the task, status and final answer when the run ends (overrides handoff.enabled; --handoff=false turns it off); fresh runs only")
}

// handoffRun is the session handoff state of one fresh run.
type handoffRun struct {
	cfg      config.Handoff
	path     string // absolute
	rel      string // as shown to the model
	readOnly bool
}

// startHandoff loads the previous handoff file and runs the startup
// command for a fresh run with handoff on, setting a.TaskPreamble. It
// returns nil when handoff is off.
func startHandoff(ctx context.Context, cfg config.AgentConfig, root string, a *agent.Agent, events *runEvents, warn func(string)) *handoffRun {
	if !cfg.Handoff.Enabled {
		return nil
	}
	h := &handoffRun{cfg: cfg.Handoff, path: handoff.Resolve(root, cfg.Handoff.Path), rel: filepath.ToSlash(filepath.Clean(cfg.Handoff.Path)), readOnly: cfg.ReadOnly}
	loaded, err := handoff.Load(h.path, cfg.Handoff.MaxBytes)
	if err != nil {
		warn("reading handoff file: " + err.Error())
	}
	fields := map[string]any{"path": h.path, "found": loaded != nil, "bytes": 0, "truncated": false}
	if loaded != nil {
		fields["bytes"] = loaded.Bytes
		fields["truncated"] = loaded.Truncated
		if loaded.Truncated {
			warn("handoff file " + h.path + " is longer than handoff.max_bytes; loading only its start")
		}
	}
	events.emit("handoff.loaded", fields)
	var startup *handoff.Startup
	switch {
	case cfg.Handoff.StartupCommand == "":
	case cfg.ReadOnly:
		warn("handoff.startup_command skipped: the run is read-only")
	default:
		s, err := handoff.RunStartup(ctx, root, cfg.Handoff.StartupCommand, time.Duration(cfg.Handoff.StartupTimeoutSeconds)*time.Second, cfg.Handoff.StartupMaxOutput)
		if err != nil {
			warn("handoff.startup_command: " + err.Error())
			break
		}
		startup = &s
		events.emit("handoff.startup", map[string]any{"command": s.Command, "exit_code": s.ExitCode, "timed_out": s.TimedOut, "duration_ms": s.Duration.Milliseconds()})
		if s.TimedOut {
			warn("handoff.startup_command timed out")
		} else if s.ExitCode != 0 {
			warn("handoff.startup_command exited with a non-zero status")
		}
	}
	a.TaskPreamble = handoff.Preamble(loaded, startup, h.rel)
	return h
}

// finish rewrites the handoff file from the finished run t. Read-only runs
// leave it unchanged.
func (h *handoffRun) finish(t *trajectory.Trajectory, modelName, output string, events *runEvents, warn func(string)) {
	if h == nil {
		return
	}
	if h.readOnly {
		return
	}
	answer := t.Submission
	if t.ExitStatus == agent.StatusNeedsInput {
		answer = "The run stopped to ask a question: " + t.Submission
	}
	content := handoff.Render(handoff.Summary{
		Task: t.Task, Status: t.ExitStatus, Answer: answer, Model: modelName,
		At: time.Now(), StartupCommand: h.cfg.StartupCommand, Trajectory: output, Untrusted: t.Tainted,
	})
	if err := handoff.Write(h.path, content); err != nil {
		warn("writing handoff file: " + err.Error())
		return
	}
	events.emit("handoff.saved", map[string]any{"path": h.path, "bytes": len(content), "status": t.ExitStatus})
}
