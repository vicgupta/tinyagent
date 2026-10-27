package config

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestWorkersConfig(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	write(t, p, "subagents:\n  workers:\n    enabled: true\n    model: openai/gpt-5-mini\n    max_steps: 20\n    max_concurrent: 2\n    tools: [read_file, bash]\n")
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	w := cfg.Subagents.Workers
	if w == nil || !w.Enabled || w.Model != "openai/gpt-5-mini" || w.MaxSteps != 20 || w.MaxConcurrent != 2 || !slices.Equal(w.Tools, []string{"read_file", "bash"}) {
		t.Errorf("workers = %+v", w)
	}
	if cfg.Subagents.Enabled {
		t.Error("workers turned delegate subagents on")
	}
	if d := Default(); d.Subagents.Workers != nil {
		t.Errorf("default workers = %+v", d.Subagents.Workers)
	}
	for _, c := range []struct{ body, want string }{
		{"subagents:\n  workers:\n    max_steps: -1\n", "subagents.workers.max_steps"},
		{"subagents:\n  workers:\n    max_concurrent: 5\n", "subagents.workers.max_concurrent"},
		{"subagents:\n  workers:\n    tools: [read_file, delegate]\n", `unknown tool "delegate"`},
	} {
		write(t, p, c.body)
		if _, err := Load(p); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: err = %v, want %q", c.body, err, c.want)
		}
	}
}
