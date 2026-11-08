package mcptools

import "fmt"

// actionable formats an MCP tool/resource error for the model: cause, then a
// concrete next step after "; ".
func actionable(cause, next string) string {
	return "error: " + cause + "; " + next
}

func actionablef(next, causeFmt string, args ...any) string {
	return actionable(fmt.Sprintf(causeFmt, args...), next)
}
