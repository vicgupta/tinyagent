package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/outschema"
	"github.com/vicgupta/tinyagent/internal/tools"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// SubmitToolName is the built-in tool an --output-schema run ends with. It
// contains no "__", so it cannot collide with MCP tools (<alias>__<name>).
const SubmitToolName = "submit_result"

// MaxInvalidOutputs is how many consecutive invalid result attempts (an
// invalid or mixed submit_result call, or a turn with no tool calls) end an
// --output-schema run as InvalidOutput.
const MaxInvalidOutputs = 3

const submitToolDescription = "Submit the final result of the task. Call this exactly once, as the only tool call in your message, when the task is done. The arguments are the result and must conform to the parameters schema."

// structuredOutputNotice is appended to the system prompt of --output-schema
// runs. It is fixed text, so the prompt stays stable for caching.
const structuredOutputNotice = "Structured output is required. When the task is done, end the run by calling the " + SubmitToolName + " tool, alone in its message, with the final result as its arguments. Plain text, the completion marker, or stopping without that call does not finish the task."

const submitReminder = "You have not submitted a result. The run only ends when you call the " + SubmitToolName + " tool, alone in its message, with arguments that conform to its schema."

// SubmitToolSpec returns the submit_result tool for s.
func SubmitToolSpec(s *outschema.Schema) model.ToolSpec {
	return model.ToolSpec{Type: "function", Function: model.FunctionSpec{
		Name: SubmitToolName, Description: submitToolDescription, Parameters: s.Params,
	}}
}

func (a *Agent) outputSchemaParams() map[string]any {
	if a.OutputSchema == nil {
		return nil
	}
	return a.OutputSchema.Params
}

func (a *Agent) outputSchemaDigest() string {
	if a.OutputSchema == nil {
		return ""
	}
	return a.OutputSchema.Digest
}

// structuredStep handles one assistant message of an --output-schema run. It
// returns a non-nil trajectory when the run must stop now (a save failure), or
// the validated result when msg submitted one. Otherwise it updates the
// consecutive-invalid counter.
func (a *Agent) structuredStep(ctx context.Context, t *trajectory.Trajectory, v *view, msg model.Message, invalid *int) (*trajectory.Trajectory, json.RawMessage) {
	if len(msg.ToolCalls) == 0 {
		*invalid++
		t.Messages = append(t.Messages, model.Message{Role: "user", Content: submitReminder})
		v.add(t, t.Messages[len(t.Messages)-1])
		a.event("warning", map[string]any{"message": "no " + SubmitToolName + " call; reminded the model"})
		return nil, nil
	}
	var result json.RawMessage
	attempted, failed := false, false
	for _, call := range msg.ToolCalls {
		if call.Function.Name != SubmitToolName {
			a.toolStep(ctx, t, v, call)
			continue
		}
		attempted = true
		a.event("tool.call", map[string]any{"id": call.ID, "name": call.Function.Name, "arguments": eventArguments(call.Function.Arguments)})
		if len(msg.ToolCalls) > 1 {
			failed = true
			a.recordToolResult(t, v, call, "error: "+SubmitToolName+" must be the only tool call in its message; the other calls ran, so review their results and call "+SubmitToolName+" again on its own", true)
			continue
		}
		obj, errs := a.OutputSchema.Validate(call.Function.Arguments)
		if len(errs) > 0 {
			failed = true
			out := "error: the result does not conform to the schema; fix these problems and call " + SubmitToolName + " again:\n- " + strings.Join(errs, "\n- ")
			a.recordToolResult(t, v, call, tools.Truncate(out, a.Tools.MaxOutputChars), true)
			continue
		}
		a.recordToolResult(t, v, call, "ok: result accepted", false)
		result = obj
	}
	switch {
	case result != nil:
		return nil, result
	case attempted && failed:
		*invalid++
	case !attempted:
		*invalid = 0
	}
	if *invalid > 0 && *invalid < MaxInvalidOutputs && a.Verbose {
		fmt.Fprintf(a.Err, "invalid %s attempt %d of %d\n", SubmitToolName, *invalid, MaxInvalidOutputs)
	}
	return nil, nil
}
