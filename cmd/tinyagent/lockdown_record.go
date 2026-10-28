package main

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/vicgupta/tinyagent/internal/agent"
	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/lockdown"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// setupResumeLockdown is setupLockdown for --resume and follow-ups. Taint
// is sticky: the checkpoint's recorded taint sources and denials carry
// over, an older checkpoint whose history holds untrusted content starts
// tainted as before, and a tainted checkpoint can't be continued with
// lockdown off (exit 2): it fails closed and needs a fresh run.
func setupResumeLockdown(fs *flag.FlagSet, l lockdownFlags, cfg *config.AgentConfig, src *trajectory.Trajectory, stderr io.Writer) (*lockdown.State, int) {
	sources, denials := agent.ResumeLockdown(src)
	untrusted := agent.ResumeUntrusted(src) && len(sources) == 0
	st, code := setupLockdown(fs, l, cfg, false, untrusted, stderr)
	if code != 0 {
		return nil, code
	}
	if st == nil {
		if src.Tainted {
			fmt.Fprintf(stderr, "error: resume: the checkpoint is tainted by untrusted content (%s); continue it with --lockdown (or untrusted_content.enabled), or start a fresh run\n", taintSourceNames(src.TaintSources))
			return nil, 2
		}
		return nil, 0
	}
	st.Restore(sources, denials)
	return st, 0
}

// taintSourceNames lists the distinct source names, in order.
func taintSourceNames(sources []trajectory.TaintSource) string {
	var names []string
	seen := map[string]bool{}
	for _, s := range sources {
		if !seen[s.Source] {
			seen[s.Source] = true
			names = append(names, s.Source)
		}
	}
	if len(names) == 0 {
		return "unknown source"
	}
	return strings.Join(names, ", ")
}

// lockdownSummary is the run summary line's lockdown part: empty unless
// the run is tainted.
func lockdownSummary(t *trajectory.Trajectory) string {
	if !t.Tainted {
		return ""
	}
	s := ", tainted"
	if len(t.TaintSources) > 0 {
		first := t.TaintSources[0]
		s += " by " + first.Source
		if first.Step > 0 {
			s += fmt.Sprintf(" at step %d", first.Step)
		}
	}
	switch n := len(t.LockdownDenials); n {
	case 0:
	case 1:
		s += " (1 call refused)"
	default:
		s += fmt.Sprintf(" (%d calls refused)", n)
	}
	return s
}

// addLockdownResult adds the taint state to the result event of a run
// with lockdown on.
func addLockdownResult(fields map[string]any, t *trajectory.Trajectory) {
	if !t.LockdownEnabled {
		return
	}
	fields["tainted"] = t.Tainted
	fields["lockdown_denials"] = len(t.LockdownDenials)
}
