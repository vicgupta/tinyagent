package main

import (
	"errors"
	"flag"
	"fmt"
	"strings"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/memory"
)

// checkMemory returns the --check memory line (issue #170), or "" when
// project memory is off and --memory wasn't passed. It only reads the
// store: a missing store is reported as empty and never created, and an
// unreadable or corrupt one is reported, not an error, since a run warns
// and continues without memory.
func checkMemory(fs *flag.FlagSet, o *options, cfg config.Memory, root string) string {
	src := "from config"
	if len(memorySource.Env) > 0 {
		src = "from config and " + strings.Join(memorySource.Env, ", ")
	}
	if memorySource.File != "" {
		src += ", memory.toml " + memorySource.File
	}
	if flagWasSet(fs, "memory") {
		cfg.Enabled = o.memory.enabled
		src = "from --memory"
	}
	if !cfg.Enabled {
		if src == "from --memory" {
			return "off (from --memory)"
		}
		return ""
	}
	head := fmt.Sprintf("on, %s backend, approval %s (%s)", cfg.Backend, cfg.Approval, src)
	path, err := memoryStorePath(cfg, root)
	if err != nil {
		return fmt.Sprintf("%s, store unavailable: %v", head, err)
	}
	if h := oldMemoryHint(cfg, root, path); h != "" {
		head += ", " + strings.TrimPrefix(h, "hint: ")
	}
	entries, err := memory.Load(path)
	if err != nil {
		var ce *memory.CorruptError
		if errors.As(err, &ce) {
			return fmt.Sprintf("%s, store corrupt (runs continue without memory): %s", head, path)
		}
		return fmt.Sprintf("%s, store unreadable (runs continue without memory): %s", head, path)
	}
	var active, pending int
	for _, e := range entries {
		switch e.Status {
		case memory.StatusActive:
			active++
		case memory.StatusPending:
			pending++
		}
	}
	return fmt.Sprintf("%s, %d active, %d pending: %s", head, active, pending, path)
}
