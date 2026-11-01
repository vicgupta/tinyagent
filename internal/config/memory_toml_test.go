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
	cwd := t.TempDir()
	p := writeTOML(t, cwd, body.String())
	m := DefaultMemory()
	if _, err := ApplyMemoryOverlay(&m, cwd, envMap(map[string]string{MemoryConfigEnv: p})); err != nil {
		t.Fatal(err)
	}
	d := DefaultMemory()
	if m.Backend != d.Backend || m.Enabled != d.Enabled || m.Approval != d.Approval || m.MaxEntrySize != d.MaxEntrySize ||
		m.MaxEntries != d.MaxEntries || len(m.DefaultTags) != 0 || m.ImportLegacy != d.ImportLegacy || m.Path != "./.tinyagent/memory" || m.SQLitePath != "./.tinyagent/memory/memory.sqlite" {
		t.Errorf("help defaults load as %+v, defaults are %+v", m, d)
	}
}

func TestMemoryPrecedenceEnvOverTOMLOverYAML(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir()) // os.UserConfigDir ignores XDG_CONFIG_HOME on macOS
	cwd := t.TempDir()
	user := writeTOML(t, cwd, "backend = \"sqlite\"\nmax_entries_per_project = 20\nmax_entry_size = 300\nauto_write = true\nrequire_approval = false\ndefault_tags = [\"Proj\"]\n")
	m := DefaultMemory()
	m.MaxEntries = 10 // from the memory: YAML section
	m.MaxWritesPerRun = 3
	src, err := ApplyMemoryOverlay(&m, cwd, envMap(map[string]string{
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
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	cwd := t.TempDir()
	if l, _ := FindMemoryConfig(cwd, envMap(nil)); l.Path != "" || l.Hint != "" {
		t.Fatalf("found %+v with no config", l)
	}
	repo := writeTOML(t, cwd, "")
	if l, _ := FindMemoryConfig(cwd, envMap(nil)); !sameFile(l.Path, repo) || !l.Local {
		t.Errorf("repo: %+v", l)
	}
	if err := os.MkdirAll(filepath.Join(cwd, "cfg"), 0o700); err != nil {
		t.Fatal(err)
	}
	explicit := writeTOML(t, filepath.Join(cwd, "cfg"), "")
	for _, p := range []string{explicit, "cfg/memory.toml"} {
		if l, _ := FindMemoryConfig(cwd, envMap(map[string]string{MemoryConfigEnv: p})); !sameFile(l.Path, explicit) || l.Local {
			t.Errorf("env %s: %+v", p, l)
		}
	}
	if _, err := FindMemoryConfig(cwd, envMap(map[string]string{MemoryConfigEnv: filepath.Join(cwd, "nope.toml")})); err == nil {
		t.Error("missing TINYAGENT_MEMORY_CONFIG accepted")
	}
}

// TestMemoryConfigOnlyFromCwd covers issue #336: memory.toml in the old
// user config location (with HOME, XDG_* and the user config dir pointed at
// a temp dir that holds it) is never used, only hinted at once, and paths
// that leave cwd are refused with a warning.
func TestMemoryConfigOnlyFromCwd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("AppData", filepath.Join(home, "AppData"))
	dir, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "tinyagent"), 0o700); err != nil {
		t.Fatal(err)
	}
	old := writeTOML(t, filepath.Join(dir, "tinyagent"), "auto_write = true\nmax_entry_size = 300\n")
	for _, d := range []string{"XDG_STATE_HOME", "XDG_CACHE_HOME"} {
		if err := os.MkdirAll(filepath.Join(os.Getenv(d), "tinyagent"), 0o700); err != nil {
			t.Fatal(err)
		}
		writeTOML(t, filepath.Join(os.Getenv(d), "tinyagent"), "auto_write = true\n")
	}

	cwd := t.TempDir()
	m := DefaultMemory()
	src, err := ApplyMemoryOverlay(&m, cwd, envMap(nil))
	if err != nil {
		t.Fatal(err)
	}
	if src.File != "" || m.Enabled || m.MaxEntrySize != DefaultMemory().MaxEntrySize {
		t.Errorf("old user memory.toml was used: %+v, %+v", src, m)
	}
	if !strings.HasPrefix(src.Hint, "hint: ") || !strings.Contains(src.Hint, old) || strings.Contains(src.Hint, "\n") {
		t.Errorf("hint %q, want one line naming %s", src.Hint, old)
	}

	// A memory.toml in cwd wins and the hint is not printed.
	writeTOML(t, cwd, "")
	if src, err := ApplyMemoryOverlay(&m, cwd, envMap(nil)); err != nil || src.Hint != "" || !src.Local {
		t.Errorf("with ./memory.toml: %+v, %v", src, err)
	}

	// TINYAGENT_MEMORY_CONFIG outside cwd is refused with a warning, not read.
	outside := writeTOML(t, t.TempDir(), "auto_write = true\n")
	for _, p := range []string{outside, "../memory.toml", old} {
		m := DefaultMemory()
		src, err := ApplyMemoryOverlay(&m, cwd, envMap(map[string]string{MemoryConfigEnv: p}))
		if err != nil || m.Enabled || len(src.Warnings) != 1 || !strings.Contains(src.Warnings[0], "ignored") {
			t.Errorf("env %s: %+v, %+v, %v; want it refused with a warning", p, src, m, err)
		}
	}

	// ./memory.toml that is a symlink to a file outside cwd is refused.
	cwd = t.TempDir()
	if err := os.Symlink(outside, filepath.Join(cwd, "memory.toml")); err != nil {
		t.Skip("symlinks unavailable:", err)
	}
	m = DefaultMemory()
	src, err = ApplyMemoryOverlay(&m, cwd, envMap(nil))
	if err != nil || src.File != "" || m.Enabled || len(src.Warnings) != 1 {
		t.Errorf("symlinked ./memory.toml: %+v, %+v, %v; want it refused with a warning", src, m, err)
	}
}

// sameFile compares two paths after resolving symlinks (macOS temp dirs
// live under /var, a symlink to /private/var).
func sameFile(a, b string) bool {
	ra, err1 := filepath.EvalSymlinks(a)
	rb, err2 := filepath.EvalSymlinks(b)
	return err1 == nil && err2 == nil && ra == rb
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
		cwd := t.TempDir()
		p := writeTOML(t, cwd, body)
		m := DefaultMemory()
		_, err := ApplyMemoryOverlay(&m, cwd, envMap(map[string]string{MemoryConfigEnv: p}))
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
