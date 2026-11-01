package main

import (
	"fmt"
	"io"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/plugins"
)

// checkPluginsLine is --check's one-line plugin summary: plugin count,
// rejected count and authorized server count. It returns the error a run
// would stop on (exit 2), writing plugin warnings to w as a run does, and
// "" when no plugins are configured. Nothing is created or started.
func checkPluginsLine(cfg config.AgentConfig, cwd string, w io.Writer) (string, error) {
	if len(cfg.Plugins) == 0 {
		return "", nil
	}
	if err := checkPlugins(&cfg, cwd, w, false, false); err != nil {
		return "", err
	}
	ls, err := plugins.Load(cfg.Plugins)
	if err != nil {
		return "", &config.InvalidError{Err: err}
	}
	rejected, servers := 0, 0
	for i, l := range ls {
		if l.Err != nil {
			rejected++
			continue
		}
		servers += len(cfg.Plugins[i].MCPServers)
	}
	return fmt.Sprintf("%d configured, %d rejected, %d authorized MCP servers", len(ls), rejected, servers), nil
}
