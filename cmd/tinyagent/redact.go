package main

import (
	"fmt"
	"io"
	"os"
	"path"
	"runtime"
	"sort"
	"strings"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/eventlog"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/tools"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// namedSecret is one secret value and the variable it came from.
type namedSecret struct{ name, value string }

// configSecrets returns the values the config hands to MCP servers, hooks,
// configured providers and commands, named by their variables.
func configSecrets(cfg config.AgentConfig) []namedSecret {
	var out []namedSecret
	for _, s := range cfg.MCPServers {
		set := s.SetEnv
		if s.Dir != "" {
			set = nil // plugin servers: mcp.json env and PLUGIN_* are package data
		}
		for _, kv := range append(append([]string(nil), s.Env...), set...) {
			if k, v, ok := strings.Cut(kv, "="); ok {
				out = append(out, namedSecret{k, v})
			}
		}
	}
	// Configured providers read their key and secret headers from the
	// environment.
	for _, p := range cfg.Providers {
		envs := []string{p.APIKeyEnv}
		for _, env := range p.EnvHeaders {
			envs = append(envs, env)
		}
		for _, env := range envs {
			if v := os.Getenv(env); env != "" && v != "" {
				out = append(out, namedSecret{env, v})
			}
		}
	}
	// Hook env entries name variables forwarded from tinyagent's own
	// environment, or set them; use the values they had at startup.
	var hookDefs []config.Hook
	hookDefs = append(hookDefs, cfg.Hooks.PreTool...)
	hookDefs = append(hookDefs, cfg.Hooks.PostTool...)
	hookDefs = append(hookDefs, cfg.Hooks.Stop...)
	for _, h := range hookDefs {
		for _, name := range h.Env {
			if k, v, ok := strings.Cut(name, "="); ok {
				out = append(out, namedSecret{k, v})
			} else if v := os.Getenv(name); v != "" {
				out = append(out, namedSecret{name, v})
			}
		}
	}
	// command_env.set values may be secrets the user deliberately provides.
	for k, v := range cfg.CommandEnv.Set {
		out = append(out, namedSecret{k, v})
	}
	return out
}

// envMatch reports whether name matches one of the globs (case-insensitively
// on Windows, as command_env does).
func envMatch(globs []string, name string) bool {
	fold := func(s string) string { return s }
	if runtime.GOOS == "windows" {
		fold = strings.ToUpper
	}
	for _, g := range globs {
		if ok, _ := path.Match(fold(g), fold(name)); ok {
			return true
		}
	}
	return false
}

// newSecretRedactor builds the redact_secrets redactor from environ
// (typically os.Environ()) and the config, and reports it on w by variable
// name and count only. It returns nil, nil when redact_secrets is off.
func newSecretRedactor(cfg config.AgentConfig, environ []string, w io.Writer, verbose bool) (*eventlog.Redactor, *trajectory.RedactSecretsInfo) {
	rs := cfg.RedactSecrets
	model.OnCredentials = nil
	if !rs.Enabled {
		return nil, nil
	}
	var secrets []namedSecret
	matched := map[string]bool{}
	for _, kv := range environ {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || v == "" {
			continue
		}
		extra := envMatch(rs.Env, k)
		if extra {
			for _, g := range rs.Env {
				if envMatch([]string{g}, k) {
					matched[g] = true
				}
			}
		}
		if extra || eventlog.SecretEnvName(k) || envMatch(tools.ProviderEnv, k) {
			secrets = append(secrets, namedSecret{k, v})
		}
	}
	secrets = append(secrets, configSecrets(cfg)...)
	var vals []string
	names, skipped := map[string]bool{}, map[string]bool{}
	for _, s := range secrets {
		switch {
		case envMatch(rs.Allow, s.name):
		case len(s.value) < eventlog.MinSecretLen:
			skipped[s.name] = true
		default:
			names[s.name] = true
			vals = append(vals, s.value)
		}
	}
	r := eventlog.NewValueRedactor(vals, rs.Patterns)
	// Credentials loaded later, such as the codex provider's tokens, are
	// masked from then on.
	model.OnCredentials = r.Add
	info := &trajectory.RedactSecretsInfo{Enabled: true, Patterns: rs.Patterns, Names: sortedKeys(names)}
	line := fmt.Sprintf("redact_secrets: %d values from %d variables", r.Len(), len(names))
	if rs.Patterns {
		line += ", token patterns on"
	}
	fmt.Fprintln(w, line)
	if verbose {
		if len(info.Names) > 0 {
			fmt.Fprintf(w, "redact_secrets: masking %s\n", strings.Join(info.Names, ", "))
		}
		for _, n := range sortedKeys(skipped) {
			if !names[n] {
				fmt.Fprintf(w, "redact_secrets: skipped %s (value shorter than %d characters)\n", n, eventlog.MinSecretLen)
			}
		}
		for _, g := range rs.Env {
			if !matched[g] {
				fmt.Fprintf(w, "redact_secrets: env %s matched nothing\n", g)
			}
		}
	}
	return r, info
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
