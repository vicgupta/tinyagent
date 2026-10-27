package config

import "fmt"

// Memory configures opt-in cross-run project memory (issue #170). Like
// mcp_servers and redact_secrets it comes only from the user config, -c or
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
)

// DefaultMemory returns the memory defaults: off, pending approval.
func DefaultMemory() Memory {
	return Memory{
		Approval:        DefaultMemoryApproval,
		MaxWritesPerRun: DefaultMemoryMaxWritesPerRun,
		MaxEntries:      DefaultMemoryMaxEntries,
		MaxBytes:        DefaultMemoryMaxBytes,
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
	return nil
}
