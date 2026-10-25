package config

import (
	"path/filepath"
	"testing"
)

func TestScopedAgentsMDConfig(t *testing.T) {
	if Default().ScopedAgentsMD {
		t.Fatal("scoped_agents_md is on by default")
	}
	for data, want := range map[string]bool{"scoped_agents_md: true\n": true, "scoped_agents_md: false\n": false, "": false} {
		p := filepath.Join(t.TempDir(), "c.yaml")
		write(t, p, data)
		cfg, err := Load(p)
		if err != nil {
			t.Errorf("%q: %v", data, err)
			continue
		}
		if cfg.ScopedAgentsMD != want {
			t.Errorf("%q: got %v, want %v", data, cfg.ScopedAgentsMD, want)
		}
	}
	for _, data := range []string{"scoped_agents_md: maybe\n", "scoped_agents_md: [1]\n", "scoped_agents_md:\n  enabled: true\n"} {
		p := filepath.Join(t.TempDir(), "c.yaml")
		write(t, p, data)
		if _, err := Load(p); err == nil {
			t.Errorf("%q: accepted", data)
		}
	}
}
