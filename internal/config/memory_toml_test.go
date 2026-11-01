package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func envMap(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

func writeTOML(t *testing.T, dir, body string) string {
	t.Helper()
	p := filepath.Join(dir, "memory.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestMemoryHelpListsEveryKey(t *testing.T) {
	want := []string{"backend", "path", "sqlite_path", "auto_write", "require_approval", "max_entry_size", "max_entries_per_project", "default_tags", "import_legacy"}
	if len(MemoryKeys) != len(want) {
		t.Fatalf("MemoryKeys has %d keys, want %d", len(MemoryKeys), len(want))
	}
	help := MemoryHelp()
	for i, name := range want {
		k := MemoryKeys[i]
		if k.Name != name {
			t.Errorf("key %d = %s, want %s", i, k.Name, name)
		}
		line := "# " + name + " = " + k.Default
		if !strings.Contains(help, line) {
			t.Errorf("help lacks %q", line)
		}
		if !strings.Contains(help, k.Type+": "+k.Desc) || !strings.Contains(help, "TINYAGENT_MEMORY_"+strings.ToUpper(name)) {
			t.Errorf("help lacks the type, description or env var for %s", name)
		}
	}
	for _, line := range strings.Split(strings.TrimRight(help, "\n"), "\n") {
		if line != "" && !strings.HasPrefix(line, "#") {
			t.Errorf("help line is not a TOML comment: %q", line)
		}
	}
}

// TestMemoryHelpDefaultsMatchLoader checks every documented default by
// uncommenting the help lines and loading them: the result must equal
// DefaultMemory.
func TestMemoryHelpDefaultsMatchLoader(t *testing.T) {
	var body strings.Builder
	for _, line := range strings.Split(MemoryHelp(), "\n") {
		for _, k := range MemoryKeys {
			if strings.HasPrefix(line, "# "+k.Name+" = ") {
				body.WriteString(strings.TrimPrefix(line, "# ") + "\n")
			}
		}
	}
	p := writeTOML(t, t.TempDir(), body.String())
	m := DefaultMemory()
	if _, err := ApplyMemoryOverlay(&m, t.TempDir(), envMap(map[string]string{MemoryConfigEnv: p})); err != nil {
		t.Fatal(err)
	}
	d := DefaultMemory()
	if m.Backend != d.Backend || m.Enabled != d.Enabled || m.Approval != d.Approval || m.MaxEntrySize != d.MaxEntrySize ||
		m.MaxEntries != d.MaxEntries || len(m.DefaultTags) != 0 || m.ImportLegacy != d.ImportLegacy || m.Path != "./memory" || m.SQLitePath != "./memory/memory.sqlite" {
		t.Errorf("help defaults load as %+v, defaults are %+v", m, d)
	}
}

func TestMemoryPrecedenceEnvOverTOMLOverYAML(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir()) // os.UserConfigDir ignores XDG_CONFIG_HOME on macOS
	user := writeTOML(t, t.TempDir(), "backend = \"sqlite\"\nmax_entries_per_project = 20\nmax_entry_size = 300\nauto_write = true\nrequire_approval = false\ndefault_tags = [\"Proj\"]\n")
	m := DefaultMemory()
	m.MaxEntries = 10 // from the memory: YAML section
	m.MaxWritesPerRun = 3
	src, err := ApplyMemoryOverlay(&m, t.TempDir(), envMap(map[string]string{
		MemoryConfigEnv:                            user,
		"TINYAGENT_MEMORY_MAX_ENTRY_SIZE":          "200",
		"TINYAGENT_MEMORY_DEFAULT_TAGS":            "a, b",
		"TINYAGENT_MEMORY_REQUIRE_APPROVAL":        "true",
		"TINYAGENT_MEMORY_MAX_ENTRIES_PER_PROJECT": "",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if src.File != user || src.Local {
		t.Errorf("source %+v", src)
	}
	if m.Backend != "sqlite" || m.MaxEntries != 20 || m.MaxEntrySize != 200 || !m.Enabled || m.Approval != "pending" || m.MaxWritesPerRun != 3 {
		t.Errorf("merged %+v", m)
	}
	if strings.Join(m.DefaultTags, ",") != "a,b" {
		t.Errorf("tags %v", m.DefaultTags)
	}
	if len(src.Env) != 3 {
		t.Errorf("env keys %v", src.Env)
	}
}

func TestMemoryConfigLookupOrder(t *testing.T) {
	cfgHome := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgHome)
	t.Setenv("HOME", t.TempDir())
	cwd := t.TempDir()
	if p, _, _ := FindMemoryConfig(cwd, envMap(nil)); p != "" {
		t.Fatalf("found %s with no config", p)
	}
	if err := os.MkdirAll(filepath.Join(cfgHome, "tinyagent"), 0o700); err != nil {
		t.Fatal(err)
	}
	user := writeTOML(t, filepath.Join(cfgHome, "tinyagent"), "")
	if dir, err := os.UserConfigDir(); err != nil || filepath.Join(dir, "tinyagent", "memory.toml") != user {
		t.Skip("user config dir does not follow XDG_CONFIG_HOME here")
	}
	if p, local, _ := FindMemoryConfig(cwd, envMap(nil)); p != user || local {
		t.Errorf("user: %s %v", p, local)
	}
	repo := writeTOML(t, cwd, "")
	if p, local, _ := FindMemoryConfig(cwd, envMap(nil)); p != repo || !local {
		t.Errorf("repo: %s %v", p, local)
	}
	explicit := writeTOML(t, t.TempDir(), "")
	if p, local, _ := FindMemoryConfig(cwd, envMap(map[string]string{MemoryConfigEnv: explicit})); p != explicit || local {
		t.Errorf("env: %s %v", p, local)
	}
	if _, _, err := FindMemoryConfig(cwd, envMap(map[string]string{MemoryConfigEnv: filepath.Join(cwd, "nope.toml")})); err == nil {
		t.Error("missing TINYAGENT_MEMORY_CONFIG accepted")
	}
}

func TestRepoLocalMemoryTOMLOnlyMakesMemorySafer(t *testing.T) {
	cwd := t.TempDir()
	writeTOML(t, cwd, "auto_write = true\nrequire_approval = false\nmax_entries_per_project = 500\nmax_entry_size = 100\n")
	m := DefaultMemory()
	src, err := ApplyMemoryOverlay(&m, cwd, envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	if !src.Local || m.Enabled || m.Approval != "pending" || m.MaxEntries != DefaultMemoryMaxEntries || m.MaxEntrySize != 100 {
		t.Errorf("local overlay gave %+v (source %+v)", m, src)
	}
	if len(src.Warnings) != 3 {
		t.Errorf("warnings %q", src.Warnings)
	}
	for _, body := range []string{"path = \"/etc/x\"\n", "path = \"../outside\"\n", "sqlite_path = \"../x.sqlite\"\n"} {
		writeTOML(t, cwd, body)
		m := DefaultMemory()
		src, err := ApplyMemoryOverlay(&m, cwd, envMap(nil))
		if err != nil || m.Path != "" || m.SQLitePath != "" || len(src.Warnings) != 1 {
			t.Errorf("local %q: %+v, warnings %q, err %v", body, m, src.Warnings, err)
		}
	}
	writeTOML(t, cwd, "path = \"notes\"\nbackend = \"sqlite\"\n")
	m = DefaultMemory()
	if _, err := ApplyMemoryOverlay(&m, cwd, envMap(nil)); err != nil || m.Path != "notes" || m.Backend != "sqlite" {
		t.Errorf("relative local path: %+v %v", m, err)
	}
	// The env still wins over a local file.
	writeTOML(t, cwd, "auto_write = true\n")
	m = DefaultMemory()
	if _, err := ApplyMemoryOverlay(&m, cwd, envMap(map[string]string{"TINYAGENT_MEMORY_AUTO_WRITE": "true"})); err != nil || !m.Enabled {
		t.Errorf("env auto_write: %+v %v", m, err)
	}
}

func TestMemoryTOMLErrors(t *testing.T) {
	for _, body := range []string{"bogus = 1\n", "backend = \"redis\"\n", "max_entry_size = 99999\n", "auto_write = \"maybe\"\n", "not toml ="} {
		p := writeTOML(t, t.TempDir(), body)
		m := DefaultMemory()
		_, err := ApplyMemoryOverlay(&m, t.TempDir(), envMap(map[string]string{MemoryConfigEnv: p}))
		var ie *InvalidError
		if err == nil || !asInvalid(err, &ie) {
			t.Errorf("%q: err %v", body, err)
		}
	}
	m := DefaultMemory()
	if _, err := ApplyMemoryOverlay(&m, t.TempDir(), envMap(map[string]string{"TINYAGENT_MEMORY_MAX_ENTRY_SIZE": "lots"})); err == nil {
		t.Error("bad env int accepted")
	}
}

func asInvalid(err error, target **InvalidError) bool {
	e, ok := err.(*InvalidError)
	*target = e
	return ok
}
