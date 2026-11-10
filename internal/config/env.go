package config

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// launchEnv lists the variables (exact names, or prefixes ending in *)
// that change which program runs or what it loads: PATH, the dynamic
// loader, shell startup files, interpreter options and Git's helpers. A
// project (cwd) config may set them under env: only with --trust-config,
// like the other UntrustedKeys. Names are compared case-insensitively.
var launchEnv = []string{
	"PATH", "PATHEXT", "LD_*", "DYLD_*", "BASH_ENV", "ENV", "ZDOTDIR", "SHELL", "COMSPEC",
	"NODE_OPTIONS", "NODE_PATH", "PYTHONPATH", "PYTHONSTARTUP", "PYTHONHOME",
	"PERL5OPT", "PERL5LIB", "PERLLIB", "RUBYOPT", "RUBYLIB", "GIT_*", "SSH_ASKPASS",
	"EDITOR", "VISUAL", "PAGER", "BROWSER",
}

// LaunchEnv reports whether name is one of the variables a project config
// may set under env: only with --trust-config (see launchEnv).
func LaunchEnv(name string) bool {
	u := strings.ToUpper(name)
	for _, p := range launchEnv {
		if pre, ok := strings.CutSuffix(p, "*"); ok {
			if strings.HasPrefix(u, pre) {
				return true
			}
		} else if u == p {
			return true
		}
	}
	return false
}

// stripLaunchEnv removes the LaunchEnv names from cfg.Env and returns them
// as "env.NAME" keys, sorted.
func stripLaunchEnv(cfg *AgentConfig) []string {
	var out []string
	for name := range cfg.Env {
		if LaunchEnv(name) {
			out = append(out, "env."+name)
			delete(cfg.Env, name)
		}
	}
	sort.Strings(out)
	if len(cfg.Env) == 0 {
		cfg.Env = nil
	}
	return out
}

// EnvNames returns the names in env, sorted. It never returns a value.
func EnvNames(env map[string]string) []string {
	names := make([]string, 0, len(env))
	for n := range env {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// validateEnv checks the env: names. Errors name the variable, never its
// value.
func validateEnv(env map[string]string) error {
	for _, n := range EnvNames(env) {
		if err := checkEnvName(n, false); err != nil {
			return fmt.Errorf("env name %q: %w", n, err)
		}
		if strings.ContainsRune(env[n], 0) {
			return fmt.Errorf("env %s: value must not contain NUL", n)
		}
	}
	return nil
}

// checkEnvNode rejects an env: that isn't a mapping of names to strings,
// which the lenient YAML decoding would otherwise accept (a number or a
// boolean). The error names the variable, never its value.
func checkEnvNode(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode && n.ShortTag() == "!!null" {
		return nil
	}
	if n.Kind != yaml.MappingNode {
		return errors.New("env must be a mapping of names to strings")
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if v := n.Content[i+1]; v.Kind != yaml.ScalarNode || v.ShortTag() != "!!str" {
			return fmt.Errorf("env %s must be a string; quote it", n.Content[i].Value)
		}
	}
	return nil
}
