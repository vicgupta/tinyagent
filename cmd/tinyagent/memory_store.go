package main

import (
	"fmt"
	"io"
	"os"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/memory"
)

// memorySource records where the memory settings came from, for --check.
var memorySource config.MemorySource

// applyMemoryConfig layers memory.toml (found from cwd) and the
// TINYAGENT_MEMORY_* variables over cfg.Memory and prints a warning for
// each repository-local setting that was ignored. Callers apply --memory
// afterwards.
func applyMemoryConfig(cfg *config.AgentConfig, cwd string, stderr io.Writer) error {
	src, err := config.ApplyMemoryOverlay(&cfg.Memory, cwd, os.Getenv)
	memorySource = src
	if err != nil {
		return err
	}
	for _, w := range src.Warnings {
		fmt.Fprintln(stderr, "warning:", w)
	}
	return nil
}

// memoryStorePath returns the store for root under cfg: the markdown folder
// (default ./memory) or the SQLite file (default ./memory/memory.sqlite).
func memoryStorePath(cfg config.Memory, root string) (string, error) {
	return memory.ResolveStore(cfg.Backend, cfg.Path, cfg.SQLitePath, root)
}

// openMemoryStore resolves the store for root. Memory lives only inside
// --cwd (issue #335): nothing is imported from older locations. When the
// store doesn't exist yet but the previous default (./memory) holds one,
// warn gets a single hint pointing at the new location.
func openMemoryStore(cfg config.Memory, root string, warn func(string)) (string, error) {
	path, err := memoryStorePath(cfg, root)
	if err != nil {
		return "", err
	}
	if h := oldMemoryHint(cfg, root, path); h != "" {
		warn(h)
	}
	return path, nil
}

// oldMemoryHint returns the one-line hint for a store left at the previous
// default location, or "".
func oldMemoryHint(cfg config.Memory, root, path string) string {
	if memory.Exists(path) {
		return ""
	}
	old := memory.OldDefaultStore(cfg.Backend, root)
	if old == "" || old == path {
		return ""
	}
	return fmt.Sprintf("hint: project memory now lives in ./%s; %s is no longer read (move it there to keep it)", memory.DefaultMarkdownPath, old)
}
