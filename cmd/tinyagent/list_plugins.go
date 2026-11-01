package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/plugins"
	"github.com/vicgupta/tinyagent/internal/skills"
)

// listPluginsConflicts are the task and mode flags --list-plugins refuses,
// so inspection never starts a run, makes a request or writes a file.
var listPluginsConflicts = []string{
	"task", "t", "check", "review", "review-base", "review-commit",
	"resume", "rewind", "to", "dry-run", "acp", "transcript", "completion",
	"init-config", "update", "worktree", "list-profiles", "workflow", "eval",
	"attempts", "follow-up",
}

// runListPlugins implements --list-plugins: for each configured plugin it
// prints the path, manifest name and version, its state, its skills
// (marking shadowed ones) and each mcp.json server with its state. It
// makes no model request, needs no credentials, starts no servers or hooks
// and writes nothing (PLUGIN_DATA is not created). It exits 0 when every
// plugin loads, 1 when one is rejected or a configured server is invalid,
// and 2 on a usage or config error.
func runListPlugins(o *options, fs *flag.FlagSet, w io.Writer) int {
	for _, name := range listPluginsConflicts {
		if fs.Lookup(name) != nil && flagWasSet(fs, name) {
			dash := "--"
			if len(name) == 1 {
				dash = "-"
			}
			fmt.Fprintf(os.Stderr, "error: --list-plugins only inspects plugins; it can't be combined with %s%s\n", dash, name)
			return 2
		}
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "error: unexpected positional argument %q\n", fs.Arg(0))
		return 2
	}
	_, cfg, err := loadConfig(o, o.cwd, os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	if len(cfg.Plugins) == 0 {
		fmt.Fprintln(w, "no plugins configured")
		return 0
	}
	ls, err := plugins.Load(cfg.Plugins)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	root := o.cwd
	if r, err := canonicalRoot(o.cwd); err == nil {
		root = r
	}
	set, warnings := skills.DiscoverWith(root, plugins.SkillSources(ls))
	code := 0
	for i, l := range ls {
		if l.Err != nil {
			fmt.Fprintf(w, "%s: rejected: %v\n", l.Path, l.Err)
			code = 1
			if _, err := plugins.Servers(l, cfg.Plugins[i], o.cwd, false); err != nil {
				fmt.Fprintf(w, "  error: %v\n", err)
			}
			continue
		}
		m := l.Manifest
		head := l.Path + ": " + m.Name
		if m.Version != "" {
			head += " " + m.Version
		}
		warns := append(append([]string{}, m.Warnings...), l.MCPWarnings(cfg.Plugins[i].MCPServers)...)
		if len(warns) > 0 {
			head += fmt.Sprintf(" warnings: %d", len(warns))
		} else {
			head += " ok"
		}
		fmt.Fprintln(w, head)
		for _, msg := range warns {
			fmt.Fprintf(w, "  warning: %s\n", msg)
		}
		for _, k := range set.Skills {
			if k.Plugin == m.Name {
				fmt.Fprintf(w, "  skill %s\n", k.Name)
			}
		}
		for _, sh := range shadowedPluginSkills(warnings, m.Root) {
			fmt.Fprintf(w, "  skill %s (shadowed by %s)\n", sh[0], sh[1])
		}
		listPluginServers(w, l, cfg.Plugins[i])
		if _, err := plugins.Servers(l, cfg.Plugins[i], o.cwd, false); err != nil {
			fmt.Fprintf(w, "  error: %v\n", err)
			code = 1
		}
	}
	return code
}

// listPluginServers prints each mcp.json server of l with its state.
func listPluginServers(w io.Writer, l plugins.Loaded, cfg config.Plugin) {
	if l.MCP == nil || !l.MCP.Present {
		return
	}
	if l.MCP.Err != nil {
		fmt.Fprintf(w, "  %s: invalid: %v\n", plugins.MCPFile, l.MCP.Err)
		return
	}
	auth := map[string]config.PluginServer{}
	for _, a := range cfg.MCPServers {
		auth[a.Server] = a
	}
	for _, e := range l.MCP.Entries {
		typ := e.Type
		if typ == "" {
			typ = "unknown"
		}
		var state string
		a, listed := auth[e.Name]
		switch {
		case errors.Is(e.Err, plugins.ErrUnsupported):
			state = "unsupported transport"
		case e.Err != nil:
			state = fmt.Sprintf("invalid: %v", e.Err)
		case listed:
			state = fmt.Sprintf("authorized as %s (%d tools)", a.Alias, len(a.Tools))
		default:
			state = "not authorized"
		}
		fmt.Fprintf(w, "  server %s (%s): %s\n", e.Name, typ, state)
	}
}

// shadowedPluginSkills picks the shadowed skills under pluginRoot out of
// the discovery warnings, as name and winning path pairs.
func shadowedPluginSkills(warnings []string, pluginRoot string) [][2]string {
	const pre, at, by = `skills: skill "`, `" at `, ` is shadowed by `
	var out [][2]string
	for _, msg := range warnings {
		rest, ok := strings.CutPrefix(msg, pre)
		if !ok {
			continue
		}
		name, rest, ok := strings.Cut(rest, at)
		if !ok {
			continue
		}
		path, winner, ok := strings.Cut(rest, by)
		if !ok || !strings.HasPrefix(path, pluginRoot+string(filepath.Separator)) {
			continue
		}
		out = append(out, [2]string{name, winner})
	}
	return out
}
