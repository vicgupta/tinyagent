package config

import (
	"errors"
	"strings"
	"testing"
)

func TestMemoryDefaults(t *testing.T) {
	m := Default().Memory
	if m.Enabled || m.Approval != "pending" || m.MaxWritesPerRun != 5 || m.MaxEntries != 50 || m.MaxBytes != 8192 {
		t.Errorf("defaults = %+v", m)
	}
}

func TestMemoryLoad(t *testing.T) {
	cfg, _, err := loadYAML(t, "memory:\n  enabled: true\n  approval: auto\n  max_bytes: 100\n")
	if err != nil {
		t.Fatal(err)
	}
	m := cfg.Memory
	if !m.Enabled || m.Approval != "auto" || m.MaxBytes != 100 || m.MaxEntries != 50 || m.MaxWritesPerRun != 5 {
		t.Errorf("memory = %+v", m)
	}
}

func TestMemoryInvalid(t *testing.T) {
	for _, c := range []struct{ body, want string }{
		{"memory:\n  approval: always\n", "memory.approval"},
		{"memory:\n  approval: \"\"\n", "memory.approval"},
		{"memory:\n  max_writes_per_run: 0\n", "memory.max_writes_per_run"},
		{"memory:\n  max_entries: -1\n", "memory.max_entries"},
		{"memory:\n  max_bytes: 0\n", "memory.max_bytes"},
		{"memory:\n  max_bytes: 1000000\n", "memory.max_bytes"},
		{"memory:\n  enable: true\n", "enable"},
		{"memory: true\n", "memory must be a mapping"},
	} {
		_, _, err := loadYAML(t, c.body)
		var ie *InvalidError
		if err == nil || !errors.As(err, &ie) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q: err = %v, want InvalidError mentioning %q", c.body, err, c.want)
		}
	}
}
