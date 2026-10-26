package agent

import (
	"fmt"
	"strings"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/tools"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// askUserNotice is appended to the system prompt of runs with ask_user. It
// is fixed text, so the prompt stays stable for caching.
const askUserNotice = "The " + tools.AskUserToolName + " tool is available. Use it only when information that blocks the task is missing and cannot be found in the workspace, " +
	"for example a product decision you cannot infer. Call it alone in its message, with one question; the run then ends until the user answers. " +
	"Otherwise proceed, and state the assumptions you made in your final answer."

// askUserAloneResult answers every call of a message that combined
// ask_user with other calls; none of them ran.
const askUserAloneResult = "error: not run; " + tools.AskUserToolName + " must be the only tool call in its message, so none of this message's calls ran. " +
	"Call " + tools.AskUserToolName + " alone if you still need to ask, or make the other calls without it"

// askUserBudgetResult answers an ask_user call once the question budget is
// used up.
const askUserBudgetResult = "error: no more questions can be asked in this run; proceed on stated assumptions and list them in your final answer"

// askUserMixed reports whether calls include ask_user together with other
// calls.
func (a *Agent) askUserMixed(calls []model.ToolCall) bool {
	if !a.Tools.AskUser || len(calls) < 2 {
		return false
	}
	for _, c := range calls {
		if c.Function.Name == tools.AskUserToolName {
			return true
		}
	}
	return false
}

// rejectMixedAskUser gives every call of msg the "call ask_user alone"
// error result without running any of them.
func (a *Agent) rejectMixedAskUser(t *trajectory.Trajectory, v *view, msg model.Message) error {
	for _, call := range msg.ToolCalls {
		a.event("tool.call", map[string]any{"id": call.ID, "name": call.Function.Name, "arguments": eventArguments(call.Function.Arguments)})
		a.recordToolResult(t, v, call, askUserAloneResult, true)
	}
	t.ConsecutiveFormatErrors = 0
	return a.step(t)
}

// maxQuestions is the per-run question budget.
func (a *Agent) maxQuestions() int {
	if n := a.Config.AskUser.MaxQuestions; n > 0 {
		return n
	}
	return config.DefaultAskUserMaxQuestions
}

// needsInput ends the run with q as the pending question. The ask_user
// call is already the only pending call; it stays unanswered.
func (a *Agent) needsInput(t *trajectory.Trajectory, q *trajectory.Question) *trajectory.Trajectory {
	t.QuestionsAsked++
	t.PendingQuestion = q
	if a.Verbose {
		fmt.Fprintf(a.Out, "  -> question for the user: %s\n", indent(q.Question))
	}
	return a.finish(t, StatusNeedsInput, q.Question)
}

// answer delivers the user's answer as the result of the checkpoint's
// pending ask_user call; notes holds any resume notes for the model.
func (a *Agent) answer(t *trajectory.Trajectory, notes string) {
	q := t.PendingQuestion
	content := trajectory.AnswerNotice
	if notes != "" {
		content += "\n\n" + notes
	}
	content += "\n\n" + a.FollowUp
	t.Messages = append(t.Messages, model.Message{Role: "tool", ToolCallID: q.ToolCallID, Content: content})
	a.event("tool.result", map[string]any{"id": q.ToolCallID, "name": tools.AskUserToolName, "output": content, "is_error": false})
	if a.Verbose {
		fmt.Fprintf(a.Out, "answer: %s\n", strings.TrimSpace(a.FollowUp))
	}
	t.PendingCalls, t.PendingQuestion = nil, nil
}
