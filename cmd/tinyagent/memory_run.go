package main

import (
	"errors"
	"fmt"

	"github.com/vicgupta/tinyagent/internal/agent"
	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/memory"
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

func entryWord(n int) string {
	if n == 1 {
		return "entry"
	}
	return "entries"
}
