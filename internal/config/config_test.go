package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeUserDirs points os.UserConfigDir at a temp dir on every OS and returns
// the default config path inside it.
func fakeUserDirs(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AppData", filepath.Join(home, "AppData"))
	p, err := DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p, home) || !strings.HasSuffix(p, filepath.Join("tinyagent", "config.yaml")) {
		t.Fatalf("DefaultPath = %q, want <%s/...>/tinyagent/config.yaml", p, home)
	}
	return p
}

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func load(t *testing.T, explicit string) (AgentConfig, error) {
	t.Helper()
	return Load(Find(explicit))
}

func TestNoUserConfigUsesDefaults(t *testing.T) {
	fakeUserDirs(t)
	if got := Find(""); got != "" {
		t.Errorf("Find(\"\") = %q, want \"\" when no user config exists", got)
	}
	cfg, err := load(t, "")
	if err != nil || cfg != Default() {
		t.Errorf("load = %+v, %v; want the defaults", cfg, err)
	}
}

func TestUserConfigOverridesDefaults(t *testing.T) {
	p := fakeUserDirs(t)
	write(t, p, "step_limit: 50\nbash_timeout_seconds: 300\n")
	if got := Find(""); got != p {
		t.Errorf("Find(\"\") = %q, want %q", got, p)
	}
	cfg, err := load(t, "")
	if err != nil || cfg.StepLimit != 50 || cfg.BashTimeoutSec != 300 || cfg.MaxOutputChars != Default().MaxOutputChars {
		t.Errorf("load = %+v, %v; want step_limit 50, bash timeout 300, other defaults kept", cfg, err)
	}
}

func TestExplicitConfigWins(t *testing.T) {
	p := fakeUserDirs(t)
	write(t, p, "step_limit: 50\n")
	explicit := filepath.Join(t.TempDir(), "ci.yaml")
	write(t, explicit, "step_limit: 7\n")
	if got := Find(explicit); got != explicit {
		t.Errorf("Find(%q) = %q", explicit, got)
	}
	if cfg, err := load(t, explicit); err != nil || cfg.StepLimit != 7 {
		t.Errorf("load = %+v, %v; want step_limit 7 from the explicit file", cfg, err)
	}
}

func TestMissingExplicitConfigFails(t *testing.T) {
	p := fakeUserDirs(t)
	write(t, p, "step_limit: 50\n")
	missing := filepath.Join(t.TempDir(), "nope.yaml")
	if _, err := load(t, missing); err == nil || !strings.Contains(err.Error(), missing) {
		t.Errorf("err = %v, want a read error naming %s (no fallback to the user config)", err, missing)
	}
}

func TestBadUserConfigFailsNamingPath(t *testing.T) {
	p := fakeUserDirs(t)
	write(t, p, "step_limit: [not a number\n")
	if _, err := load(t, ""); err == nil || !strings.Contains(err.Error(), p) {
		t.Errorf("malformed: err = %v, want a parse error naming %s", err, p)
	}

	p = fakeUserDirs(t)
	if err := os.MkdirAll(p, 0o755); err != nil { // a directory where the file should be
		t.Fatal(err)
	}
	if _, err := load(t, ""); err == nil || !strings.Contains(err.Error(), p) {
		t.Errorf("unreadable: err = %v, want a read error naming %s", err, p)
	}
}

func TestNoUserConfigDir(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("AppData", "")
	if got := Find(""); got != "" {
		t.Errorf("Find(\"\") = %q, want \"\" when there's no user config dir", got)
	}
}

func TestUnknownKeyFailsNamingPathAndKey(t *testing.T) {
	p := fakeUserDirs(t)
	write(t, p, "cost_limit: 2.0\nstep_limt: 20\n")
	if _, err := load(t, ""); err == nil || !strings.Contains(err.Error(), p) || !strings.Contains(err.Error(), "step_limt") {
		t.Errorf("user config: err = %v, want an error naming %s and step_limt", err, p)
	}

	explicit := filepath.Join(t.TempDir(), "ci.yaml")
	write(t, explicit, "step_limit: 7\nwall_time_secs: 60\n")
	if _, err := load(t, explicit); err == nil || !strings.Contains(err.Error(), explicit) || !strings.Contains(err.Error(), "wall_time_secs") {
		t.Errorf("explicit config: err = %v, want an error naming %s and wall_time_secs", err, explicit)
	}
}

func TestEmptyConfigUsesDefaults(t *testing.T) {
	for name, data := range map[string]string{"empty": "", "comments only": "# step_limit: 50\n\n# nothing set\n"} {
		p := filepath.Join(t.TempDir(), "config.yaml")
		write(t, p, data)
		if cfg, err := Load(p); err != nil || cfg != Default() {
			t.Errorf("%s: Load = %+v, %v; want the defaults", name, cfg, err)
		}
	}
}
