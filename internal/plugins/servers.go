package plugins

import (
	"errors"
	"fmt"
	"os"

	"github.com/vicgupta/tinyagent/internal/config"
)

// MCPWarnings returns the non-fatal mcp.json problems of a loaded plugin:
// MCP disabled by a bad top-level file, and invalid or unsupported entries
// that aren't authorized (authorized ones fail in Servers instead).
func (l Loaded) MCPWarnings(auth []config.PluginServer) []string {
	if l.MCP == nil || !l.MCP.Present {
		return nil
	}
	if l.MCP.Err != nil {
		return []string{fmt.Sprintf("%v; MCP is disabled for this plugin", l.MCP.Err)}
	}
	listed := map[string]bool{}
	for _, a := range auth {
		listed[a.Server] = true
	}
	var out []string
	for _, e := range l.MCP.Entries {
		if e.Err != nil && !listed[e.Name] {
			out = append(out, fmt.Sprintf("%s server %q: %v; skipped", MCPFile, e.Name, e.Err))
		}
	}
	return out
}

// Servers returns the MCP server definitions the config authorizes for
// loaded plugin l (cfg is its config entry). A rejected plugin with
// authorized servers, a listed server that is missing, invalid or
// unsupported, or a disabled mcp.json is an error naming plugin and server.
// PLUGIN_DATA is ./.tinyagent/plugin-data/<name> inside cwd; with create
// set, that directory is created (0700).
func Servers(l Loaded, cfg config.Plugin, cwd string, create bool) ([]config.MCPServer, error) {
	if len(cfg.MCPServers) == 0 {
		return nil, nil
	}
	if l.Err != nil {
		return nil, fmt.Errorf("plugin %s has mcp_servers but was rejected: %w", l.Path, l.Err)
	}
	name := l.Manifest.Name
	switch {
	case l.MCP == nil || !l.MCP.Present:
		return nil, fmt.Errorf("plugin %s: mcp_servers are configured but the plugin has no %s", name, MCPFile)
	case l.MCP.Err != nil:
		return nil, fmt.Errorf("plugin %s: %v", name, l.MCP.Err)
	}
	data, err := DataDir(cwd, name)
	if err != nil {
		return nil, fmt.Errorf("plugin %s: PLUGIN_DATA: %w", name, err)
	}
	var out []config.MCPServer
	for _, auth := range cfg.MCPServers {
		e, ok := l.MCP.Entry(auth.Server)
		if !ok {
			return nil, fmt.Errorf("plugin %s: server %q is not in %s", name, auth.Server, MCPFile)
		}
		def, err := Server(e, auth, l.Manifest.Root, data)
		if errors.Is(err, ErrUnsupported) {
			return nil, fmt.Errorf("plugin %s: server %q uses an unsupported transport (%s)", name, auth.Server, e.Type)
		} else if err != nil {
			return nil, fmt.Errorf("plugin %s: server %q: %v", name, auth.Server, err)
		}
		out = append(out, def)
	}
	if create {
		if err := os.MkdirAll(data, 0o700); err != nil {
			return nil, fmt.Errorf("plugin %s: PLUGIN_DATA: %w", name, err)
		}
	}
	return out, nil
}
