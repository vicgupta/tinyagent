package config

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Handoff defaults (issue #251).
const (
	DefaultHandoffPath           = ".tinyagent/handoff.md"
	DefaultHandoffMaxBytes       = 16 * 1024
	DefaultHandoffStartupTimeout = 120
	DefaultHandoffStartupOutput  = 8 * 1024
)

// Handoff configures the opt-in session handoff file (issue #251): a run
// reads the file left by the previous session at start and rewrites it at
// the end. Like memory it comes only from -c, a trusted ./tinyagent.yaml (--trust-config) or a
// profile, never from the repository, so a repository can't set the
// startup command.
type Handoff struct {
	// Enabled reads and writes the handoff file (off by default; --handoff
	// / --handoff=false override it).
	Enabled bool `yaml:"enabled"`
	// Path is the handoff file, relative to --cwd (default
	// .tinyagent/handoff.md). It must stay inside --cwd.
	Path string `yaml:"path"`
	// MaxBytes caps how much of the file is loaded into the first message
	// (default 16 KiB); a longer file is cut and marked as truncated.
	MaxBytes int `yaml:"max_bytes"`
	// StartupCommand is an optional shell command run in --cwd before the
	// first turn; its exit code and output are given to the model as data.
	StartupCommand string `yaml:"startup_command"`
	// StartupTimeoutSeconds bounds the startup command (default 120).
	StartupTimeoutSeconds int `yaml:"startup_timeout_seconds"`
	// StartupMaxOutput caps the startup output given to the model (default
	// 8 KiB; the tail is kept).
	StartupMaxOutput int `yaml:"startup_max_output"`
}

// DefaultHandoff returns the handoff defaults (off).
func DefaultHandoff() Handoff {
	return Handoff{
		Path:                  DefaultHandoffPath,
		MaxBytes:              DefaultHandoffMaxBytes,
		StartupTimeoutSeconds: DefaultHandoffStartupTimeout,
		StartupMaxOutput:      DefaultHandoffStartupOutput,
	}
}

// Validate reports an invalid handoff setting.
func (h Handoff) Validate() error {
	p := strings.TrimSpace(h.Path)
	switch {
	case p == "":
		return fmt.Errorf("handoff.path must not be empty")
	case filepath.IsAbs(p):
		return fmt.Errorf("handoff.path must be relative to --cwd, got %q", h.Path)
	case filepath.Clean(p) == "." || strings.HasPrefix(filepath.ToSlash(filepath.Clean(p)), "../") || filepath.Clean(p) == "..":
		return fmt.Errorf("handoff.path must name a file inside --cwd, got %q", h.Path)
	case h.MaxBytes < 1:
		return fmt.Errorf("handoff.max_bytes must be >= 1, got %d", h.MaxBytes)
	case h.StartupTimeoutSeconds < 1:
		return fmt.Errorf("handoff.startup_timeout_seconds must be >= 1, got %d", h.StartupTimeoutSeconds)
	case h.StartupMaxOutput < 1:
		return fmt.Errorf("handoff.startup_max_output must be >= 1, got %d", h.StartupMaxOutput)
	}
	return nil
}
