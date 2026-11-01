package main

import (
	"context"
	"fmt"
	"io"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/lockdown"
	"github.com/vicgupta/tinyagent/internal/mcptools"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/plugins"
	"github.com/vicgupta/tinyagent/internal/skills"
	"github.com/vicgupta/tinyagent/internal/tools"
)

// checkPlugins loads the configured plugins before a run starts, writing a
// warning for each rejected plugin or manifest problem and, with -v, one line
// per loaded plugin. Only errors that make the plugin list unusable (such as
// duplicate names) are returned, as config.InvalidError (exit 2).
//
// Authorized plugin MCP servers are checked against the configured ones
// (alias collisions, the server cap) and, with startMCP, appended to
// cfg.MCPServers so every existing MCP rule applies to them. Their
// PLUGIN_DATA directories (./.tinyagent/plugin-data/<name> in cwd) are
// created unless the run is read-only. Plugin data left in the old user
// config location gets a single hint line; it is never used.
func checkPlugins(cfg *config.AgentConfig, cwd string, w io.Writer, verbose, startMCP bool) error {
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
	hinted := false
	for i, l := range ls {
		if len(cfg.Plugins[i].MCPServers) > 0 && !hinted {
			hinted = true
			if h := plugins.LegacyDataHint(); h != "" {
				fmt.Fprintln(w, h)
			}
		}
		servers, err := plugins.Servers(l, cfg.Plugins[i], cwd, startMCP && !cfg.ReadOnly)
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

// wasmPool holds the process's plugin WebAssembly modules. Each activation
// (a run, or an ACP session) reloads them: an unchanged module is reused
// and a changed file is swapped in under its pinned permissions.
var wasmPool plugins.WasmPool

// startWasmTools loads the authorized WebAssembly code modules of the
// configured plugins through the Extism sandbox, with a grant derived from
// the session: no host functions, paths only inside root and allowed by
// file_access, hosts only from sandbox_allowed_domains. A refused module is
// reported on w and the others still load. Read-only runs load none.
func startWasmTools(ctx context.Context, cfg config.AgentConfig, root string, fa *tools.FileAccess, lock *lockdown.State, w io.Writer) *plugins.WasmTools {
	any := false
	for _, p := range cfg.Plugins {
		any = any || len(p.WasmModules) > 0
	}
	if !any {
		return nil
	}
	if cfg.ReadOnly {
		fmt.Fprintln(w, "read-only mode: plugin wasm modules are not loaded")
		return nil
	}
	ls, err := plugins.Load(cfg.Plugins)
	if err != nil {
		return nil
	}
	g := plugins.SessionGrant{
		Root:  root,
		Hosts: cfg.SandboxAllowedDomains,
		CheckPath: func(abs string, write bool) error {
			if write {
				return fa.CheckWrite(abs, abs)
			}
			return fa.CheckRead(abs, abs)
		},
	}
	if lock != nil {
		g.Taint = lock
	}
	wasmPool.Store = moduleStore(root)
	wasmPool.Compile = goModuleCompiler(cfg.TinyGo, w)
	return wasmPool.Activate(ctx, ls, cfg.Plugins, g, w)
}

// externalTools merges the MCP catalog with the plugin WebAssembly tools.
func externalTools(mcp *mcptools.Registry, wt *plugins.WasmTools) ([]model.ToolSpec, func(context.Context, string, string) (string, bool, bool), error) {
	if wt == nil || len(wt.Specs) == 0 {
		return mcp.Specs, mcp.Call, nil
	}
	specs := append([]model.ToolSpec{}, mcp.Specs...)
	seen := map[string]bool{}
	for _, s := range specs {
		seen[s.Function.Name] = true
	}
	for _, s := range wt.Specs {
		if seen[s.Function.Name] {
			return nil, nil, fmt.Errorf("plugin wasm tool %q collides with an MCP tool", s.Function.Name)
		}
		specs = append(specs, s)
	}
	call := func(ctx context.Context, name, raw string) (string, bool, bool) {
		if out, isErr, ok := wt.Call(ctx, name, raw); ok {
			return out, isErr, ok
		}
		return mcp.Call(ctx, name, raw)
	}
	return specs, call, nil
}
