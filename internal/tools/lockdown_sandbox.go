package tools

import (
	"fmt"

	"github.com/vicgupta/tinyagent/internal/sandboxnet"
)

// CommandSandbox returns the sandbox that confines env's commands, or nil
// when they run unsandboxed or in a container.
func CommandSandbox(env Environment) *Sandbox {
	if l, ok := env.(Local); ok {
		return l.Sandbox
	}
	return nil
}

// LockdownConfinable reports whether the untrusted-content lockdown (issue
// #171) can keep commands running after taint: their network is denied, or
// limited by the allowlist proxy, which Narrow can restrict further.
// sandbox_network gives commands direct network that can't be narrowed.
func (s *Sandbox) LockdownConfinable() bool {
	return s != nil && !s.Network
}

// Proxied reports whether commands reach the network through the
// allowlist proxy.
func (s *Sandbox) Proxied() bool {
	return s != nil && s.proxy != nil
}

// NarrowNetwork restricts the allowlist proxy, for the rest of the run, to
// hosts in both sandbox_allowed_domains and allowed (the
// untrusted_content.lockdown.allowed_domains entries), and closes the
// connections opened before. Background processes started earlier go
// through the same proxy, so they are narrowed too. With network denied
// there is nothing to narrow. It fails for a sandbox lockdown can't
// confine.
func (s *Sandbox) NarrowNetwork(allowed []string) error {
	if !s.LockdownConfinable() {
		return fmt.Errorf("sandbox: commands have direct network (sandbox_network), which lockdown can't narrow")
	}
	rules, err := sandboxnet.ParseRules(allowed)
	if err != nil {
		return fmt.Errorf("untrusted_content.lockdown.allowed_domains %v", err)
	}
	if s.proxy != nil {
		s.proxy.Narrow(rules)
	}
	return nil
}
