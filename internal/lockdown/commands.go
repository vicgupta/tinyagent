package lockdown

import (
	"fmt"
	"strings"

	"github.com/vicgupta/tinyagent/internal/sandboxnet"
)

// commandPolicy is what happens to commands (bash, start_process, command
// tools) after taint. Unconfined, they are refused. Confined (the run's
// commands are sandboxed with network denied or behind the allowlist
// proxy), they keep running and the proxy is narrowed to the lockdown
// allowlist at the first taint.
type commandPolicy struct {
	// domains is untrusted_content.lockdown.allowed_domains.
	domains []string
	// confined is set by ConfineCommands.
	confined bool
	// proxied: commands reach the network through the allowlist proxy;
	// otherwise their network is denied.
	proxied bool
	// narrow restricts the proxy; it runs once, at the first taint.
	narrow func()
}

// SetCommandAllowedDomains records untrusted_content.lockdown.allowed_domains,
// the hosts sandboxed commands may still reach through the allowlist proxy
// after taint (sandbox_allowed_domains syntax; a bare * is rejected). Call
// it before the run starts.
func (s *State) SetCommandAllowedDomains(domains []string) error {
	if s == nil {
		return nil
	}
	if _, err := sandboxnet.ParseRules(domains); err != nil {
		return fmt.Errorf("untrusted_content.lockdown.allowed_domains %v", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.commands.domains = append([]string(nil), domains...)
	return nil
}

// CommandDomains returns untrusted_content.lockdown.allowed_domains.
func (s *State) CommandDomains() []string {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.commands.domains...)
}

// ConfineCommands marks the run's commands as confined, so they keep running
// after taint instead of being refused. proxied says they reach the network
// only through the allowlist proxy; narrow, which may be nil when they have
// no network, restricts that proxy to CommandDomains. narrow runs once: at
// the first taint, or right away when the run is already tainted. Call it
// only when every command the run can start (workers included) goes
// through that sandbox, and before the run starts.
func (s *State) ConfineCommands(proxied bool, narrow func([]string)) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.commands.confined = true
	s.commands.proxied = proxied
	s.commands.narrow = nil
	if narrow != nil {
		domains := append([]string(nil), s.commands.domains...)
		s.commands.narrow = func() { narrow(domains) }
	}
	var now func()
	if len(s.sources) > 0 {
		now = s.takeNarrowLocked()
	}
	s.mu.Unlock()
	if now != nil {
		now()
	}
}

// CommandsConfined reports whether commands keep running after taint.
func (s *State) CommandsConfined() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.commands.confined
}

// takeNarrowLocked returns the pending narrow hook, at most once.
func (s *State) takeNarrowLocked() func() {
	f := s.commands.narrow
	s.commands.narrow = nil
	return f
}

// describeCommands is the command part of Describe.
func (s *State) describeCommands() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.commands
	switch {
	case !c.confined:
		return "commands refused"
	case !c.proxied:
		return "commands sandboxed, network denied"
	case len(c.domains) == 1:
		return "commands sandboxed, network → 1 domain (" + c.domains[0] + ")"
	case len(c.domains) > 1:
		return fmt.Sprintf("commands sandboxed, network → %d domains (%s)", len(c.domains), strings.Join(c.domains, ", "))
	}
	return "commands sandboxed, network → 0 domains"
}
