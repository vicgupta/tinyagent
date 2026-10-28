package main

import (
	"fmt"
	"io"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/plugins"
	"github.com/vicgupta/tinyagent/internal/skills"
)

// checkPlugins loads the configured plugins before a run starts, writing a
// warning for each rejected plugin or manifest problem and, with -v, one line
// per loaded plugin. Only errors that make the plugin list unusable (such as
// duplicate names) are returned, as config.InvalidError (exit 2).
func checkPlugins(cfg config.AgentConfig, w io.Writer, verbose bool) error {
	if len(cfg.Plugins) == 0 {
		return nil
	}
	ls, err := plugins.Load(cfg.Plugins)
	if err != nil {
		return &config.InvalidError{Err: err}
	}
	for _, l := range ls {
		if l.Err != nil {
			fmt.Fprintf(w, "warning: plugin %s rejected: %v\n", l.Path, l.Err)
			continue
		}
		for _, msg := range l.Manifest.Warnings {
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
