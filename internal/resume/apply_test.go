package resume

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

func TestApplyPinsRecordedSettings(t *testing.T) {
	all := &trajectory.Trajectory{
		ReadOnly: true, PlanEnabled: true, ImagesEnabled: true, ApplyPatchEnabled: true,
		SubagentsEnabled: true, WorkersEnabled: true, AskUserEnabled: true, WebFetchEnabled: true,
		LSPEnabled: true, ScopedAgentsMDEnabled: true, SkillsEnabled: true, MemoryEnabled: true,
	}
	tests := []struct {
		name string
		tr   *trajectory.Trajectory
		cfg  config.AgentConfig
		on   bool
	}{
		{"all recorded on, config off", all, config.AgentConfig{}, true},
		{"all recorded off, config on", &trajectory.Trajectory{}, allOnConfig(), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tc.cfg
			Apply(tc.tr, &cfg)
			got := map[string]bool{
				"read_only":        cfg.ReadOnly,
				"plan":             cfg.Plan.Enabled,
				"images":           cfg.Images.Enabled,
				"apply_patch":      cfg.ApplyPatch,
				"subagents":        cfg.Subagents.Enabled,
				"workers":          cfg.Subagents.Workers != nil && cfg.Subagents.Workers.Enabled,
				"ask_user":         cfg.AskUser.Enabled,
				"web_fetch":        cfg.WebFetch.Enabled,
				"lsp":              cfg.LSP.Enabled,
				"scoped_agents_md": cfg.ScopedAgentsMD,
				"skills":           cfg.SkillsOn(),
				"memory":           cfg.Memory.Enabled,
			}
			for k, v := range got {
				if v != tc.on {
					t.Errorf("%s = %v, want %v", k, v, tc.on)
				}
			}
		})
	}
}

func allOnConfig() config.AgentConfig {
	var c config.AgentConfig
	c.ReadOnly, c.ApplyPatch, c.ScopedAgentsMD, c.Skills = true, true, true, config.BoolPtr(true)
	c.Plan.Enabled, c.Images.Enabled, c.Subagents.Enabled = true, true, true
	c.Subagents.Workers = &config.Workers{Enabled: true}
	c.AskUser.Enabled, c.WebFetch.Enabled, c.LSP.Enabled, c.Memory.Enabled = true, true, true, true
	return c
}

func TestApplyWorkersDoesNotMutateSharedValue(t *testing.T) {
	shared := &config.Workers{Enabled: true}
	cfg := config.AgentConfig{}
	cfg.Subagents.Workers = shared
	Apply(&trajectory.Trajectory{}, &cfg)
	if !shared.Enabled {
		t.Fatal("Apply mutated the shared workers block")
	}
	if cfg.Subagents.Workers.Enabled {
		t.Fatal("workers still on after applying a checkpoint without workers")
	}
	off := config.AgentConfig{}
	Apply(&trajectory.Trajectory{}, &off)
	if off.Subagents.Workers != nil {
		t.Fatal("Apply created a workers block for a checkpoint without workers")
	}
}

func TestLoadRejectsInvalidCheckpoint(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(path, []byte(`{"unknown_field": 1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load accepted a checkpoint with unknown fields")
	}
	if _, err := Load(filepath.Join(dir, "missing.json")); err == nil {
		t.Fatal("Load accepted a missing checkpoint")
	}
}
