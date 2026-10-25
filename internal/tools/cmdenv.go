package tools

import (
	"fmt"
	"path"
	"runtime"
	"sort"
	"strings"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/eventlog"
)

// CoreEnv is the allowlist command_env inherit: core keeps. LC_* is a glob.
var CoreEnv = []string{"PATH", "HOME", "USER", "LOGNAME", "SHELL", "LANG", "LC_*", "TERM", "TZ", "TMPDIR"}

// coreEnvWindows is added to CoreEnv on Windows: what MCP servers and hooks
// get, plus what bash.exe and cmd.exe need to find programs.
var coreEnvWindows = []string{"SystemRoot", "WINDIR", "USERPROFILE", "TEMP", "TMP", "PATHEXT", "COMSPEC"}

// ProviderEnv are tinyagent's own provider credentials, always withheld by
// exclude_secrets.
var ProviderEnv = []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "OPENROUTER_API_KEY"}

// CommandEnv is the environment computed once per run from a command_env
// policy. Only names are meant to be shown; Vars holds the values.
type CommandEnv struct {
	Inherit        string // all, core or none
	ExcludeSecrets bool
	Vars           []string // KEY=VALUE entries given to every command
	Passed         []string // sorted names in Vars
	Withheld       []string // sorted names in tinyagent's environment not in Vars
	Missing        []string // pass entries that matched nothing
}

// NewCommandEnv applies p to environ (typically os.Environ()): the inherit
// base, then exclude_secrets, exclude, pass and set, in that order.
func NewCommandEnv(p config.CommandEnv, environ []string) *CommandEnv {
	return newCommandEnv(p, environ, runtime.GOOS == "windows")
}

func newCommandEnv(p config.CommandEnv, environ []string, windows bool) *CommandEnv {
	fold := func(s string) string { return s }
	if windows {
		fold = strings.ToUpper
	}
	matchAny := func(globs []string, name string) bool {
		for _, g := range globs {
			if ok, _ := path.Match(fold(g), fold(name)); ok {
				return true
			}
		}
		return false
	}
	inherit := p.Inherit
	if inherit == "" {
		inherit = "all"
	}
	core := CoreEnv
	if windows {
		core = append(append([]string(nil), CoreEnv...), coreEnvWindows...)
	}

	// Parent variables by folded name; the last duplicate wins, as in exec.
	type kv struct{ name, val string }
	var order []string
	parent := map[string]kv{}
	for _, e := range environ {
		k, v, ok := strings.Cut(e, "=")
		if !ok || k == "" {
			continue
		}
		if _, dup := parent[fold(k)]; !dup {
			order = append(order, fold(k))
		}
		parent[fold(k)] = kv{k, v}
	}
	keep := map[string]bool{}
	for _, f := range order {
		name := parent[f].name
		switch {
		case inherit == "none":
		case inherit == "core" && !matchAny(core, name):
		case p.ExcludeSecrets && (eventlog.SecretEnvName(name) || matchAny(ProviderEnv, name)):
		case matchAny(p.Exclude, name):
		default:
			keep[f] = true
		}
	}
	c := &CommandEnv{Inherit: inherit, ExcludeSecrets: p.ExcludeSecrets, Vars: []string{}}
	for _, g := range p.Pass {
		found := false
		for _, f := range order {
			if ok, _ := path.Match(fold(g), f); ok {
				keep[f], found = true, true
			}
		}
		if !found {
			c.Missing = append(c.Missing, g)
		}
	}
	set := map[string]bool{}
	for k := range p.Set {
		set[fold(k)] = true
	}
	for _, f := range order {
		if keep[f] && !set[f] {
			c.Vars = append(c.Vars, parent[f].name+"="+parent[f].val)
		}
	}
	setNames := make([]string, 0, len(p.Set))
	for k := range p.Set {
		setNames = append(setNames, k)
	}
	sort.Strings(setNames)
	for _, k := range setNames {
		c.Vars = append(c.Vars, k+"="+p.Set[k])
	}
	for _, e := range c.Vars {
		k, _, _ := strings.Cut(e, "=")
		c.Passed = append(c.Passed, k)
	}
	for _, f := range order {
		if !keep[f] && !set[f] {
			c.Withheld = append(c.Withheld, parent[f].name)
		}
	}
	sort.Strings(c.Passed)
	sort.Strings(c.Withheld)
	sort.Strings(c.Missing)
	return c
}

// Describe is the one-line summary printed at startup. It names no values.
func (c *CommandEnv) Describe() string {
	s := "command env: " + c.Inherit
	if c.ExcludeSecrets {
		s += ", secrets excluded"
	}
	return fmt.Sprintf("%s; %d passed, %d withheld", s, len(c.Passed), len(c.Withheld))
}
