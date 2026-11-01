package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/plugins"
	"github.com/vicgupta/tinyagent/internal/skills"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// pluginRecords returns the run.started and trajectory entries for the
// configured plugins that load: name, version, path, the SHA-256 of
// plugin.json and mcp.json, the plugin skills in the catalog (when skills
// are on) and, with withMCP, the authorized servers. Nil with no plugins,
// so runs without a plugins key record nothing.
func pluginRecords(cfg config.AgentConfig, root string, withMCP bool) []trajectory.PluginInfo {
	if len(cfg.Plugins) == 0 {
		return nil
	}
	ls, err := plugins.Load(cfg.Plugins)
	if err != nil {
		return nil
	}
	bySkill := map[string][]string{}
	if cfg.SkillsOn() {
		set, _ := skills.DiscoverWith(root, plugins.SkillSources(ls))
		for _, k := range set.Skills {
			if k.Plugin != "" {
				bySkill[k.Plugin] = append(bySkill[k.Plugin], k.Name)
			}
		}
	}
	var out []trajectory.PluginInfo
	for i, l := range ls {
		if l.Err != nil {
			continue
		}
		info := trajectory.PluginInfo{
			Name:           l.Manifest.Name,
			Version:        l.Manifest.Version,
			Path:           l.Path,
			ManifestSHA256: fileSHA256(filepath.Join(l.Manifest.Root, plugins.ManifestFile)),
			Skills:         bySkill[l.Manifest.Name],
		}
		if l.MCP != nil && l.MCP.Present {
			info.MCPSHA256 = l.MCP.Digest
		}
		if withMCP {
			for _, s := range cfg.Plugins[i].MCPServers {
				info.MCPServers = append(info.MCPServers, trajectory.PluginServerInfo{Server: s.Server, Alias: s.Alias})
			}
		}
		out = append(out, info)
	}
	return out
}

func fileSHA256(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// resumePlugins warns, naming the plugin, when a plugin's plugin.json or
// mcp.json digest differs from the checkpoint's, then records the current
// plugins in the resumed trajectory. A changed skills catalog is refused
// separately, by the resume validation.
func resumePlugins(src *trajectory.Trajectory, cur []trajectory.PluginInfo, warn func(string)) {
	prev := map[string]trajectory.PluginInfo{}
	for _, p := range src.Plugins {
		prev[p.Name] = p
	}
	for _, p := range cur {
		old, ok := prev[p.Name]
		if !ok {
			continue
		}
		if old.ManifestSHA256 != p.ManifestSHA256 {
			warn(fmt.Sprintf("plugin %s: %s changed since the checkpoint", p.Name, plugins.ManifestFile))
		}
		if old.MCPSHA256 != p.MCPSHA256 {
			warn(fmt.Sprintf("plugin %s: %s changed since the checkpoint", p.Name, plugins.MCPFile))
		}
	}
	src.Plugins = cur
}
