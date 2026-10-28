package lockdown

import (
	"sort"
	"strings"
)

// MCPServerPolicy is one configured MCP server's lockdown settings
// (mcp_servers[].untrusted and mcp_servers[].lockdown_allowed).
type MCPServerPolicy struct {
	Alias string
	// Trusted is untrusted: false: the server's results don't taint the
	// run. The default is false, so results taint.
	Trusted bool
	// LockdownAllowed is lockdown_allowed: true: the server's tools stay
	// callable after taint. The default is false, so they are refused.
	LockdownAllowed bool
}

// SetMCPServers records the configured MCP servers' lockdown settings. Call
// it before the run starts. A server that isn't listed, including one an
// ACP client supplies, keeps the defaults: its results taint and its tools
// are refused after taint.
func (s *State) SetMCPServers(servers []MCPServerPolicy) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mcpServers = make(map[string]MCPServerPolicy, len(servers))
	for _, p := range servers {
		s.mcpServers[p.Alias] = p
	}
}

// MCPAliases returns the aliases of the servers SetMCPServers and
// SetACPClientMCPServers recorded, in sorted order.
func (s *State) MCPAliases() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.mcpServers)+len(s.clientMCP))
	for alias := range s.mcpServers {
		out = append(out, alias)
	}
	for alias := range s.clientMCP {
		if _, ok := s.mcpServers[alias]; !ok {
			out = append(out, alias)
		}
	}
	sort.Strings(out)
	return out
}

// MCPCallTaints reports whether a result from a call that may have reached
// any of aliases taints the run. It fails closed: an empty list or an
// unknown alias taints, and so does any server not marked untrusted: false.
func (s *State) MCPCallTaints(aliases []string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(aliases) == 0 {
		return true
	}
	for _, a := range aliases {
		if p, ok := s.mcpServers[a]; !ok || !p.Trusted {
			return true
		}
	}
	return false
}

// MCPCallAllowed reports whether a call that may reach any of aliases is
// still allowed after taint. It fails closed: an empty list or an unknown
// alias is refused, and every server must be lockdown_allowed: true.
func (s *State) MCPCallAllowed(aliases []string) bool {
	if s == nil {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(aliases) == 0 {
		return false
	}
	for _, a := range aliases {
		if p, ok := s.mcpServers[a]; !ok || !p.LockdownAllowed {
			return false
		}
	}
	return true
}

// describeMCP is the MCP part of the startup line: which servers stay
// callable after taint, and which don't taint.
func (s *State) describeMCP() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var allowed, trusted []string
	for alias, p := range s.mcpServers {
		if p.LockdownAllowed {
			allowed = append(allowed, alias)
		}
		if p.Trusted {
			trusted = append(trusted, alias)
		}
	}
	sort.Strings(allowed)
	sort.Strings(trusted)
	out := "mcp tools refused"
	if len(allowed) > 0 {
		out = "mcp → " + strings.Join(allowed, ", ")
	}
	if len(trusted) > 0 {
		out += "; mcp results that don't taint: " + strings.Join(trusted, ", ")
	}
	return out
}
