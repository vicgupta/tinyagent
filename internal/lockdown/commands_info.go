package lockdown

// Command policy values reported in run.started and OTel attributes.
const (
	// CommandsRefused: commands are refused after taint.
	CommandsRefused = "refused"
	// CommandsSandboxed: commands keep running after taint, confined by the
	// sandbox.
	CommandsSandboxed = "sandboxed"

	// CommandNetworkDenied: confined commands have no network.
	CommandNetworkDenied = "denied"
	// CommandNetworkProxy: confined commands reach the network only through
	// the allowlist proxy, narrowed at the first taint.
	CommandNetworkProxy = "proxy"
)

// CommandsInfo returns the after-taint command policy for run.started:
// policy (refused or sandboxed), and for sandboxed commands their network
// (denied or proxy). Proxied commands also list allowed_domains, the
// untrusted_content.lockdown.allowed_domains patterns the proxy is
// narrowed to at taint ([] when none). Host patterns only, never content.
func (s *State) CommandsInfo() map[string]any {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.commands
	if !c.confined {
		return map[string]any{"policy": CommandsRefused}
	}
	if !c.proxied {
		return map[string]any{"policy": CommandsSandboxed, "network": CommandNetworkDenied}
	}
	domains := append([]string{}, c.domains...)
	return map[string]any{"policy": CommandsSandboxed, "network": CommandNetworkProxy, "allowed_domains": domains}
}
