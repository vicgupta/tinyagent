package memory

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/vicgupta/tinyagent/internal/eventlog"
)

// Result describes one successful write.
type Result struct {
	Op       string `json:"op"`
	ID       string `json:"id"`
	Status   string `json:"status"`
	Redacted bool   `json:"redacted"`
}

// Writer applies one run's memory writes to the store. It enforces the
// per-run write limit, the store's active-entry limit, the approval policy
// and redaction. Writes go to the store only; they never change the run's
// own prompt. It is safe for concurrent use.
type Writer struct {
	Path     string
	Approval string
	Limits   Limits
	// Redactor masks configured secret values; token-shaped strings are
	// always masked, whether or not it is set.
	Redactor *eventlog.Redactor
	Source   Source

	mu        sync.Mutex
	writes    int
	known     map[string]bool // ids loaded at run start or added this run
	untrusted bool
	saved     []Saved // successful writes not yet taken by TakeSaved
	now       func() time.Time
}

// NewWriter returns a Writer for path whose replace and forget operations
// may target the ids in snapshot (the entries loaded at run start).
func NewWriter(path, approval string, limits Limits, red *eventlog.Redactor, src Source, snapshot []Entry) *Writer {
	w := &Writer{Path: path, Approval: approval, Limits: limits, Redactor: red, Source: src, known: map[string]bool{}, now: func() time.Time { return time.Now().UTC() }}
	for _, e := range snapshot {
		w.known[e.ID] = true
	}
	if src.UntrustedContext {
		w.untrusted = true
	}
	return w
}

// MarkUntrusted records that untrusted external content (a fetched web
// page, an MCP result, a review diff) entered the run's context; every later
// write is saved pending with source.untrusted_context set.
func (w *Writer) MarkUntrusted() {
	w.mu.Lock()
	w.untrusted = true
	w.mu.Unlock()
}

// Untrusted reports whether MarkUntrusted was called.
func (w *Writer) Untrusted() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.untrusted
}

// Writes returns how many writes succeeded in this run.
func (w *Writer) Writes() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writes
}

func (w *Writer) redact(s string) (string, bool) {
	before := s
	if w.Redactor != nil {
		s, _ = w.Redactor.RedactCount(s)
	}
	// A nil Redactor masks token-shaped strings, which are always masked.
	s = (*eventlog.Redactor)(nil).Redact(s)
	return s, s != before
}

func (w *Writer) status() string {
	if w.untrusted || w.Approval != ApprovalAuto {
		return StatusPending
	}
	return StatusActive
}

func (w *Writer) begin() error {
	max := w.Limits.MaxWritesPerRun
	if max <= 0 {
		max = DefaultMaxWritesPerRun
	}
	if w.writes >= max {
		return fmt.Errorf("memory write limit reached: max_writes_per_run is %d; nothing was saved", max)
	}
	return nil
}

func (w *Writer) maxEntries() int {
	if w.Limits.MaxEntries <= 0 {
		return DefaultMaxEntries
	}
	return w.Limits.MaxEntries
}

func (w *Writer) source() Source {
	s := w.Source
	s.UntrustedContext = w.untrusted
	return s
}

// Add saves a new entry.
func (w *Writer) Add(text string) (Result, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.begin(); err != nil {
		return Result{}, err
	}
	text, err := normalizeText(text)
	if err != nil {
		return Result{}, err
	}
	text, red := w.redact(text)
	status := w.status()
	var id string
	err = Update(w.Path, func(entries []Entry) ([]Entry, error) {
		if status == StatusActive && countActive(entries) >= w.maxEntries() {
			return nil, fmt.Errorf("memory is full: max_entries is %d active entries; forget or replace one first", w.maxEntries())
		}
		now := w.now()
		id = newID(entries)
		return append(entries, Entry{ID: id, Text: text, Status: status, CreatedAt: now, UpdatedAt: now, Source: w.source()}), nil
	})
	if err != nil {
		return Result{}, err
	}
	w.writes++
	w.known[id] = true
	return w.record(Result{Op: "add", ID: id, Status: status, Redacted: red}), nil
}

// Replace changes the text of entry id. When the new text would be pending
// and id is active, a pending replacement is saved instead and the active
// entry stays in place until the replacement is approved.
func (w *Writer) Replace(id, text string) (Result, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.begin(); err != nil {
		return Result{}, err
	}
	if !w.known[id] {
		return Result{}, fmt.Errorf("memory entry %s wasn't loaded at the start of this run or added in it; only those can be replaced", id)
	}
	text, err := normalizeText(text)
	if err != nil {
		return Result{}, err
	}
	text, red := w.redact(text)
	status := w.status()
	res := Result{Op: "replace", ID: id, Status: status, Redacted: red}
	err = Update(w.Path, func(entries []Entry) ([]Entry, error) {
		i := find(entries, id)
		if i < 0 {
			return nil, ErrNotFound
		}
		now := w.now()
		if status == StatusPending && entries[i].Status == StatusActive {
			nid := newID(entries)
			res.ID = nid
			return append(entries, Entry{ID: nid, Text: text, Status: StatusPending, CreatedAt: now, UpdatedAt: now, Source: w.source(), Replaces: id}), nil
		}
		if status == StatusActive && entries[i].Status == StatusPending && countActive(entries) >= w.maxEntries() {
			return nil, fmt.Errorf("memory is full: max_entries is %d active entries; forget or replace one first", w.maxEntries())
		}
		e := &entries[i]
		e.Text, e.Status, e.UpdatedAt, e.Source = text, status, now, w.source()
		return entries, nil
	})
	if err != nil {
		return Result{}, err
	}
	w.writes++
	w.known[res.ID] = true
	return w.record(res), nil
}

// Forget removes entry id from the store.
func (w *Writer) Forget(id string) (Result, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.begin(); err != nil {
		return Result{}, err
	}
	if !w.known[id] {
		return Result{}, fmt.Errorf("memory entry %s wasn't loaded at the start of this run or added in it; only those can be forgotten", id)
	}
	if err := Forget(w.Path, id); err != nil {
		if errors.Is(err, ErrNotFound) {
			return Result{}, fmt.Errorf("memory entry %s: %w", id, err)
		}
		return Result{}, err
	}
	w.writes++
	return w.record(Result{Op: "forget", ID: id, Status: "forgotten"}), nil
}
