package agent

import (
	"encoding/json"
	"strings"

	"github.com/vicgupta/tinyagent/internal/mcptools"
)

// mcpServersOf returns the aliases of the configured MCP servers a call to
// the MCP tool name may reach, for the per-server lockdown settings. A
// catalogued tool is alias__tool; the resource tools name the server in
// their arguments, and list_mcp_resources without one lists every server.
// A name that more than one alias could prefix returns all of them, and a
// call it can't attribute returns nil: lockdown treats both the strict way.
func (a *Agent) mcpServersOf(name, rawArgs string) []string {
	aliases := a.Lockdown.MCPAliases()
	if name == mcptools.ListResourcesTool || name == mcptools.ReadResourceTool {
		var args struct {
			Server string `json:"server"`
		}
		if strings.TrimSpace(rawArgs) != "" && json.Unmarshal([]byte(rawArgs), &args) != nil {
			return nil
		}
		if args.Server == "" {
			if name == mcptools.ReadResourceTool {
				return nil
			}
			return aliases
		}
		return []string{args.Server}
	}
	var out []string
	for _, alias := range aliases {
		if strings.HasPrefix(name, alias+"__") {
			out = append(out, alias)
		}
	}
	return out
}
