package memory

// Saved is one successful write a Writer made, reported as a memory.saved
// event: the result plus the entry's source (the untrusted-context flag).
type Saved struct {
	Result
	UntrustedContext bool
}

// record keeps r for TakeSaved and returns it. Callers hold w.mu.
func (w *Writer) record(r Result) Result {
	w.saved = append(w.saved, Saved{Result: r, UntrustedContext: w.untrusted})
	return r
}

// TakeSaved returns the writes made since the last call and forgets them.
func (w *Writer) TakeSaved() []Saved {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := w.saved
	w.saved = nil
	return s
}

// Fields returns s as memory.saved event fields. The entry text is never
// included, only its id and status.
func (s Saved) Fields() map[string]any {
	return map[string]any{"op": s.Op, "id": s.ID, "status": s.Status, "redacted": s.Redacted, "untrusted_context": s.UntrustedContext}
}
