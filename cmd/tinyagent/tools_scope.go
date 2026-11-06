package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/tools"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// toolsFlag is a repeatable --tools flag. Each value may be a single name
// or a comma-separated list. An empty Set ("") or a lone "*" means clear
// scoping (override a config/profile tools: list).
type toolsFlag struct {
	tokens []string
	clear  bool
	set    bool
}

func (f *toolsFlag) String() string {
	if f.clear {
		return "*"
	}
	return strings.Join(f.tokens, ",")
}

func (f *toolsFlag) Set(v string) error {
	f.set = true
	if strings.TrimSpace(v) == "" || strings.TrimSpace(v) == "*" {
		f.clear = true
		f.tokens = nil
		return nil
	}
	if f.clear {
		return errors.New("--tools=* (or \"\") clears scoping; do not also pass tool names")
	}
	f.tokens = append(f.tokens, v)
	return nil
}

// resolveScopedTools picks the effective tools: list for this invocation.
// Precedence: --no-tool-scope / --tools=* / --tools="" clears; else --tools
// replaces config; else config/profile tools:; else unset (all tools).
func resolveScopedTools(fs *flag.FlagSet, o options, cfg *config.AgentConfig) ([]string, error) {
	_ = fs
	if o.noToolScope && o.tools.set && !o.tools.clear {
		return nil, errors.New("--no-tool-scope and --tools conflict")
	}
	if o.noToolScope {
		cfg.Tools = nil
		return nil, nil
	}
	if o.tools.set {
		if o.tools.clear {
			cfg.Tools = nil
			return nil, nil
		}
		only, err := tools.NormalizeScopedTools(o.tools.tokens)
		if err != nil {
			return nil, fmt.Errorf("--tools: %w", err)
		}
		cfg.Tools = only
		return only, nil
	}
	if len(cfg.Tools) == 0 {
		return nil, nil
	}
	only, err := tools.NormalizeScopedTools(cfg.Tools)
	if err != nil {
		return nil, fmt.Errorf("tools: %w", err)
	}
	cfg.Tools = only
	return only, nil
}

// validateRunTools checks that e.Only names are offered by the run's
// catalog. Call after newRunExecutor (which copies cfg.Tools into Only).
func validateRunTools(e *tools.Executor) error {
	if len(e.Only) == 0 {
		return nil
	}
	return tools.ValidateScopedTools(e.Only, e.Selection())
}

// checkResumeScopedTools exits-style error when --tools / --no-tool-scope
// conflicts with a checkpoint's scoped_tools (matching --web-fetch resume).
func checkResumeScopedTools(fs *flag.FlagSet, o options, src *trajectory.Trajectory) error {
	_ = fs
	want := append([]string(nil), src.ScopedTools...)
	switch {
	case o.noToolScope:
		if len(want) == 0 {
			return nil
		}
		return fmt.Errorf("--no-tool-scope conflicts with the checkpoint's scoped_tools (%s); start a fresh task", strings.Join(want, ", "))
	case o.tools.set && o.tools.clear:
		if len(want) == 0 {
			return nil
		}
		return fmt.Errorf("--tools=* conflicts with the checkpoint's scoped_tools (%s); start a fresh task", strings.Join(want, ", "))
	case o.tools.set:
		got, err := tools.NormalizeScopedTools(o.tools.tokens)
		if err != nil {
			return fmt.Errorf("--tools: %w", err)
		}
		if tools.EqualScopedTools(got, want) {
			return nil
		}
		gotS, wantS := strings.Join(got, ","), strings.Join(want, ",")
		if wantS == "" {
			wantS = "(none)"
		}
		if gotS == "" {
			gotS = "(none)"
		}
		return fmt.Errorf("--tools %s conflicts with the checkpoint's scoped_tools %s; start a fresh task", gotS, wantS)
	}
	return nil
}

func describeScopedTools(w io.Writer, only []string) {
	if len(only) == 0 {
		return
	}
	fmt.Fprintf(w, "tools: scoped to %s\n", strings.Join(only, ", "))
}
