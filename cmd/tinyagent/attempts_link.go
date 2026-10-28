package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/vicgupta/tinyagent/internal/attempts"
	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/eventlog"
	"github.com/vicgupta/tinyagent/internal/telemetry"
)

// addAttemptLink adds the "attempt" field to run.started when this run is
// one attempt of a parent --attempts run, so the log DB row and the trace's
// root span name the group and the attempt number.
func addAttemptLink(started map[string]any) {
	l, ok := attempts.LinkFromEnv(os.Getenv)
	if !ok {
		return
	}
	m := map[string]any{"attempts_id": l.ID, "index": l.Index}
	if l.Count > 0 {
		m["count"] = l.Count
	}
	started["attempt"] = m
}

// attemptsLink ties a parent --attempts run to its children: one group id,
// one log DB (the parent's, which every child is pointed at so worktree
// roots don't scatter it) and one trace with a parent span.
type attemptsLink struct {
	id     string
	dbPath string // "" when the log DB is off
	otel   *telemetry.Exporter
	stderr io.Writer
}

func startAttemptsLink(fs *flag.FlagSet, o options, cfg config.AgentConfig, n int, stderr io.Writer) (*attemptsLink, error) {
	l := &attemptsLink{id: attempts.NewID(), stderr: stderr}
	settings, err := otelSettings(fs, o, cfg)
	if err != nil {
		return nil, err
	}
	if settings != nil {
		e, warning := telemetry.NewAttempts(telemetry.Options{
			Settings: *settings, Model: o.modelName, Task: o.task, Version: currentVersion(),
			Redactor:    eventlog.EnvRedactor(configuredSecrets(cfg)...),
			TraceParent: os.Getenv("TRACEPARENT"), TraceState: os.Getenv("TRACESTATE"),
		}, l.id, n)
		if warning != "" {
			fmt.Fprintln(stderr, "tinyagent:", warning)
		}
		l.otel = e
	}
	root, err := filepath.Abs(o.cwd)
	if err != nil {
		root = o.cwd
	}
	if on, path := logDBPath(fs, o, cfg.LogDB, root); on {
		g := eventlog.AttemptGroup{ID: l.id, Cwd: root, Requested: n, Version: currentVersion(), Started: time.Now()}
		if l.otel != nil {
			g.TraceID = l.otel.TraceID()
		}
		if err := eventlog.StartAttemptGroup(path, g); err != nil {
			fmt.Fprintf(stderr, "tinyagent: warning: %v; attempts are not grouped in the event log\n", err)
		} else {
			l.dbPath = path
		}
	}
	return l, nil
}

// traceParent is the TRACEPARENT each child gets, or "".
func (l *attemptsLink) traceParent() string {
	if l.otel == nil {
		return ""
	}
	return l.otel.TraceParent()
}

// childArgs points a child at the parent's log DB.
func (l *attemptsLink) childArgs(args []string) []string {
	if l.dbPath == "" {
		return args
	}
	return append([]string{"--log-db=" + l.dbPath}, args...)
}

func (l *attemptsLink) finish(rep attempts.Report) {
	if l.dbPath != "" {
		err := eventlog.FinishAttemptGroup(l.dbPath, l.id, eventlog.AttemptGroupResult{
			Run: len(rep.Attempts), Winner: rep.Winner, WinnerBranch: rep.WinnerBranch, StopReason: rep.StopReason,
			CostUSD: rep.TotalCostUSD, WallMS: rep.TotalWallMS, Ended: time.Now()})
		if err != nil {
			fmt.Fprintf(l.stderr, "tinyagent: warning: %v\n", err)
		}
	}
	if l.otel != nil {
		l.otel.FinishAttempts(rep.Winner, len(rep.Attempts), rep.StopReason, rep.TotalCostUSD, rep.TotalWallMS)
		if w := l.otel.Close(); w != "" {
			fmt.Fprintln(l.stderr, "tinyagent:", w)
		}
	}
}
