package telemetry

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
