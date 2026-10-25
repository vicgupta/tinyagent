package config

import (
	"path/filepath"
	"testing"
)

func TestSnapshotsConfig(t *testing.T) {
	if d := Default().Snapshots; d.Enabled || d.MaxFileBytes != DefaultSnapshotMaxFileBytes {
		t.Fatalf("default = %+v", d)
	}
	ok := map[string]Snapshots{
		"snapshots: true\n":  {Enabled: true, MaxFileBytes: DefaultSnapshotMaxFileBytes},
		"snapshots: false\n": {MaxFileBytes: DefaultSnapshotMaxFileBytes},
		"snapshots:\n":       {MaxFileBytes: DefaultSnapshotMaxFileBytes},
		"snapshots:\n  enabled: true\n  max_file_bytes: 1024\n": {Enabled: true, MaxFileBytes: 1024},
		"snapshots:\n  max_file_bytes: 99\n":                    {MaxFileBytes: 99},
	}
	for data, want := range ok {
		p := filepath.Join(t.TempDir(), "c.yaml")
		write(t, p, data)
		cfg, err := Load(p)
		if err != nil {
			t.Errorf("%q: %v", data, err)
			continue
		}
		if cfg.Snapshots != want {
			t.Errorf("%q: got %+v, want %+v", data, cfg.Snapshots, want)
		}
	}
	for _, data := range []string{
		"snapshots: yes please\n",
		"snapshots:\n  enabled: 3\n",
		"snapshots:\n  retention: 5\n",
		"snapshots:\n  max_file_bytes: 0\n",
		"snapshots:\n  max_file_bytes: -1\n",
		"snapshots: [1]\n",
	} {
		p := filepath.Join(t.TempDir(), "c.yaml")
		write(t, p, data)
		if _, err := Load(p); err == nil {
			t.Errorf("%q: accepted", data)
		}
	}
}
