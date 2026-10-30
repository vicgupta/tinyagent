package main

import (
	"github.com/vicgupta/tinyagent/internal/agent"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// memoryTurn sets up project memory (memory.enabled or --memory) for one
// ACP turn. The session's first turn loads the store like a fresh run: the
// active entries become the Project memory section of the session's system
// prompt, memory.loaded is emitted, and the remember tool is offered unless
// the session is read-only. Later turns continue that conversation like a
// follow-up: they replay the first turn's snapshot (the store is not
// re-read, so writes never reach the session that made them), emit
// memory.loaded for it, and keep offering the same remember tool.
//
// One writer serves the whole session, so max_writes_per_run counts every
// write in the session, and once untrusted content entered the
// conversation every later write stays pending. A session that started
// client-supplied MCP servers (acp.client_mcp_servers) treats its context
// as untrusted from the start, since their tool descriptions come from the
// editor rather than the trusted config.
func (s *acpSession) memoryTurn(a *agent.Agent, prev *trajectory.Trajectory, events *runEvents, warn func(string)) {
	if !s.cfg.Memory.Enabled {
		return
	}
	if prev == nil {
		loadMemory(a, s.cfg.Memory, s.root, warn)
		events.emit("memory.loaded", agent.MemoryLoadedFields(a.Memory))
		s.remember = memoryWriter(a, s.cfg.Memory)
		if s.remember != nil && s.clientMCP {
			s.remember.MarkUntrusted()
		}
	} else {
		emitResumeMemoryLoaded(events, prev, s.cfg.Memory, s.root)
	}
	a.Tools.Remember = s.remember
	a.Tools.MemoryTools = s.remember != nil
}
