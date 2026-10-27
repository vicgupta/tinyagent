// Package memory is tinyagent's cross-run project memory store: short,
// redacted notes that a run saves about a project and later runs load into
// their system prompt. Each project has one JSON file outside the
// repository, keyed by the repository's Git common directory so every
// worktree of a repository shares it. Entries carry provenance and may be
// held as pending until a person approves them; pending entries are never
// rendered into a prompt.
//
// The package has no model, network or config dependency: callers decide
// when memory is enabled and pass the store path, policy and limits in.
package memory

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// Entry statuses.
const (
	StatusActive  = "active"
	StatusPending = "pending"
)

// Approval policies for new entries.
const (
	// ApprovalPending saves every new entry as pending.
	ApprovalPending = "pending"
	// ApprovalAuto saves new entries as active unless the run had untrusted
	// content in context.
	ApprovalAuto = "auto"
)

// Default limits.
const (
	DefaultMaxWritesPerRun = 5
	DefaultMaxEntries      = 50
	DefaultMaxBytes        = 8 * 1024
	// MaxTextLen is the longest entry text, in characters.
	MaxTextLen = 500
)

// Source records where an entry came from.
type Source struct {
	RunID            string `json:"run_id,omitempty"`
	Model            string `json:"model,omitempty"`
	Version          string `json:"version,omitempty"`
	UntrustedContext bool   `json:"untrusted_context"`
}

// Entry is one saved note.
type Entry struct {
	ID        string    `json:"id"`
	Text      string    `json:"text"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Source    Source    `json:"source"`
	// Replaces is set on a pending entry that, once approved, replaces the
	// active entry with this id.
	Replaces string `json:"replaces,omitempty"`
}

// Limits bound what one run may write and what the store may hold.
type Limits struct {
	MaxWritesPerRun int
	MaxEntries      int
}

// DefaultLimits returns the default per-run and store limits.
func DefaultLimits() Limits {
	return Limits{MaxWritesPerRun: DefaultMaxWritesPerRun, MaxEntries: DefaultMaxEntries}
}

// ValidApproval reports whether s is a known approval policy.
func ValidApproval(s string) bool { return s == ApprovalPending || s == ApprovalAuto }

// ErrNotFound is returned for an id that isn't in the store.
var ErrNotFound = errors.New("no memory entry with that id")

// normalizeText collapses all whitespace runs, including newlines, to single
// spaces so every entry is one paragraph, and enforces the length limit.
// Over-long text is rejected, never truncated.
func normalizeText(s string) (string, error) {
	s = strings.Join(strings.Fields(s), " ")
	if s == "" {
		return "", errors.New("memory text is empty")
	}
	if n := utf8.RuneCountInString(s); n > MaxTextLen {
		return "", fmt.Errorf("memory text is %d characters; the limit is %d (shorten it; nothing was saved)", n, MaxTextLen)
	}
	return s, nil
}

// newID returns a short random id not already used in entries.
func newID(entries []Entry) string {
	used := make(map[string]bool, len(entries))
	for _, e := range entries {
		used[e.ID] = true
	}
	for {
		var b [4]byte
		if _, err := rand.Read(b[:]); err != nil {
			panic(err) // crypto/rand never fails on supported platforms
		}
		id := "m" + hex.EncodeToString(b[:])
		if !used[id] {
			return id
		}
	}
}

func find(entries []Entry, id string) int {
	for i, e := range entries {
		if e.ID == id {
			return i
		}
	}
	return -1
}

func countActive(entries []Entry) int {
	n := 0
	for _, e := range entries {
		if e.Status == StatusActive {
			n++
		}
	}
	return n
}

// Active returns the active entries, newest (by UpdatedAt) first; ties keep
// the later-stored entry first.
func Active(entries []Entry) []Entry {
	var out []Entry
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Status == StatusActive {
			out = append(out, entries[i])
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out
}
