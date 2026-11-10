package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/vicgupta/tinyagent/internal/config"
)

// configEnv remembers the variables this process set from a config's env:
// and the value it set, so a later load (an ACP session in another
// project, a second loadConfig in the same run) can tell them from
// variables the user exported. Only names and our own values are kept;
// nothing here is ever printed.
var configEnv = struct {
	sync.Mutex
	set map[string]string
}{set: map[string]string{}}

// envResult says which env: names were applied from the config and which
// were left to the real environment. It holds names only.
type envResult struct {
	fromConfig []string // set from the config
	kept       []string // already set in the environment, which wins
}

// applyConfigEnv sets cfg's env: entries in the process environment
// (#410). A variable that is already set to a nonempty value, other than
// by an earlier config load, wins. Variables an earlier load set that this
// config no longer names are unset again, so they don't carry over to
// another project's session. Values are never logged.
func applyConfigEnv(env map[string]string) envResult {
	configEnv.Lock()
	defer configEnv.Unlock()
	var r envResult
	for name, ours := range configEnv.set {
		if _, still := env[name]; still {
			continue
		}
		if cur, ok := os.LookupEnv(name); ok && cur == ours {
			os.Unsetenv(name)
		}
		delete(configEnv.set, name)
	}
	for _, name := range config.EnvNames(env) {
		cur := os.Getenv(name)
		if ours, mine := configEnv.set[name]; cur != "" && (!mine || cur != ours) {
			delete(configEnv.set, name)
			r.kept = append(r.kept, name)
			continue
		}
		if err := os.Setenv(name, env[name]); err != nil {
			r.kept = append(r.kept, name)
			continue
		}
		configEnv.set[name] = env[name]
		r.fromConfig = append(r.fromConfig, name)
	}
	return r
}

// configEnvSource reports where name's value comes from for --check: "from
// config" or "from environment". It never returns the value.
func configEnvSource(name string) string {
	configEnv.Lock()
	defer configEnv.Unlock()
	if ours, ok := configEnv.set[name]; ok && os.Getenv(name) == ours {
		return "from config"
	}
	return "from environment"
}

// describeConfigEnv is the --check "env:" value: each env: name and where
// its value comes from, or "" when the config sets none. Names only.
func describeConfigEnv(env map[string]string) string {
	var parts []string
	for _, n := range config.EnvNames(env) {
		parts = append(parts, fmt.Sprintf("%s (%s)", n, configEnvSource(n)))
	}
	return strings.Join(parts, ", ")
}

// printConfigEnv writes the -v line for r, names only.
func printConfigEnv(w io.Writer, r envResult) {
	if len(r.fromConfig) == 0 && len(r.kept) == 0 {
		return
	}
	msg := "env:"
	if len(r.fromConfig) > 0 {
		msg += " set from config: " + strings.Join(r.fromConfig, ", ")
		if len(r.kept) > 0 {
			msg += ";"
		}
	}
	if len(r.kept) > 0 {
		msg += " already set in the environment (kept): " + strings.Join(r.kept, ", ")
	}
	fmt.Fprintln(w, msg)
}
