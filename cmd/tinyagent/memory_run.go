package main

import (
	"errors"
	"fmt"

	"github.com/vicgupta/tinyagent/internal/agent"
	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/eventlog"
	"github.com/vicgupta/tinyagent/internal/memory"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// loadMemory loads cwd's project memory into a fresh run (memory.enabled
// or --memory): the newest active entries that fit within max_bytes become
// the Project memory section after AGENTS.md. Pending entries never load.
// A store that can't be read or parsed warns once and the run continues
// with memory on but empty; a missing store loads nothing and creates
// nothing.
func loadMemory(a *agent.Agent, cfg config.Memory, root string, warn func(string)) {
	pm := &agent.ProjectMemory{}
	a.Memory = pm
	path, err := memory.StorePath("", root)
	if err != nil {
		warn(fmt.Sprintf("project memory: %v; continuing without memory", err))
		return
	}
	pm.Path = path
	entries, err := memory.Load(path)
	if err != nil {
		var ce *memory.CorruptError
		if errors.As(err, &ce) {
			warn(fmt.Sprintf("project memory: %v; continuing without memory (fix or remove the file, or run --memory-forget all --memory-yes)", err))
		} else {
			warn(fmt.Sprintf("project memory: reading %s: %v; continuing without memory", path, err))
		}
		return
	}
	r := memory.Render(entries, cfg.MaxBytes)
	pm.Lines = memory.Lines(r.Loaded)
	pm.Omitted = r.Omitted
	if r.Omitted > 0 {
		warn(fmt.Sprintf("project memory: %d older %s omitted to stay within memory.max_bytes (%d)", r.Omitted, entryWord(r.Omitted), cfg.MaxBytes))
	}
	if a.Verbose {
		fmt.Fprintf(a.Out, "project memory: loaded %d %s (%d omitted) from %s\n", len(r.Loaded), entryWord(len(r.Loaded)), r.Omitted, path)
	}
}

// memoryWriter returns the remember tool's writer for a fresh run with
// memory on, or nil in read-only (and so review) runs, which never write.
// It is offered whenever memory is enabled, whatever the store holds: with
// no usable store path, its calls fail with an error. replace and forget
// may target only the entries a.Memory loaded at start.
func memoryWriter(a *agent.Agent, cfg config.Memory) *memory.Writer {
	if a.Tools.ReadOnly {
		return nil
	}
	var path string
	var snapshot []memory.Entry
	if a.Memory != nil {
		path = a.Memory.Path
		for _, l := range a.Memory.Lines {
			snapshot = append(snapshot, memory.Entry{ID: l.ID})
		}
	}
	limits := memory.Limits{MaxWritesPerRun: cfg.MaxWritesPerRun, MaxEntries: cfg.MaxEntries}
	src := memory.Source{Model: a.ModelName, Version: currentVersion()}
	return memory.NewWriter(path, cfg.Approval, limits, a.Tools.Redactor, src, snapshot)
}

// resumeMemoryWriter returns the remember tool's writer for a resumed run
// whose checkpoint offered it: replace and forget may target the entries in
// the checkpoint's snapshot, and writes stay pending when the conversation
// already holds untrusted content.
func resumeMemoryWriter(src *trajectory.Trajectory, cfg config.Memory, root, modelName string, red *eventlog.Redactor, warn func(string)) *memory.Writer {
	path, err := memory.StorePath("", root)
	if err != nil {
		warn(fmt.Sprintf("project memory: %v; remember calls will fail", err))
		path = ""
	}
	var snapshot []memory.Entry
	for _, l := range src.Memory {
		snapshot = append(snapshot, memory.Entry{ID: l.ID})
	}
	limits := memory.Limits{MaxWritesPerRun: cfg.MaxWritesPerRun, MaxEntries: cfg.MaxEntries}
	srcInfo := memory.Source{Model: modelName, Version: currentVersion(), UntrustedContext: agent.ResumeUntrusted(src)}
	return memory.NewWriter(path, cfg.Approval, limits, red, srcInfo, snapshot)
}

func entryWord(n int) string {
	if n == 1 {
		return "entry"
	}
	return "entries"
}
