package resume

import (
	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// Load reads and structurally validates the checkpoint at path. It is the
// one entry point a resume uses to read a checkpoint, so the size limit,
// strict decoding, and validation can't differ between callers.
func Load(path string) (*trajectory.Trajectory, error) {
	return trajectory.Load(path)
}

// Apply pins every setting the checkpoint recorded onto cfg, so a resumed
// run's config selects exactly the tools and context the checkpoint was
// built with. Call it after Check has accepted the command-line flags; the
// fields it sets are the same ones the flag table in Check compares, which
// keeps the resumed run's agent.Options built from cfg the same way a fresh
// run's are.
func Apply(t *trajectory.Trajectory, cfg *config.AgentConfig) {
	cfg.ReadOnly = t.ReadOnly
	cfg.Plan.Enabled = t.PlanEnabled
	cfg.Images.Enabled = t.ImagesEnabled
	cfg.ApplyPatch = t.ApplyPatchEnabled
	cfg.Subagents.Enabled = t.SubagentsEnabled
	setWorkers(cfg, t.WorkersEnabled)
	cfg.AskUser.Enabled = t.AskUserEnabled
	cfg.WebFetch.Enabled = t.WebFetchEnabled
	cfg.LSP.Enabled = t.LSPEnabled
	cfg.ScopedAgentsMD = t.ScopedAgentsMDEnabled
	cfg.Skills = t.SkillsEnabled
	cfg.Memory.Enabled = t.MemoryEnabled
}

// setWorkers turns the workers pool on or off without mutating a Workers
// value cfg may share with another config copy. Off with no workers block
// leaves the block absent.
func setWorkers(cfg *config.AgentConfig, on bool) {
	if cfg.Subagents.Workers == nil {
		if !on {
			return
		}
		cfg.Subagents.Workers = &config.Workers{}
	} else {
		w := *cfg.Subagents.Workers
		cfg.Subagents.Workers = &w
	}
	cfg.Subagents.Workers.Enabled = on
}
