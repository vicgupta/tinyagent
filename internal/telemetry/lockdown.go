package telemetry

// After-taint command policy attributes, from run.started's
// lockdown.commands. The allowlist is exported as a count, not the host
// patterns.
const (
	attrLockdownCommands              = "tinyagent.lockdown.commands"
	attrLockdownCommandNetwork        = "tinyagent.lockdown.command_network"
	attrLockdownCommandAllowedDomains = "tinyagent.lockdown.command_allowed_domains"
)

// lockdownEvent records the untrusted-content lockdown state (#171) as
// attributes: run.started's lockdown config enables it on the root span,
// and a taint event marks the root span and every tool span started after
// it as tainted. Only the state is exported, never content. The events
// themselves are still recorded as span events.
func (e *Exporter) lockdownEvent(kind string, fields map[string]any) {
	switch kind {
	case "run.started":
		if info, ok := fields["lockdown"].(map[string]any); ok {
			e.root.set(attrLockdownEnabled, true)
			e.lockdownCommands(info["commands"])
			if tainted, _ := info["tainted"].(bool); tainted {
				e.tainted = true
				e.root.set(attrLockdownTainted, true)
			}
		}
	case "taint":
		e.tainted = true
		e.root.set(attrLockdownTainted, true)
	}
}

// lockdownCommands sets the after-taint command policy attributes on the
// root span: the policy (refused or sandboxed), the sandboxed commands'
// network (denied or proxy) and, behind the proxy, how many
// untrusted_content.lockdown.allowed_domains patterns it is narrowed to.
func (e *Exporter) lockdownCommands(v any) {
	c, ok := v.(map[string]any)
	if !ok {
		return
	}
	if p, ok := c["policy"].(string); ok && p != "" {
		e.root.set(attrLockdownCommands, p)
	}
	if n, ok := c["network"].(string); ok && n != "" {
		e.root.set(attrLockdownCommandNetwork, n)
	}
	switch d := c["allowed_domains"].(type) {
	case []string:
		e.root.set(attrLockdownCommandAllowedDomains, len(d))
	case []any:
		e.root.set(attrLockdownCommandAllowedDomains, len(d))
	}
}
