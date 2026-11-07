package mcptools

import (
	"strings"
	"testing"
)

func TestActionableMCPWording(t *testing.T) {
	cases := []struct {
		got  string
		want []string
	}{
		{
			actionable("MCP server docs connection is unavailable after an earlier failure", "restart the run or fix that server's command/config, then retry"),
			[]string{"error: MCP server docs connection is unavailable", "restart the run"},
		},
		{
			actionable("invalid arguments JSON object", "resend a single JSON object matching the tool schema"),
			[]string{"error: invalid arguments JSON object", "resend a single JSON object"},
		},
		{
			actionablef("retry with simpler arguments, or raise the server's call_timeout_seconds", "MCP tool %s timed out after %s", "docs__search", "1s"),
			[]string{"error: MCP tool docs__search timed out after 1s", "call_timeout_seconds"},
		},
		{
			actionable("MCP tool requested unsupported interactive input", "use a different tool or non-interactive arguments; tinyagent cannot answer MCP prompts"),
			[]string{"unsupported interactive input", "cannot answer MCP prompts"},
		},
		{
			actionable("resource not allowed for server docs: secret://x", "pick a URI from list_mcp_resources or ask to update the server's resources allowlist"),
			[]string{"resource not allowed", "list_mcp_resources"},
		},
		{
			actionable("server and uri are required", "pass both server and uri from list_mcp_resources"),
			[]string{"server and uri are required", "list_mcp_resources"},
		},
	}
	for i, c := range cases {
		if !strings.Contains(c.got, ";") {
			t.Errorf("case %d: %q missing cause; next-step separator", i, c.got)
		}
		for _, w := range c.want {
			if !strings.Contains(c.got, w) {
				t.Errorf("case %d: %q, want substring %q", i, c.got, w)
			}
		}
	}
}
