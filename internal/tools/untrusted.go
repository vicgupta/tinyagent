package tools

// Untrusted-content sources a tool result can come from. Project memory
// (#170) and the untrusted-content lockdown (#171) share this one
// definition, so a result that keeps memory writes pending is exactly a
// result that can taint a locked-down run. The names match the
// untrusted_content.sources values lockdown.SourceWebFetch and
// lockdown.SourceMCP.
const (
	UntrustedWebFetch = "web_fetch"
	UntrustedMCP      = "mcp"
)

// UntrustedSource reports which untrusted source a result of the tool
// name brings into context: UntrustedWebFetch for web_fetch when it is
// enabled, UntrustedMCP for an external (MCP) tool, and "" for every
// other tool. It decides by tool, never by content.
func (e *Executor) UntrustedSource(name string) string {
	if e == nil {
		return ""
	}
	if name == WebFetchToolName && e.WebFetch != nil {
		return UntrustedWebFetch
	}
	for _, s := range e.ExternalSpecs {
		if s.Function.Name == name {
			return UntrustedMCP
		}
	}
	return ""
}

// UntrustedHistoryCall reports whether a recorded call to the tool name
// brought untrusted content into a checkpoint's history: a web_fetch call,
// or a call to one of the checkpoint's external tools. It is the
// UntrustedSource rule for a run that only exists as a trajectory.
func UntrustedHistoryCall(name string, externalTools []string) bool {
	if name == WebFetchToolName {
		return true
	}
	for _, n := range externalTools {
		if n == name {
			return true
		}
	}
	return false
}
