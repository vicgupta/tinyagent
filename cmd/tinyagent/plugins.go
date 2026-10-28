package main

import (
	"fmt"
	"io"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/mcptools"
	"github.com/vicgupta/tinyagent/internal/plugins"
	"github.com/vicgupta/tinyagent/internal/skills"
)

// checkPlugins loads the configured plugins before a run starts, writing a
// warning for each rejected plugin or manifest problem and, with -v, one line
// per loaded plugin. Only errors that make the plugin list unusable (such as
// duplicate names) are returned, as config.InvalidError (exit 2).
//
// Authorized plugin MCP servers are checked against the configured ones
// (alias collisions, the server cap) and, with startMCP, appended to
// cfg.MCPServers so every existing MCP rule applies to them. Their
// PLUGIN_DATA directories are created unless the run is read-only.
func checkPlugins(cfg *config.AgentConfig, w io.Writer, verbose, startMCP bool) error {
	if len(cfg.Plugins) == 0 {
		return nil
	}
	ls, err := plugins.Load(cfg.Plugins)
	if err != nil {
		return &config.InvalidError{Err: err}
	}
	aliases := map[string]string{}
	for _, s := range cfg.MCPServers {
		aliases[s.Alias] = fmt.Sprintf("mcp_servers alias %q", s.Alias)
	}
	var defs []config.MCPServer
	for i, l := range ls {
		servers, err := plugins.Servers(l, cfg.Plugins[i], startMCP && !cfg.ReadOnly)
		if err != nil {
			return &config.InvalidError{Err: err}
		}
		for _, d := range servers {
			this := fmt.Sprintf("plugin %s alias %q", l.Manifest.Name, d.Alias)
			if prev, ok := aliases[d.Alias]; ok {
				return &config.InvalidError{Err: fmt.Errorf("%s collides with %s", this, prev)}
			}
			aliases[d.Alias] = this
		}
		defs = append(defs, servers...)
		if l.Err != nil {
			fmt.Fprintf(w, "warning: plugin %s rejected: %v\n", l.Path, l.Err)
			continue
		}
		for _, msg := range append(l.Manifest.Warnings, l.MCPWarnings(cfg.Plugins[i].MCPServers)...) {
			fmt.Fprintf(w, "warning: plugin %s: %s\n", l.Manifest.Name, msg)
		}
		if verbose {
			v := ""
			if l.Manifest.Version != "" {
				v = " " + l.Manifest.Version
			}
			fmt.Fprintf(w, "loaded plugin %s%s from %s\n", l.Manifest.Name, v, l.Manifest.Root)
		}
	}
	if n := len(cfg.MCPServers) + len(defs); len(defs) > 0 && n > mcptools.MaxServers {
		return &config.InvalidError{Err: fmt.Errorf("mcp: %d servers configured (mcp_servers plus plugins); at most %d are allowed", n, mcptools.MaxServers)}
	}
	if startMCP {
		cfg.MCPServers = append(cfg.MCPServers, defs...)
	}
	return nil
}

// pluginSkillSources returns the skills/ directories of the configured
// plugins that load, lowest precedence last. Problems were already reported
// by checkPlugins.
func pluginSkillSources(cfg config.AgentConfig) []skills.PluginSource {
	if len(cfg.Plugins) == 0 {
		return nil
	}
	ls, err := plugins.Load(cfg.Plugins)
	if err != nil {
		return nil
	}
	return plugins.SkillSources(ls)
}
