package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandoffDefaultsOff(t *testing.T) {
	h := Default().Handoff
	if h.Enabled || h.Path != DefaultHandoffPath || h.StartupCommand != "" {
		t.Errorf("default handoff = %+v", h)
	}
	if err := h.Validate(); err != nil {
		t.Error(err)
	}
}

func TestHandoffConfig(t *testing.T) {
	write := func(body string) string {
		p := filepath.Join(t.TempDir(), "c.yaml")
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cfg, err := Load(write("handoff:\n  enabled: true\n  path: notes/h.md\n  startup_command: make dev\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Handoff.Enabled || cfg.Handoff.Path != "notes/h.md" || cfg.Handoff.StartupCommand != "make dev" || cfg.Handoff.MaxBytes != DefaultHandoffMaxBytes {
		t.Errorf("handoff = %+v", cfg.Handoff)
	}
	for _, c := range []struct{ body, want string }{
		{"handoff:\n  enabld: true\n", "enabld"},
		{"handoff:\n  path: /tmp/h.md\n", "relative"},
		{"handoff:\n  path: ../h.md\n", "inside --cwd"},
		{"handoff:\n  max_bytes: 0\n", "max_bytes"},
		{"handoff:\n  startup_timeout_seconds: 0\n", "startup_timeout_seconds"},
	} {
		if _, err := Load(write(c.body)); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: err = %v, want %q", c.body, err, c.want)
		}
	}
}
