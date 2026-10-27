package agent

import (
	"github.com/vicgupta/tinyagent/internal/memory"
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
