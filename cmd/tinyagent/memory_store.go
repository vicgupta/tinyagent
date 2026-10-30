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

// legacyMemoryPath returns the pre-markdown JSON store for root, or "".
func legacyMemoryPath(root string) string {
	p, err := memory.StorePath("", root)
	if err != nil {
		return ""
	}
	return p
}

// openMemoryStore resolves the store for root and, with import_legacy on,
// copies the legacy JSON store into it once when it doesn't exist yet.
func openMemoryStore(cfg config.Memory, root string, warn func(string)) (string, error) {
	path, err := memoryStorePath(cfg, root)
	if err != nil {
		return "", err
	}
	if cfg.ImportLegacy {
		legacy := legacyMemoryPath(root)
		n, err := memory.ImportLegacy(path, legacy)
		switch {
		case err != nil:
			warn(fmt.Sprintf("project memory: importing legacy store %s: %v", legacy, err))
		case n > 0:
			warn(fmt.Sprintf("project memory: imported %d %s from legacy store %s into %s (the old file was kept)", n, plural(n, "entry", "entries"), legacy, path))
		}
	}
	return path, nil
}
