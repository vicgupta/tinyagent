package plugins

import (
	"fmt"
	"path/filepath"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/skills"
)

// SkillsDir is the plugin's skills directory, relative to the plugin root.
const SkillsDir = "skills"

// Loaded is one configured plugin after loading. Err is set when the plugin
// was rejected; Manifest is set otherwise.
type Loaded struct {
	Path     string // the configured path
	Manifest *Manifest
	Err      error
}

// Load loads every configured plugin in order. A rejected plugin is
// reported in its Loaded.Err and doesn't stop the others; two plugins with
// the same manifest name are an error, since names identify plugins.
func Load(cfg []config.Plugin) ([]Loaded, error) {
	out := make([]Loaded, 0, len(cfg))
	byName := map[string]string{}
	for _, p := range cfg {
		m, err := LoadManifest(p.Path)
		l := Loaded{Path: p.Path, Manifest: m, Err: err}
		if err == nil {
			if prev, ok := byName[m.Name]; ok {
				return nil, fmt.Errorf("plugins: %s and %s both have the name %q", prev, p.Path, m.Name)
			}
			byName[m.Name] = p.Path
		}
		out = append(out, l)
	}
	return out, nil
}

// SkillSources returns the skills/ directory of each loaded plugin, in order.
func SkillSources(ls []Loaded) []skills.PluginSource {
	var out []skills.PluginSource
	for _, l := range ls {
		if l.Err != nil {
			continue
		}
		out = append(out, skills.PluginSource{Plugin: l.Manifest.Name, Root: l.Manifest.Root, Dir: filepath.Join(l.Manifest.Root, SkillsDir)})
	}
	return out
}
