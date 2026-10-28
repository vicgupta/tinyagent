package lockdown

import "sort"

// SetACPClientMCPServers marks the run as an ACP session and records the
// aliases of the MCP servers its client supplied. Results from those
// servers taint as acp_client_mcp rather than mcp, so
// untrusted_content.sources can choose them separately. Client servers have
// no mcp_servers entry, so they keep the strict defaults: their results
// always taint (when acp_client_mcp is a source) and their tools are
// refused after taint. Call it before the session's first turn.
func (s *State) SetACPClientMCPServers(aliases []string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.acp = true
	s.clientMCP = make(map[string]bool, len(aliases))
	for _, a := range aliases {
		s.clientMCP[a] = true
	}
}

// ACPClientMCPAliases returns the client-supplied server aliases, sorted.
func (s *State) ACPClientMCPAliases() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.clientMCP))
	for a := range s.clientMCP {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

// MCPTaintSource returns the taint source of a result from a call that may
// have reached any of aliases, and whether it taints the run. A
// client-supplied server taints as acp_client_mcp, any other server not
// marked untrusted: false as mcp; the first such server whose source is
// enabled in untrusted_content.sources decides. It fails closed: a call
// that can't be tied to a server (empty aliases) taints as mcp, or as
// acp_client_mcp when mcp is turned off and the client supplied servers.
func (s *State) MCPTaintSource(aliases []string) (string, bool) {
	if s == nil {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(aliases) == 0 {
		if !s.taintsFromLocked(SourceMCP) && len(s.clientMCP) > 0 && s.taintsFromLocked(SourceACPClientMCP) {
			return SourceACPClientMCP, true
		}
		return SourceMCP, s.taintsFromLocked(SourceMCP)
	}
	for _, a := range aliases {
		src := SourceMCP
		if s.clientMCP[a] {
			src = SourceACPClientMCP
		} else if p, ok := s.mcpServers[a]; ok && p.Trusted {
			continue
		}
		if s.taintsFromLocked(src) {
			return src, true
		}
	}
	return "", false
}
