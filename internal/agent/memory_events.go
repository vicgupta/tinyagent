package agent

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
