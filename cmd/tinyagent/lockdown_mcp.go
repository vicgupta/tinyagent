package main

import (
	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/lockdown"
)

// mcpLockdownPolicies maps the configured mcp_servers to their lockdown
// settings: untrusted: false (results don't taint) and lockdown_allowed:
// true (tools stay callable after taint).
func mcpLockdownPolicies(servers []config.MCPServer) []lockdown.MCPServerPolicy {
	out := make([]lockdown.MCPServerPolicy, 0, len(servers))
	for _, s := range servers {
		out = append(out, lockdown.MCPServerPolicy{
			Alias:           s.Alias,
			Trusted:         s.Untrusted != nil && !*s.Untrusted,
			LockdownAllowed: s.LockdownAllowed,
		})
	}
	return out
}
