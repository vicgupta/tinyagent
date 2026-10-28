package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPluginsConfig(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	os.Mkdir(a, 0o755)
	os.Mkdir(b, 0o755)
	file := filepath.Join(dir, "file")
	os.WriteFile(file, nil, 0o644)
	write := func(body string) string {
		p := filepath.Join(t.TempDir(), "c.yaml")
		os.WriteFile(p, []byte(body), 0o644)
		return p
	}
	cfg, err := Load(write("plugins:\n  - path: " + a + "\n  - path: " + b + "\n"))
	if err != nil || len(cfg.Plugins) != 2 || cfg.Plugins[1].Path != b {
		t.Fatalf("%+v, %v", cfg.Plugins, err)
	}
	for name, c := range map[string]struct{ body, want string }{
		"relative":    {"plugins:\n  - path: rel/dir\n", "must be an absolute path"},
		"missing":     {"plugins:\n  - path: " + filepath.Join(dir, "nope") + "\n", "not an existing directory"},
		"file":        {"plugins:\n  - path: " + file + "\n", "not an existing directory"},
		"empty":       {"plugins:\n  - {}\n", "plugins[0].path is required"},
		"duplicate":   {"plugins:\n  - path: " + a + "\n  - path: " + a + "/\n", "duplicates plugins[0].path"},
		"unknown key": {"plugins:\n  - path: " + a + "\n    pth: x\n", "plugins[0]"},
		"not a list":  {"plugins:\n  path: " + a + "\n", "must be a list"},
	} {
		p := write(c.body)
		_, err := Load(p)
		var invalid *InvalidError
		if err == nil || !errors.As(err, &invalid) || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), p) {
			t.Errorf("%s: %v", name, err)
		}
	}
	p := write("plugins:\n  - path: " + a + "\nprofiles:\n  solo:\n    plugins:\n      - path: " + b + "\n")
	cfg, err = LoadProfile(p, "solo")
	if err != nil || len(cfg.Plugins) != 1 || cfg.Plugins[0].Path != b {
		t.Errorf("profile: %+v, %v", cfg.Plugins, err)
	}
}
