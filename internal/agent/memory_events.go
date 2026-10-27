package agent

import "github.com/vicgupta/tinyagent/internal/trajectory"

// recordMemorySaves emits a memory.saved event for each project memory
// write the last tool call made (issue #170).
func (a *Agent) recordMemorySaves() {
	if a.Tools.Remember == nil {
		return
	}
	for _, s := range a.Tools.Remember.TakeSaved() {
		a.event("memory.saved", s.Fields())
	}
}

// MemoryLoadedFields returns the memory.loaded event fields for m: the
// store path, the loaded entry ids (newest first) and how many active
// entries were omitted. Entry text is never included.
func MemoryLoadedFields(m *ProjectMemory) map[string]any {
	ids := make([]string, 0, len(m.Lines))
	for _, l := range m.Lines {
		ids = append(ids, l.ID)
	}
	return map[string]any{"path": m.Path, "loaded": len(m.Lines), "ids": ids, "omitted": m.Omitted}
}

// ResumeMemoryLoadedFields returns the memory.loaded event fields for a
// resumed or follow-up run of checkpoint t, which replays the checkpoint's
// memory snapshot instead of reading the store: the same fields as a fresh
// run, with path set to path (the store the snapshot came from, or "" when
// it can't be resolved) and from_checkpoint true. It returns nil when the
// checkpoint ran with memory off.
func ResumeMemoryLoadedFields(t *trajectory.Trajectory, path string) map[string]any {
	m := checkpointMemory(t)
	if m == nil {
		return nil
	}
	m.Path = path
	f := MemoryLoadedFields(m)
	f["from_checkpoint"] = true
	return f
}
