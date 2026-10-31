package tools

import "github.com/vicgupta/tinyagent/internal/model"

// DelegateToolName is the opt-in tool that hands a task to a read-only
// subagent with a fresh context.
const DelegateToolName = "delegate"

// DelegateSpec describes the delegate tool.
func DelegateSpec() model.ToolSpec {
	return spec(DelegateToolName, "Hand a self-contained investigation to a read-only subagent that starts with a fresh context. "+
		"It cannot see this conversation and can only use read_file, list_files, and grep; it never edits files or runs commands. "+
		"Only its final report comes back to you, so use it for searches that would otherwise fill your context "+
		"(e.g. find where X is handled and list the callers of Y). Write the task as a complete brief: "+
		"what to find, where to look, and what the report must contain.",
		props{
			"task":  "the complete, self-contained brief for the subagent",
			"label": "optional short label for logs and events",
		}, "task")
}

// WithTaskType adds the optional task_type argument, one of types, to a
// delegate or worker spec; with no types it returns s unchanged.
func WithTaskType(s model.ToolSpec, types []string) model.ToolSpec {
	if len(types) == 0 {
		return s
	}
	params := map[string]any{}
	for k, v := range s.Function.Parameters {
		params[k] = v
	}
	props := map[string]any{}
	if p, ok := params["properties"].(map[string]any); ok {
		for k, v := range p {
			props[k] = v
		}
	}
	enum := append([]string(nil), types...)
	props["task_type"] = map[string]any{"type": "string", "enum": enum, "description": "optional kind of task; picks the model that runs it"}
	params["properties"] = props
	s.Function.Parameters = params
	return s
}

// LimitResult bounds out like any other tool result: truncated to
// MaxOutputChars, or saved to a per-run file when OutputFiles is set.
func (e *Executor) LimitResult(call model.ToolCall, out string, isErr bool) Result {
	return e.limit(call, out, isErr)
}
