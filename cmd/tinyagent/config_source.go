package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/vicgupta/tinyagent/internal/config"
)

// locateConfig picks the config file for o (see config.Locate) and prints
// its notes, such as the user-config deprecation warning, to stderr.
func locateConfig(o *options, cwd string, stderr io.Writer) config.Source {
	src := config.Locate(o.configPath, cwd)
	for _, n := range src.Notes {
		fmt.Fprintln(stderr, n)
	}
	return src
}

// loadConfig locates and loads the config with o's --profile. A project
// config found in the cwd (not given with -c) can't launch executables
// unless --trust-config is set: those settings are dropped with a warning.
func loadConfig(o *options, cwd string, stderr io.Writer) (config.Source, config.AgentConfig, error) {
	src := locateConfig(o, cwd, stderr)
	cfg, err := config.LoadProfile(src.Path, o.profile)
	if err != nil {
		return src, cfg, err
	}
	if src.Kind == config.SourceCwd && !o.trustConfig {
		if keys := config.StripUntrusted(&cfg); len(keys) > 0 {
			fmt.Fprintf(stderr, "warning: ignoring %s from %s: a project config can't launch executables or loosen security settings; pass --trust-config to allow them\n", strings.Join(keys, ", "), src.Path)
		}
	}
	return src, cfg, nil
}
