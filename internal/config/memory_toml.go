package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/vicgupta/tinyagent/internal/cwdpath"
)

// MemoryConfigEnv names an explicit memory.toml file.
const MemoryConfigEnv = "TINYAGENT_MEMORY_CONFIG"

// MemoryEnvPrefix prefixes the per-key environment overrides: the key in
// upper case, e.g. TINYAGENT_MEMORY_BACKEND.
const MemoryEnvPrefix = "TINYAGENT_MEMORY_"

// MemoryKey is one memory.toml key. The loader, the environment overrides
// and --memory-help all read this one table, so they can't drift.
type MemoryKey struct {
	Name    string
	Type    string // string, bool, int or []string
	Default string // TOML literal
	Desc    string
	set     func(m *Memory, v any) error
	// local applies a value from a repository-local ./memory.toml, which
	// may only make memory safer; nil means set is used as is.
	local func(m *Memory, v any) (warning string, err error)
}

// MemoryKeys is the memory.toml schema, in help order.
var MemoryKeys = []MemoryKey{
	{Name: "backend", Type: "string", Default: `"markdown"`, Desc: "store backend: markdown (one <id>.md file per entry in path) or sqlite (one database at sqlite_path)",
		set: func(m *Memory, v any) error { s, err := asString(v); m.Backend = s; return err }},
	{Name: "path", Type: "string", Default: `"./.tinyagent/memory"`, Desc: "markdown folder, relative to --cwd and inside it",
		set:   func(m *Memory, v any) error { s, err := asString(v); m.Path = s; return err },
		local: localPath(func(m *Memory, s string) { m.Path = s })},
	{Name: "sqlite_path", Type: "string", Default: `"./.tinyagent/memory/memory.sqlite"`, Desc: "SQLite database file (.sqlite, .sqlite3 or .db), relative to --cwd and inside it",
		set:   func(m *Memory, v any) error { s, err := asString(v); m.SQLitePath = s; return err },
		local: localPath(func(m *Memory, s string) { m.SQLitePath = s })},
	{Name: "auto_write", Type: "bool", Default: "false", Desc: "turn project memory on: load active entries into the prompt and offer remember, memory_search and memory_list (same as --memory)",
		set: func(m *Memory, v any) error { b, err := asBool(v); m.Enabled = b; return err },
		local: func(m *Memory, v any) (string, error) {
			b, err := asBool(v)
			if err != nil || !b {
				m.Enabled = m.Enabled && b
				return "", err
			}
			return "auto_write = true ignored: a memory.toml in the working directory can't turn memory on (use --memory, the user memory.toml or TINYAGENT_MEMORY_AUTO_WRITE)", nil
		}},
	{Name: "require_approval", Type: "bool", Default: "true", Desc: "hold new entries as pending until approved with --memory-approve (false = approval auto: active unless untrusted content was in context)",
		set: func(m *Memory, v any) error {
			b, err := asBool(v)
			m.Approval = "auto"
			if b {
				m.Approval = "pending"
			}
			return err
		},
		local: func(m *Memory, v any) (string, error) {
			b, err := asBool(v)
			if err != nil || b {
				if b {
					m.Approval = "pending"
				}
				return "", err
			}
			return "require_approval = false ignored: a memory.toml in the working directory can't turn approval off", nil
		}},
	{Name: "max_entry_size", Type: "int", Default: "500", Desc: fmt.Sprintf("longest entry text, in characters (1-%d)", MaxMemoryEntrySize),
		set:   func(m *Memory, v any) error { n, err := asInt(v); m.MaxEntrySize = n; return err },
		local: localMin(func(m *Memory) *int { return &m.MaxEntrySize })},
	{Name: "max_entries_per_project", Type: "int", Default: "50", Desc: "most active entries the store may hold (memory.max_entries)",
		set:   func(m *Memory, v any) error { n, err := asInt(v); m.MaxEntries = n; return err },
		local: localMin(func(m *Memory) *int { return &m.MaxEntries })},
	{Name: "default_tags", Type: "[]string", Default: "[]", Desc: `tags added to every new entry, e.g. ["project-x"] (env: comma-separated)`,
		set: func(m *Memory, v any) error { s, err := asStrings(v); m.DefaultTags = s; return err }},
	{Name: "import_legacy", Type: "bool", Default: "true", Desc: "ignored: accepted so older files still load; memory is never imported from outside --cwd",
		set: func(m *Memory, v any) error { b, err := asBool(v); m.ImportLegacy = b; return err }},
}

func localPath(set func(*Memory, string)) func(*Memory, any) (string, error) {
	return func(m *Memory, v any) (string, error) {
		s, err := asString(v)
		if err != nil {
			return "", err
		}
		c := filepath.Clean(filepath.FromSlash(s))
		if filepath.IsAbs(c) || c == ".." || strings.HasPrefix(c, ".."+string(filepath.Separator)) || strings.HasPrefix(s, "~") {
			return fmt.Sprintf("path %q ignored: a memory.toml in the working directory may only name a store inside it", s), nil
		}
		set(m, s)
		return "", nil
	}
}

func localMin(field func(*Memory) *int) func(*Memory, any) (string, error) {
	return func(m *Memory, v any) (string, error) {
		n, err := asInt(v)
		if err != nil {
			return "", err
		}
		if p := field(m); n < *p {
			*p = n
		} else if n > *p {
			return fmt.Sprintf("%d ignored: a memory.toml in the working directory may only lower limits (keeping %d)", n, *p), nil
		}
		return "", nil
	}
}

func asString(v any) (string, error) {
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s), nil
	}
	return "", fmt.Errorf("want a string, got %v", v)
}

func asBool(v any) (bool, error) {
	switch x := v.(type) {
	case bool:
		return x, nil
	case string:
		b, err := strconv.ParseBool(strings.TrimSpace(x))
		if err != nil {
			return false, fmt.Errorf("want true or false, got %q", x)
		}
		return b, nil
	}
	return false, fmt.Errorf("want true or false, got %v", v)
}

func asInt(v any) (int, error) {
	switch x := v.(type) {
	case int64:
		return int(x), nil
	case int:
		return x, nil
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(x))
		if err != nil {
			return 0, fmt.Errorf("want a whole number, got %q", x)
		}
		return n, nil
	}
	return 0, fmt.Errorf("want a whole number, got %v", v)
}

func asStrings(v any) ([]string, error) {
	switch x := v.(type) {
	case string:
		var out []string
		for _, s := range strings.Split(x, ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
		return out, nil
	case []any:
		out := make([]string, 0, len(x))
		for _, e := range x {
			s, ok := e.(string)
			if !ok {
				return nil, fmt.Errorf("want a list of strings, got %v", v)
			}
			out = append(out, s)
		}
		return out, nil
	}
	return nil, fmt.Errorf("want a list of strings, got %v", v)
}

// MemorySource describes where the memory settings came from, for --check.
type MemorySource struct {
	// File is the memory.toml that was read, or "".
	File string
	// Local is true when File is ./memory.toml in the working directory,
	// whose settings may only make memory safer.
	Local bool
	// Env lists the TINYAGENT_MEMORY_* keys that were applied.
	Env []string
	// Warnings are settings and paths that were ignored, one line each.
	Warnings []string
	// Hint is a one-line "hint: ..." about a memory.toml left in the old
	// user config location, or "".
	Hint string
}

// MemoryLookup is the result of FindMemoryConfig.
type MemoryLookup struct {
	// Path is the memory.toml to read, or "" for none.
	Path string
	// Local is true for ./memory.toml in cwd (repository-local).
	Local bool
	// Warnings are refused paths, one line each.
	Warnings []string
	// Hint is a single "hint: ..." line when a memory.toml exists in the
	// old user config location (which is no longer read), else "".
	Hint string
}

// FindMemoryConfig returns the memory.toml to read. Only files inside cwd
// are used (issue #336): $TINYAGENT_MEMORY_CONFIG if set (relative to cwd,
// or absolute inside it; it must exist), else ./memory.toml in cwd, else
// none. A path that leaves cwd (through "..", an absolute path elsewhere or
// a symlink) is refused with a warning. The old user config location
// (tinyagent/memory.toml under the user config directory) is never read;
// when nothing was found and a file exists there, Hint points at the new
// location.
func FindMemoryConfig(cwd string, getenv func(string) string) (MemoryLookup, error) {
	var l MemoryLookup
	if p := getenv(MemoryConfigEnv); p != "" {
		r, err := cwdpath.Resolve(cwd, p)
		if err != nil {
			l.Warnings = append(l.Warnings, fmt.Sprintf("%s=%s ignored: %v", MemoryConfigEnv, p, err))
		} else if _, err := os.Stat(r); err != nil {
			return l, fmt.Errorf("%s=%s: %w", MemoryConfigEnv, p, err)
		} else {
			l.Path = r
			return l, nil
		}
	}
	if r, err := cwdpath.Resolve(cwd, "memory.toml"); err != nil {
		if _, lerr := os.Lstat(filepath.Join(cwd, "memory.toml")); lerr == nil {
			l.Warnings = append(l.Warnings, fmt.Sprintf("./memory.toml ignored: %v", err))
		}
	} else if fi, err := os.Stat(r); err == nil && !fi.IsDir() {
		l.Path, l.Local = r, true
		return l, nil
	}
	if old := LegacyMemoryConfig(); old != "" {
		l.Hint = fmt.Sprintf("hint: %s is no longer read; memory.toml is read only from --cwd (move it to ./memory.toml, or set %s to a file inside --cwd)", old, MemoryConfigEnv)
	}
	return l, nil
}

// LegacyMemoryConfig returns the old per-user memory.toml
// (tinyagent/memory.toml under the user config directory) when a file
// exists there, else "". It is only stat'ed, for the hint; never read.
func LegacyMemoryConfig() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	p := filepath.Join(dir, "tinyagent", "memory.toml")
	if _, err := os.Stat(p); err != nil {
		return ""
	}
	return p
}

// ApplyMemoryOverlay layers memory.toml and then TINYAGENT_MEMORY_* over m
// (the memory: section of the YAML config and its defaults), so the
// precedence is env > memory.toml > memory: > defaults; command-line flags
// such as --memory are applied by the caller afterwards. The result is
// validated. getenv is os.Getenv outside tests.
func ApplyMemoryOverlay(m *Memory, cwd string, getenv func(string) string) (MemorySource, error) {
	var src MemorySource
	look, err := FindMemoryConfig(cwd, getenv)
	src.Warnings = append(src.Warnings, look.Warnings...)
	src.Hint = look.Hint
	if err != nil {
		return src, &InvalidError{err}
	}
	path, local := look.Path, look.Local
	if path != "" {
		src.File, src.Local = path, local
		var raw map[string]any
		md, err := toml.DecodeFile(path, &raw)
		if err != nil {
			return src, &InvalidError{fmt.Errorf("memory config %s: %w", path, err)}
		}
		_ = md
		for name := range raw {
			if memoryKey(name) == nil {
				return src, &InvalidError{fmt.Errorf("memory config %s: unknown key %q (see tinyagent --memory-help)", path, name)}
			}
		}
		for _, k := range MemoryKeys {
			v, ok := raw[k.Name]
			if !ok {
				continue
			}
			if local && k.local != nil {
				w, err := k.local(m, v)
				if err != nil {
					return src, &InvalidError{fmt.Errorf("memory config %s: %s: %w", path, k.Name, err)}
				}
				if w != "" {
					src.Warnings = append(src.Warnings, fmt.Sprintf("memory config %s: %s: %s", path, k.Name, w))
				}
				continue
			}
			if err := k.set(m, v); err != nil {
				return src, &InvalidError{fmt.Errorf("memory config %s: %s: %w", path, k.Name, err)}
			}
		}
	}
	for _, k := range MemoryKeys {
		env := MemoryEnvPrefix + strings.ToUpper(k.Name)
		v := getenv(env)
		if v == "" {
			continue
		}
		if err := k.set(m, v); err != nil {
			return src, &InvalidError{fmt.Errorf("%s: %w", env, err)}
		}
		src.Env = append(src.Env, env)
	}
	if err := m.Validate(); err != nil {
		where := "memory settings"
		if path != "" {
			where = "memory config " + path
		}
		return src, &InvalidError{fmt.Errorf("%s: %w", where, err)}
	}
	return src, nil
}

func memoryKey(name string) *MemoryKey {
	for i := range MemoryKeys {
		if MemoryKeys[i].Name == name {
			return &MemoryKeys[i]
		}
	}
	return nil
}

// MemoryHelp returns the memory.toml reference printed by --memory-help:
// every key with its default, type and description, as commented TOML
// that can be pasted into a memory.toml.
func MemoryHelp() string {
	var b strings.Builder
	b.WriteString("# memory.toml: project memory settings for tinyagent.\n")
	b.WriteString("# Lookup (first found wins): $" + MemoryConfigEnv + " (a file inside --cwd), then\n")
	b.WriteString("# ./memory.toml in --cwd. Nothing outside --cwd is read.\n")
	b.WriteString("# Precedence: " + MemoryEnvPrefix + "<KEY> env vars > memory.toml > memory: in config.yaml > defaults;\n")
	b.WriteString("# --memory/--memory=false overrides auto_write. A ./memory.toml in --cwd can't turn\n")
	b.WriteString("# memory or auto-approval on, raise limits, or point outside --cwd.\n")
	b.WriteString("# Uncomment and change the lines you need. Defaults shown.\n\n")
	w := 0
	for _, k := range MemoryKeys {
		if n := len(k.Name) + 3 + len(k.Default); n > w {
			w = n
		}
	}
	for _, k := range MemoryKeys {
		line := k.Name + " = " + k.Default
		fmt.Fprintf(&b, "# %-*s  # %s: %s (env %s%s)\n", w, line, k.Type, k.Desc, MemoryEnvPrefix, strings.ToUpper(k.Name))
	}
	return b.String()
}

// EnvVar is the environment variable that overrides the key.
func (k MemoryKey) EnvVar() string { return MemoryEnvPrefix + strings.ToUpper(k.Name) }
