package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeEnvConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// #410: env: is a mapping of names to strings, merged by profiles; errors
// name the variable, never the value.
func TestEnvConfig(t *testing.T) {
	cfg, err := LoadProfile(writeEnvConfig(t, "env:\n  A: \"1\"\n  B: base\nprofiles:\n  p:\n    env:\n      B: over\n      C: \"\"\n"), "p")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Env["A"] != "1" || cfg.Env["B"] != "over" || cfg.Env["C"] != "" || len(cfg.Env) != 3 {
		t.Errorf("env = %v", EnvNames(cfg.Env))
	}
	for _, c := range []struct{ body, want string }{
		{"env: [A]\n", "env must be a mapping"},
		{"env:\n  A: 4242424242\n", "env A must be a string; quote it"},
		{"env:\n  A: true\n", "env A must be a string"},
		{"env:\n  A:\n", "env A must be a string"},
		{"env:\n  \"A=B\": \"x9x9x9\"\n", "must not contain ="},
		{"env:\n  \"A*\": \"x9x9x9\"\n", "plain name"},
	} {
		_, err := Load(writeEnvConfig(t, c.body))
		if err == nil || !strings.Contains(err.Error(), c.want) || strings.Contains(err.Error(), "4242424242") || strings.Contains(err.Error(), "x9x9x9") {
			t.Errorf("%q: %v", c.body, err)
		}
	}
}

func TestStripUntrustedEnv(t *testing.T) {
	cfg := Default()
	cfg.Env = map[string]string{"OPENAI_API_KEY": "k", "PATH": "/x", "ld_preload": "y", "GIT_SSH_COMMAND": "z", "FOO": "f"}
	got := StripUntrusted(&cfg)
	if strings.Join(got, ",") != "env.GIT_SSH_COMMAND,env.PATH,env.ld_preload" {
		t.Errorf("stripped %v", got)
	}
	if strings.Join(EnvNames(cfg.Env), ",") != "FOO,OPENAI_API_KEY" {
		t.Errorf("kept %v", EnvNames(cfg.Env))
	}
	cfg.Env = map[string]string{"PATH": "/x"}
	StripUntrusted(&cfg)
	if cfg.Env != nil {
		t.Errorf("env = %v", cfg.Env)
	}
}
