package agent

import (
	"github.com/vicgupta/tinyagent/internal/memory"
	"github.com/vicgupta/tinyagent/internal/tools"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// ProjectMemory is the project memory a run loaded at start (issue #170):
// the active entries rendered into its system prompt. Writes during the run
// never change it, so the run that saves an entry never sees it.
type ProjectMemory struct {
	// Path is the store file the entries came from.
	Path string
	// Lines are the loaded entries, newest first.
	Lines []memory.Line
	// Omitted counts active entries left out to stay within max_bytes.
	Omitted int
}

// section returns the Project memory system prompt section, or "" when m
// is nil or holds no entries.
func (m *ProjectMemory) section() string {
	if m == nil {
		return ""
	}
	return memory.RenderLines(m.Lines, m.Omitted)
}

// memorySection returns the Project memory section that leads the
// system prompt for a run with memory on, separated from what precedes it.
func memorySection(m *ProjectMemory) string {
	s := m.section()
	if s == "" {
		return ""
	}
	return "\n\n" + s
}

// recordMemory stores the loaded snapshot in a new trajectory so a resume
// or follow-up can rebuild the same prompt without reading the store.
func (a *Agent) recordMemory(t *trajectory.Trajectory) {
	if a.Memory == nil {
		return
	}
	t.MemoryEnabled = true
	t.MemoryOmitted = a.Memory.Omitted
	for _, l := range a.Memory.Lines {
		t.Memory = append(t.Memory, trajectory.MemoryLine{ID: l.ID, Date: l.Date, Text: l.Text})
	}
}

// checkpointMemory rebuilds the ProjectMemory recorded in t, or nil when
// the checkpoint ran with memory off.
func checkpointMemory(t *trajectory.Trajectory) *ProjectMemory {
	if !t.MemoryEnabled {
		return nil
	}
	m := &ProjectMemory{Omitted: t.MemoryOmitted}
	for _, l := range t.Memory {
		m.Lines = append(m.Lines, memory.Line{ID: l.ID, Date: l.Date, Text: l.Text})
	}
	return m
}

// RememberOffered reports whether a resumed checkpoint offered the remember
// tool, so the resumed run offers the same tool set. Checkpoints written
// before the tool existed resume without it.
func RememberOffered(t *trajectory.Trajectory) bool {
	if !t.MemoryEnabled || t.ReadOnly {
		return false
	}
	for _, name := range t.Tools {
		if name == tools.RememberToolName {
			return true
		}
	}
	return false
}

// ResumeUntrusted reports whether a checkpoint's conversation already holds
// untrusted external content (a review diff, a web_fetch result or an
// external tool result), so a resumed run's memory writes stay pending.
func ResumeUntrusted(t *trajectory.Trajectory) bool {
	if t.Review != nil {
		return true
	}
	external := map[string]bool{tools.WebFetchToolName: true}
	for _, name := range t.ExternalTools {
		external[name] = true
	}
	for _, m := range t.Messages {
		for _, c := range m.ToolCalls {
			if external[c.Function.Name] {
				return true
			}
		}
	}
	return false
}
