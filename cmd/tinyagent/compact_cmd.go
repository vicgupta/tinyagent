package main

import (
	"context"
	"fmt"
	"io"

	"github.com/vicgupta/tinyagent/internal/agent"
	"github.com/vicgupta/tinyagent/internal/tools"
)

// compactNow immediately compacts the conversation (chat and web
// /compact). It writes a progress line and the result (tokens before →
// after, or that nothing could be compacted) to w. On error the stored
// history is left unchanged; the caller should report a next step.
// When path is not empty and anything was compacted, the checkpoint is
// saved there so the result survives without another turn.
func (s *session) compactNow(ctx context.Context, w io.Writer, path string) error {
	if s.traj == nil {
		fmt.Fprintln(w, "nothing to compact yet")
		return nil
	}
	if s.o.noCompact {
		fmt.Fprintln(w, "compaction is off (--no-compact)")
		return nil
	}
	a := s.compactAgent(w)
	fmt.Fprintln(w, "compacting...")
	before := len(s.traj.Compactions)
	if err := a.CompactNow(ctx, s.traj); err != nil {
		return err
	}
	if path != "" && len(s.traj.Compactions) != before {
		if err := s.traj.Save(path); err != nil {
			return err
		}
	}
	return nil
}

// compactAgent is a lightweight Agent that can run CompactNow with the
// session's model, helpers and skill catalog (so skill loads are not
// elided). It does not start LSP servers or open a turn's event log.
func (s *session) compactAgent(w io.Writer) *agent.Agent {
	e := newRunExecutor(s.cfg, toolWiring{
		root:        s.root,
		env:         s.env,
		cmdTools:    s.cmdTools,
		extSpecs:    s.extSpecs,
		extCall:     s.extCall,
		mcp:         s.mcp,
		subagents:   s.subagents,
		workers:     s.workers,
		webFetch:    s.webFetch,
		webSearch:   s.webSearch,
		outputFiles: s.outputFiles,
		fileAccess:  s.fileAccess,
		extraDirs:   s.extraDirs,
		redactor:    s.redactor,
	})
	if s.cfg.LSP.Enabled && e.LSP == nil {
		e.LSP = &tools.LSP{}
	}
	a := &agent.Agent{
		Model:          s.model,
		ModelName:      s.modelName,
		FallbackModels: s.fallbacks,
		Config:         s.cfg,
		Helpers:        s.helpers,
		Tools:          e,
		SkipAgentsMD:   s.o.noAgentsMD,
		NoCompact:      s.o.noCompact,
		Out:            io.Discard,
		Err:            w,
	}
	if s.cfg.SkillsOn() {
		loadSkills(a, s.root, pluginSkillSources(s.cfg), func(string) {})
	}
	return a
}
