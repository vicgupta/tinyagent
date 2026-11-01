package config

import "fmt"

// Memory configures opt-in cross-run project memory (issue #170). Like
// mcp_servers and redact_secrets it comes only from -c, a trusted ./tinyagent.yaml (--trust-config) or
// a profile, never from the repository.
type Memory struct {
	// Enabled loads the project's active memory entries into the system
	// prompt (off by default; --memory / --memory=false override it).
	Enabled bool `yaml:"enabled"`
	// Approval is the policy for new entries: pending (default) holds
	// every new entry until a person approves it; auto saves entries as
	// active unless the run had untrusted content in context.
	Approval string `yaml:"approval"`
	// MaxWritesPerRun caps the entries one run may add, replace or forget
	// (default 5).
	MaxWritesPerRun int `yaml:"max_writes_per_run"`
	// MaxEntries caps the active entries in the store (default 50).
	MaxEntries int `yaml:"max_entries"`
	// MaxBytes caps the Project memory section added to the system
	// prompt (default 8 KiB); the newest entries that fit are loaded.
	MaxBytes int `yaml:"max_bytes"`

	// The fields below are normally set in memory.toml or TINYAGENT_MEMORY_*
	// (see memory_toml.go); the yaml keys accept the same values.

	// Backend selects the store: markdown (default; one <id>.md per entry
	// in Path) or sqlite (one database at SQLitePath).
	Backend string `yaml:"backend"`
	// Path is the markdown folder, relative to --cwd and inside it
	// (default ./.tinyagent/memory).
	Path string `yaml:"path"`
	// SQLitePath is the SQLite database (default
	// ./.tinyagent/memory/memory.sqlite).
	SQLitePath string `yaml:"sqlite_path"`
	// MaxEntrySize caps one entry's text, in characters (default 500).
	MaxEntrySize int `yaml:"max_entry_size"`
	// DefaultTags are added to every new entry.
	DefaultTags []string `yaml:"default_tags"`
	// ImportLegacy is ignored: it is still accepted so older configs load,
	// but memory is never imported from outside --cwd (issue #335).
	ImportLegacy bool `yaml:"import_legacy"`
}

// Memory defaults and limits.
const (
	DefaultMemoryApproval        = "pending"
	DefaultMemoryMaxWritesPerRun = 5
	DefaultMemoryMaxEntries      = 50
	DefaultMemoryMaxBytes        = 8 << 10
	// MaxMemoryMaxBytes bounds memory.max_bytes so the section can't crowd
	// out the task (64 KiB, twice the AGENTS.md cap).
	MaxMemoryMaxBytes = 64 << 10
	// DefaultMemoryMaxEntrySize and MaxMemoryEntrySize bound one entry's
	// text in characters.
	DefaultMemoryMaxEntrySize = 500
	MaxMemoryEntrySize        = 4000
	DefaultMemoryBackend      = "markdown"
)

// DefaultMemory returns the memory defaults: off, pending approval.
func DefaultMemory() Memory {
	return Memory{
		Approval:        DefaultMemoryApproval,
		MaxWritesPerRun: DefaultMemoryMaxWritesPerRun,
		MaxEntries:      DefaultMemoryMaxEntries,
		MaxBytes:        DefaultMemoryMaxBytes,
		Backend:         DefaultMemoryBackend,
		MaxEntrySize:    DefaultMemoryMaxEntrySize,
		ImportLegacy:    true,
	}
}

// Validate checks the approval policy and limits.
func (m Memory) Validate() error {
	if m.Approval != "pending" && m.Approval != "auto" {
		return fmt.Errorf("memory.approval %q must be pending or auto", m.Approval)
	}
	if m.MaxWritesPerRun < 1 {
		return fmt.Errorf("memory.max_writes_per_run must be a whole number >= 1, got %d", m.MaxWritesPerRun)
	}
	if m.MaxEntries < 1 {
		return fmt.Errorf("memory.max_entries must be a whole number >= 1, got %d", m.MaxEntries)
	}
	if m.MaxBytes < 1 || m.MaxBytes > MaxMemoryMaxBytes {
		return fmt.Errorf("memory.max_bytes must be a whole number from 1 to %d, got %d", MaxMemoryMaxBytes, m.MaxBytes)
	}
	if m.Backend != "markdown" && m.Backend != "sqlite" {
		return fmt.Errorf("memory.backend %q must be markdown or sqlite", m.Backend)
	}
	if m.MaxEntrySize < 1 || m.MaxEntrySize > MaxMemoryEntrySize {
		return fmt.Errorf("memory.max_entry_size must be a whole number from 1 to %d, got %d", MaxMemoryEntrySize, m.MaxEntrySize)
	}
	return nil
}
