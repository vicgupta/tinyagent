package trajectory

// TaintSource is one recorded taint of the untrusted-content lockdown
// (#171): what brought untrusted content into context and where. It holds
// source and tool names, ids, steps and hosts, never content.
type TaintSource struct {
	Source     string `json:"source"`
	Step       int    `json:"step"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	Tool       string `json:"tool,omitempty"`
	Host       string `json:"host,omitempty"`
	// Resumed marks a source carried over from an earlier invocation.
	Resumed bool `json:"resumed,omitempty"`
}

// LockdownDenial is one tool call the lockdown refused after taint.
type LockdownDenial struct {
	Channel    string `json:"channel"`
	Tool       string `json:"tool"`
	ToolCallID string `json:"tool_call_id"`
	Step       int    `json:"step"`
	Host       string `json:"host,omitempty"`
}
