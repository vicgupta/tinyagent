package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Plugin is one Agent Plugins 1.0.0 package directory to load. Like
// mcp_servers, plugins come only from the user config, -c or a profile.
type Plugin struct {
	// Path is the absolute path to the plugin root (the directory holding
	// plugin.json).
	Path string `yaml:"path"`
}

// checkPlugins decodes the plugins node strictly, so an unknown key under
// plugins[] exits 2 like other invalid settings.
func checkPlugins(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode && n.ShortTag() == "!!null" {
		return nil
	}
	if n.Kind != yaml.SequenceNode {
		return fmt.Errorf("plugins must be a list of {path: ...} entries")
	}
	for i, item := range n.Content {
		if err := checkStrict(item, fmt.Sprintf("plugins[%d]", i), &Plugin{}); err != nil {
			return err
		}
	}
	return nil
}

// validatePlugins requires each path to be an absolute path to an existing
// directory and rejects duplicates. Manifest checks happen when a run loads
// the plugins (internal/plugins).
func validatePlugins(ps []Plugin) error {
	seen := map[string]int{}
	for i, p := range ps {
		key := fmt.Sprintf("plugins[%d].path", i)
		if p.Path == "" {
			return fmt.Errorf("%s is required", key)
		}
		if !filepath.IsAbs(p.Path) {
			return fmt.Errorf("%s %q must be an absolute path", key, p.Path)
		}
		fi, err := os.Stat(p.Path)
		if err != nil || !fi.IsDir() {
			return fmt.Errorf("%s %q is not an existing directory", key, p.Path)
		}
		clean := filepath.Clean(p.Path)
		if real, err := filepath.EvalSymlinks(clean); err == nil {
			clean = real
		}
		if j, ok := seen[clean]; ok {
			return fmt.Errorf("%s %q duplicates plugins[%d].path", key, p.Path, j)
		}
		seen[clean] = i
	}
	return nil
}
