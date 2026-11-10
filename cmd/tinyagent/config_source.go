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

// ensureConfigIgnored keeps an untracked ./.tinyagent/tinyagent.yaml out of
// broad staging (see config.EnsureIgnored). A failure is only a warning.
// The rule does not cover a tracked file.
func ensureConfigIgnored(cwd string, stderr io.Writer) {
	if _, err := config.EnsureIgnored(cwd); err != nil {
		fmt.Fprintf(stderr, "warning: could not add %s to %s: %v; keep an untracked copy out of commits yourself (a tracked file is not covered by an ignore rule)\n", config.ProjectFile, config.IgnoreFile, err)
	}
}

// ensureGitExclude adds an untracked tinyagent.yaml pattern to
// .git/info/exclude when cwd is a Git work tree (see config.EnsureGitExclude).
// A failure is only a warning. It does not cover a tracked file, and it
// does not edit the repository .gitignore.
func ensureGitExclude(cwd string, stderr io.Writer) {
	if _, err := config.EnsureGitExclude(cwd); err != nil {
		fmt.Fprintf(stderr, "warning: could not add an untracked tinyagent.yaml pattern to .git/info/exclude: %v; keep an untracked copy out of commits yourself (a tracked file is not covered by an ignore rule)\n", err)
	}
}

// loadConfig locates and loads the config with o's --profile. A project
// config found in the cwd (not given with -c) can't launch executables
// unless --trust-config is set: those settings are dropped with a warning.
// The config's env: entries are then set in the process environment (see
// applyConfigEnv).
func loadConfig(o *options, cwd string, stderr io.Writer) (config.Source, config.AgentConfig, error) {
	src := locateConfig(o, cwd, stderr)
	if src.Kind == config.SourceCwd {
		ensureConfigIgnored(cwd, stderr)
	}
	ensureGitExclude(cwd, stderr)
	cfg, err := config.LoadProfile(src.Path, o.profile)
	if err != nil {
		return src, cfg, err
	}
	if src.Kind == config.SourceCwd && !o.trustConfig {
		if keys := config.StripUntrusted(&cfg); len(keys) > 0 {
			fmt.Fprintf(stderr, "warning: ignoring %s from %s: a project config can't launch executables or loosen security settings; pass --trust-config to allow them\n", strings.Join(keys, ", "), src.Path)
		}
	}
	// env: goes into the process environment before anything reads it
	// (#410): providers, key checks, MCP servers, commands, child runs.
	if r := applyConfigEnv(cfg.Env); o.verbose {
		printConfigEnv(stderr, r)
	}
	return src, cfg, nil
}
