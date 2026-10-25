// Package agent runs the step loop: query the model, execute tool calls, repeat.
package agent

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/hooks"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/outschema"
	"github.com/vicgupta/tinyagent/internal/tools"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

const SubmitMarker = "COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT"
const readOnlyNotice = "Read-only mode is active. You may only inspect files with read_file, list_files, and grep. Do not attempt edits or commands."

const (
	StatusSubmitted   = "Submitted"
	StatusCompleted   = "Completed" // model stopped calling tools without the submit marker
	StatusStepLimit   = "StepLimitExceeded"
	StatusCostLimit   = "CostLimitExceeded"
	StatusTimeLimit   = "TimeLimitExceeded"
	StatusInterrupted = "Interrupted"
	StatusError       = "Error"
	// StatusInvalidOutput ends an --output-schema run that produced no valid
	// submit_result within MaxInvalidOutputs consecutive attempts.
	StatusInvalidOutput = "InvalidOutput"
	// StatusFormatError: too many consecutive steps of malformed tool calls.
	StatusFormatError = "RepeatedFormatError"
	// StatusVerificationFailed: stop hooks were still rejecting the result
	// when the max_stop_continuations cap was reached.
	StatusVerificationFailed = "VerificationFailed"
)

// stopNotice opens the user message that sends the model back to work after
// a stop hook rejected its result. It is fixed text.
const stopNotice = "The run was not accepted as finished: a stop hook checked your result and asked you to continue. Address the feedback below, then finish again."

// notRunResult is the tool result for calls in a message that also carried
// the submit marker, when a stop hook sent the model back to work.
const notRunResult = "error: not run; this message submitted the task, so its tool calls were skipped. Call the tool again if you still need it"

// maxAgentsMD caps how much of AGENTS.md goes into the system prompt.
const maxAgentsMD = 32 << 10

type Agent struct {
	Model     model.Model
	ModelName string
	// ReasoningEffort is recorded in new trajectories; the Model sends it.
	ReasoningEffort string
	// OpenAIAPI is the openai provider's resolved transport, recorded in
	// trajectories and run.started; the Model uses it.
	OpenAIAPI string
	Tools     *tools.Executor
	Config    config.AgentConfig
	Out       io.Writer // progress log; nil discards
	Err       io.Writer // warnings; nil discards
	Verbose   bool
	// Streamed means the model already wrote its text to Out as it arrived.
	Streamed bool
	// SkipAgentsMD stops Run from appending the nearest AGENTS.md at or above
	// <root> to the system prompt.
	SkipAgentsMD bool
	// SkillsEnabled records that skills discovery was on for this run; the
	// discovered skills are Tools.Skills.
	SkillsEnabled bool
	// NoCompact turns off context compaction: the full history is always
	// sent, and a context-overflow error ends the run.
	NoCompact bool
	// Hooks run around tool calls the executor permits, and stop hooks
	// before a successful finish; nil runs none.
	Hooks *hooks.Runner
	// OnStep is called after every step, e.g. to save the trajectory. A
	// non-nil error stops the run.
	OnStep func(*trajectory.Trajectory) error
	// OnEvent receives machine-readable progress events when configured.
	OnEvent func(string, map[string]any)
	// Resume, when non-nil, continues that trajectory instead of starting a
	// fresh run: its messages, accounting totals, and steps are the starting
	// state, and a continuation notice is appended before the first request.
	Resume       *trajectory.Trajectory
	ResumeSource string // audit-only input path for resume provenance
	// FollowUp, with Resume, continues a finished (Submitted or Completed)
	// checkpoint with this new user instruction. It must be empty for an
	// unfinished checkpoint and set for a finished one.
	FollowUp string
	// OutputSchema, when non-nil, adds the submit_result tool and makes the
	// run succeed only with a result that validates against it.
	OutputSchema *outschema.Schema

	cur  *trajectory.Trajectory
	view *view
}

// Step returns the current run's completed model calls, or 0 before Run
// starts. Event sinks call it from OnEvent to tag events with a step.
func (a *Agent) Step() int {
	if a.cur == nil {
		return 0
	}
	return a.cur.Steps
}

// resumeNotice is appended to the model's history when a run is resumed, so
// the model knows the workspace may have changed since the last save.
const resumeNotice = trajectory.ContinuationNotice

// Run executes task until submission, a limit, or an error. It always returns
// a trajectory describing how the run ended.
func (a *Agent) Run(ctx context.Context, task string) *trajectory.Trajectory {
	if a.Out == nil {
		a.Out = io.Discard
	}
	if a.Err == nil {
		a.Err = io.Discard
	}
	if a.Config.WallTimeSec > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(a.Config.WallTimeSec)*time.Second)
		defer cancel()
	}
	if a.OnEvent != nil {
		ctx = model.WithRetryObserver(ctx, func(r model.Retry) {
			a.event("provider.retry", map[string]any{"attempt": r.Attempt, "reason": r.Reason, "backoff_ms": r.Backoff.Milliseconds()})
		})
	}

	var t *trajectory.Trajectory
	if a.Resume != nil {
		t = a.Resume
		if a.ModelName != t.Model {
			t.ExitStatus, t.Submission = StatusError, "resume model differs from checkpoint"
			return t
		}
		if err := ValidateResume(t, a.Config, a.Tools.Root); err != nil {
			t.ExitStatus, t.Submission = StatusError, err.Error()
			return t
		}
		finished := trajectory.Finished(t.ExitStatus)
		if finished && a.FollowUp == "" {
			t.ExitStatus, t.Submission = StatusError, "checkpoint already finished; continue it with a follow-up task"
			return t
		}
		if !finished && a.FollowUp != "" {
			t.ExitStatus, t.Submission = StatusError, "a follow-up task can only continue a finished checkpoint"
			return t
		}
		source := a.ResumeSource
		if source == "" {
			source = "memory"
		}
		t.Resume = &trajectory.ResumeInfo{
			Source: source, ExitStatus: t.ExitStatus, Submission: t.Submission,
			Steps: t.Steps, Message: len(t.Messages), FollowUp: finished, Previous: t.Resume,
		}
		if t.PlanEnabled != a.Tools.Plan {
			t.ExitStatus, t.Submission = StatusError, "resume plan setting differs from checkpoint"
			return t
		}
		if t.ImagesEnabled != a.Tools.Images {
			t.ExitStatus, t.Submission = StatusError, "resume images setting differs from checkpoint"
			return t
		}
		t.ExitStatus, t.Submission = "", ""
		if t.OpenAIAPI == "" {
			t.OpenAIAPI = a.OpenAIAPI
		}
		a.closeInterrupted(t)
		notice := resumeNotice
		if finished {
			notice = trajectory.FollowUpNotice
		}
		if lost := a.lostProcesses(t); lost != "" {
			notice += "\n\n" + lost
		}
		if finished {
			notice += "\n\n" + a.FollowUp
			if a.Verbose {
				fmt.Fprintf(a.Out, "follow-up task: %s\n", a.FollowUp)
			}
		}
		t.SchemaVersion = trajectory.SchemaVersion
		t.Messages = append(t.Messages, model.Message{Role: "user", Content: notice})
		// Persist the validated starting state before the first request.
		if err := a.step(t); err != nil {
			return a.saveFailed(t, err)
		}
	} else {
		system := a.systemPrompt()
		t = &trajectory.Trajectory{
			Task:                  task,
			ReadOnly:              a.Tools.ReadOnly,
			Model:                 a.ModelName,
			ReasoningEffort:       a.ReasoningEffort,
			OpenAIAPI:             a.OpenAIAPI,
			SchemaVersion:         trajectory.SchemaVersion,
			Root:                  a.Tools.Root,
			SystemPrompt:          system,
			SkipAgentsMD:          a.SkipAgentsMD,
			Tools:                 toolNames(a.Tools.Catalog()),
			ToolSignature:         toolSignature(a.Tools.ReadOnly, a.Tools.Plan, a.Tools.Images),
			PlanEnabled:           a.Tools.Plan,
			ImagesEnabled:         a.Tools.Images,
			MCPEnabled:            len(a.Tools.ExternalSpecs) > 0,
			ExternalTools:         externalToolNames(a.Tools.ExternalSpecs),
			ExternalToolSignature: externalToolSignature(a.Tools.ExternalSpecs),
			OutputSchema:          a.outputSchemaParams(),
			OutputSchemaDigest:    a.outputSchemaDigest(),
			SkillsEnabled:         a.SkillsEnabled,
			Skills:                skillInfos(a.Tools.Skills),
			Messages: []model.Message{
				{Role: "system", Content: system},
				{Role: "user", Content: task},
			},
		}
	}
	a.cur = t
	t.Sandbox, t.Environment = nil, nil
	switch env := a.Tools.Env.(type) {
	case tools.Local:
		if sb := env.Sandbox; sb != nil {
			t.Sandbox = &trajectory.SandboxInfo{Backend: sb.Backend, Writable: sb.Writable, Network: sb.Network}
		}
	case *tools.Container:
		t.Environment = &trajectory.EnvironmentInfo{Name: "container", Runtime: env.Runtime, Image: env.Image}
	}
	specs := a.Tools.Catalog()
	if a.OutputSchema != nil {
		specs = append(specs, SubmitToolSpec(a.OutputSchema))
	}
	v := viewFromTrajectory(t)
	a.view = v
	invalidOutputs := 0

	for {
		if status, ok := a.checkLimits(ctx, t); !ok {
			return a.finish(t, status, "")
		}
		if a.needsCompaction(t, v) {
			if err := a.compact(ctx, t, v, "proactive"); err != nil {
				return a.compactFailed(ctx, t, err)
			}
			if status, ok := a.checkLimits(ctx, t); !ok {
				return a.finish(t, status, "")
			}
		}

		msg, cost, err := a.Model.Query(ctx, a.request(t, v), specs)
		if err != nil && !a.NoCompact && model.IsContextOverflow(err) && ctx.Err() == nil {
			if cerr := a.compact(ctx, t, v, "overflow"); cerr != nil {
				return a.compactFailed(ctx, t, fmt.Errorf("%w: %v", cerr, err))
			}
			if status, ok := a.checkLimits(ctx, t); !ok {
				return a.finish(t, status, "")
			}
			msg, cost, err = a.Model.Query(ctx, a.request(t, v), specs)
			if err != nil && model.IsContextOverflow(err) {
				err = fmt.Errorf("%w: %v", errStillTooLong, err)
			}
		}
		if err != nil {
			if status, ok := ctxStatus(ctx); ok {
				return a.finish(t, status, "")
			}
			return a.finish(t, StatusError, err.Error())
		}
		v.queried(cost)
		t.Steps++
		t.Cost += cost.USD
		t.InputTokens += cost.InputTokens
		t.OutputTokens += cost.OutputTokens
		t.CacheReadTokens += cost.CacheReadTokens
		t.CacheWriteTokens += cost.CacheWriteTokens
		t.Messages = append(t.Messages, msg)
		v.add(t, msg)
		v.pendingChars = 0 // already counted in cost.OutputTokens
		a.logAssistant(msg)
		if msg.Content != "" {
			a.event("assistant.message", map[string]any{"text": msg.Content})
		}

		if a.OutputSchema != nil {
			done, result := a.structuredStep(ctx, t, v, msg, &invalidOutputs)
			if done != nil {
				return done
			}
			if result != nil {
				if done := a.finishSuccess(ctx, t, v, msg, StatusSubmitted, string(result), result); done != nil {
					return done
				}
				continue
			}
			if invalidOutputs >= MaxInvalidOutputs {
				return a.finish(t, StatusInvalidOutput, fmt.Sprintf("no valid %s after %d consecutive invalid attempts", SubmitToolName, invalidOutputs))
			}
			if err := a.step(t); err != nil {
				return a.saveFailed(t, err)
			}
			continue
		}

		if strings.Contains(msg.Content, SubmitMarker) {
			sub := strings.TrimSpace(strings.Replace(msg.Content, SubmitMarker, "", 1))
			if done := a.finishSuccess(ctx, t, v, msg, StatusSubmitted, sub, nil); done != nil {
				return done
			}
			continue
		}
		if len(msg.ToolCalls) == 0 {
			if done := a.finishSuccess(ctx, t, v, msg, StatusCompleted, msg.Content, nil); done != nil {
				return done
			}
			continue
		}

		// Write-ahead: persist the response and its pending calls before any
		// tool runs, then save after every result.
		// (merged: format-error handling from #66 kept alongside the
		// write-ahead saves and plan tool from #69.)
		t.PendingCalls = make([]trajectory.PendingCall, len(msg.ToolCalls))
		for i, c := range msg.ToolCalls {
			t.PendingCalls[i] = trajectory.PendingCall{ID: c.ID, Name: c.Function.Name}
		}
		if err := a.step(t); err != nil {
			return a.saveFailed(t, err)
		}
		malformed := 0
		for _, call := range msg.ToolCalls {
			a.event("tool.call", map[string]any{"id": call.ID, "name": call.Function.Name, "arguments": eventArguments(call.Function.Arguments)})
			var out string
			var images []model.Image
			var isError bool
			formatError := false
			if reason := a.Tools.FormatError(call); reason != "" {
				malformed++
				t.FormatErrors++
				t.LastFormatError = reason
				t.FormatErrorMessages = append(t.FormatErrorMessages, len(t.Messages))
				out, isError, formatError = a.Tools.FormatCorrection(reason), true, true
			} else if call.Function.Name == tools.PlanToolName && a.Tools.Plan {
				out, isError = a.updatePlan(t, call)
			} else {
				out, images, isError = a.runTool(ctx, t, call)
			}
			fields := map[string]any{"id": call.ID, "name": call.Function.Name, "output": out, "is_error": isError}
			if formatError {
				fields["format_error"] = true
			}
			a.event("tool.result", fields)
			t.Messages = append(t.Messages, model.Message{Role: "tool", ToolCallID: call.ID, Content: out, Images: images})
			t.PendingCalls = t.PendingCalls[1:]
			if len(t.PendingCalls) == 0 {
				t.PendingCalls = nil
			}
			a.recordProcesses(t)
			v.add(t, t.Messages[len(t.Messages)-1])
			if a.Verbose {
				fmt.Fprintf(a.Out, "  -> %s\n", indent(out))
			}
			if err := a.step(t); err != nil {
				return a.saveFailed(t, err)
			}
		}
		if malformed == len(msg.ToolCalls) {
			t.ConsecutiveFormatErrors++
		} else {
			t.ConsecutiveFormatErrors = 0
		}
		if t.ConsecutiveFormatErrors >= a.maxFormatErrors() {
			return a.finish(t, StatusFormatError, "")
		}
	}
}

// finishSuccess ends the run with a successful status unless stop hooks
// reject the result. It returns the final trajectory, or nil after appending
// the hooks' feedback when the model must keep working.
func (a *Agent) finishSuccess(ctx context.Context, t *trajectory.Trajectory, v *view, msg model.Message, status, submission string, structured json.RawMessage) *trajectory.Trajectory {
	if !a.Hooks.HasStop() {
		t.StructuredOutput = structured
		return a.finish(t, status, submission)
	}
	results := a.Hooks.Stop(ctx, hooks.StopInput{
		Status: status, Submission: submission, StructuredOutput: structured,
		Root: a.Tools.Root, ReadOnly: a.Tools.ReadOnly, Step: t.Steps,
		Continuations: t.StopContinuations,
	})
	var feedback []string
	for _, r := range results {
		a.stopHookRan(r)
		if r.Decision == hooks.Continue {
			feedback = append(feedback, r.Feedback)
		}
	}
	if s, ok := ctxStatus(ctx); ok {
		return a.finish(t, s, "")
	}
	if len(feedback) == 0 {
		t.StructuredOutput = structured
		return a.finish(t, status, submission)
	}
	reasons := strings.Join(feedback, "\n\n")
	if t.StopContinuations >= a.Hooks.MaxStopContinuations() {
		t.StopReason = reasons
		return a.finish(t, StatusVerificationFailed, submission)
	}
	if strings.Contains(msg.Content, SubmitMarker) && structured == nil {
		// Skipped calls still need results so the history stays valid.
		for _, c := range msg.ToolCalls {
			a.recordToolResult(t, v, c, notRunResult, true)
		}
	}
	t.StopContinuations++
	t.Messages = append(t.Messages, model.Message{Role: "user", Content: stopNotice + "\n\n" + reasons})
	v.add(t, t.Messages[len(t.Messages)-1])
	if err := a.step(t); err != nil {
		return a.saveFailed(t, err)
	}
	return nil
}

// stopHookRan reports one stop hook run as an event, a verbose line, and,
// for a failed hook, a warning.
func (a *Agent) stopHookRan(r hooks.Result) {
	a.event("hook", map[string]any{
		"event": r.Event, "command": r.Command, "decision": r.Decision, "duration_ms": r.Duration.Milliseconds(),
	})
	if a.Verbose {
		fmt.Fprintf(a.Err, "hook %s %s: %s (%s)\n", r.Event, r.Command, r.Decision, r.Duration.Round(time.Millisecond))
	}
	if r.Decision != hooks.Error {
		return
	}
	msg := fmt.Sprintf("%s hook %s failed: %s; treated as accept", r.Event, r.Command, r.Reason)
	fmt.Fprintln(a.Err, "warning:", msg)
	a.event("warning", map[string]any{"message": msg})
}

// toolStep runs one tool call and records its events and result message.
func (a *Agent) toolStep(ctx context.Context, t *trajectory.Trajectory, v *view, call model.ToolCall) bool {
	a.event("tool.call", map[string]any{"id": call.ID, "name": call.Function.Name, "arguments": eventArguments(call.Function.Arguments)})
	out, images, isError := a.runTool(ctx, t, call)
	a.recordToolResult(t, v, call, out, isError, images...)
	return isError
}

func (a *Agent) recordToolResult(t *trajectory.Trajectory, v *view, call model.ToolCall, out string, isError bool, images ...model.Image) {
	a.event("tool.result", map[string]any{"id": call.ID, "name": call.Function.Name, "output": out, "is_error": isError})
	t.Messages = append(t.Messages, model.Message{Role: "tool", ToolCallID: call.ID, Content: out, Images: images})
	v.add(t, t.Messages[len(t.Messages)-1])
	if a.Verbose {
		fmt.Fprintf(a.Out, "  -> %s\n", indent(out))
	}
}

// maxFormatErrors is the configured cap on consecutive format-error steps;
// an unset (zero) config uses the default.
func (a *Agent) maxFormatErrors() int {
	if a.Config.MaxFormatErrors > 0 {
		return a.Config.MaxFormatErrors
	}
	return config.DefaultMaxFormatErrors
}

// runTool executes call with its matching hooks. Calls the executor rejects
// (read-only denials, unknown tools) never reach hooks, so hooks cannot widen
// tool authority.
func (a *Agent) runTool(ctx context.Context, t *trajectory.Trajectory, call model.ToolCall) (string, []model.Image, bool) {
	if a.Hooks == nil || !a.Tools.Permits(call.Function.Name) {
		return a.execTool(ctx, t, call)
	}
	in := hooks.Input{
		ToolName: call.Function.Name, ToolCallID: call.ID, Arguments: eventArguments(call.Function.Arguments),
		Root: a.Tools.Root, ReadOnly: a.Tools.ReadOnly, Step: t.Steps,
	}
	for _, r := range a.Hooks.Pre(ctx, in) {
		a.hookRan(call, r)
		if r.Denied {
			return tools.Truncate(fmt.Sprintf("error: denied by pre_tool hook %s: %s", r.Command, r.Reason), a.Tools.MaxOutputChars), nil, true
		}
	}
	// Hooks see only the text, whose placeholders stand in for images.
	out, images, isError := a.execTool(ctx, t, call)
	var feedback strings.Builder
	for _, r := range a.Hooks.Post(ctx, in, out, isError) {
		a.hookRan(call, r)
		feedback.WriteString(r.Feedback)
	}
	if feedback.Len() > 0 {
		out = tools.Truncate(out+feedback.String(), a.Tools.MaxOutputChars)
	}
	return out, images, isError
}

// hookRan reports one hook run as an event, a verbose line, and, for a
// failed hook, a warning.
func (a *Agent) hookRan(call model.ToolCall, r hooks.Result) {
	a.event("hook", map[string]any{
		"event": r.Event, "tool_call_id": call.ID, "tool_name": call.Function.Name,
		"command": r.Command, "decision": r.Decision, "duration_ms": r.Duration.Milliseconds(),
	})
	if a.Verbose {
		fmt.Fprintf(a.Err, "hook %s %s for %s: %s (%s)\n", r.Event, r.Command, call.Function.Name, r.Decision, r.Duration.Round(time.Millisecond))
	}
	if r.Decision != hooks.Error {
		return
	}
	outcome := "result unchanged"
	if r.Event == hooks.PreTool {
		outcome = "call allowed"
		if r.Denied {
			outcome = "call denied"
		}
	}
	msg := fmt.Sprintf("%s hook %s failed for %s: %s; %s", r.Event, r.Command, call.Function.Name, r.Reason, outcome)
	fmt.Fprintln(a.Err, "warning:", msg)
	a.event("warning", map[string]any{"message": msg})
}

func externalToolNames(specs []model.ToolSpec) []string {
	names := make([]string, len(specs))
	for i, s := range specs {
		names[i] = s.Function.Name
	}
	return names
}

func externalToolSignature(specs []model.ToolSpec) string {
	data, _ := json.Marshal(specs)
	return fmt.Sprintf("mcp-tools-v1:%x", sha256.Sum256(data))
}

func (a *Agent) event(kind string, fields map[string]any) {
	if a.OnEvent != nil {
		a.OnEvent(kind, fields)
	}
}

func eventArguments(raw string) any {
	var args any
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return raw
	}
	return args
}

// compactFailed ends the run after a failed compaction: as the context's
// status if it was cancelled or timed out, else as an error.
func (a *Agent) compactFailed(ctx context.Context, t *trajectory.Trajectory, err error) *trajectory.Trajectory {
	if status, ok := ctxStatus(ctx); ok {
		return a.finish(t, status, "")
	}
	return a.finish(t, StatusError, err.Error())
}

// systemPrompt returns the effective system prompt for this agent's run.
func (a *Agent) systemPrompt() string {
	system := strings.NewReplacer("{{cwd}}", a.Tools.Root, "{{os}}", runtime.GOOS).Replace(a.Config.SystemPrompt)
	if !a.SkipAgentsMD {
		section, err := loadAgentsMD(a.Tools.Root, a.Out, a.Verbose)
		if err != nil {
			fmt.Fprintln(a.Err, "warning: reading AGENTS.md:", err)
			a.event("warning", map[string]any{"message": "reading AGENTS.md: " + err.Error()})
		}
		system += section
	}
	system += a.Tools.Skills.Catalog()
	if a.Tools.ReadOnly {
		system += "\n\n" + readOnlyNotice
	}
	if a.OutputSchema != nil {
		system += "\n\n" + structuredOutputNotice
	}
	return system
}

// SystemPrompt returns the effective system prompt a run rooted at root would
// use, given cfg and whether AGENTS.md is skipped. It is exported so callers
// can validate a checkpoint against the current configuration.
func SystemPrompt(cfg config.AgentConfig, root string, skipAgentsMD bool, readOnly ...bool) string {
	system := strings.NewReplacer("{{cwd}}", root, "{{os}}", runtime.GOOS).Replace(cfg.SystemPrompt)
	if !skipAgentsMD {
		system += agentsMD(root, io.Discard, io.Discard, false)
	}
	if len(readOnly) > 0 && readOnly[0] {
		system += "\n\n" + readOnlyNotice
	}
	return system
}

// agentsMD returns the project's AGENTS.md as a system prompt section, or ""
// when there is none. Warnings go to errW and verbose notices to outW.
func agentsMD(root string, errW, outW io.Writer, verbose bool) string {
	section, err := loadAgentsMD(root, outW, verbose)
	if err != nil {
		fmt.Fprintln(errW, "warning: reading AGENTS.md:", err)
	}
	return section
}

func loadAgentsMD(root string, outW io.Writer, verbose bool) (string, error) {
	path, err := findAgentsMD(root)
	if err != nil || path == "" {
		return "", err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if verbose {
		fmt.Fprintf(outW, "loaded AGENTS.md from %s (%d bytes)\n", path, len(b))
	}
	s := string(b)
	if len(s) > maxAgentsMD {
		// Back off to a rune boundary so the cut stays valid UTF-8.
		n := maxAgentsMD
		for n > 0 && !utf8.RuneStart(s[n]) {
			n--
		}
		s = s[:n] + "\n[AGENTS.md truncated]"
	}
	return "\n\n## Project instructions (AGENTS.md)\n\n" + s, nil
}

// toolNames returns the tool names from specs, in order.
func toolNames(specs []model.ToolSpec) []string {
	names := make([]string, len(specs))
	for i, s := range specs {
		names[i] = s.Function.Name
	}
	return names
}

// findAgentsMD returns the nearest AGENTS.md at or above root, stopping at the
// repository root: the nearest directory containing .git. Without a .git above
// root, only root itself is checked. It returns "" when there is no such file.
func findAgentsMD(root string) (string, error) {
	stop := root
	for dir := root; ; {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			stop = dir
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	for dir := root; ; {
		path := filepath.Join(dir, "AGENTS.md")
		if _, err := os.Stat(path); err == nil {
			return path, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if dir == stop {
			return "", nil
		}
		dir = filepath.Dir(dir)
	}
}

func (a *Agent) checkLimits(ctx context.Context, t *trajectory.Trajectory) (string, bool) {
	if status, ok := ctxStatus(ctx); ok {
		return status, false
	}
	if a.Config.StepLimit > 0 && t.Steps >= a.Config.StepLimit {
		return StatusStepLimit, false
	}
	if a.Config.CostLimit > 0 && t.Cost >= a.Config.CostLimit {
		return StatusCostLimit, false
	}
	return "", true
}

func ctxStatus(ctx context.Context) (string, bool) {
	switch err := ctx.Err(); {
	case errors.Is(err, context.DeadlineExceeded):
		return StatusTimeLimit, true
	case err != nil:
		return StatusInterrupted, true
	}
	return "", false
}

func (a *Agent) closeTools(t *trajectory.Trajectory) {
	a.recordProcesses(t)
	if summary := a.Tools.Close(); summary != "" {
		t.BackgroundProcesses = summary
		if a.Verbose {
			fmt.Fprintf(a.Out, "stopped background processes:\n%s", summary)
		}
	}
}

func (a *Agent) finish(t *trajectory.Trajectory, status, submission string) *trajectory.Trajectory {
	a.closeTools(t)
	t.ExitStatus, t.Submission = status, submission
	if err := a.step(t); err != nil {
		return a.saveFailed(t, err)
	}
	return t
}

// Never retry persistence after failure: the previous complete checkpoint is
// the recovery boundary, even if an in-memory turn has since completed.
func (a *Agent) saveFailed(t *trajectory.Trajectory, err error) *trajectory.Trajectory {
	a.closeTools(t)
	t.ExitStatus, t.Submission = StatusError, "saving trajectory: "+err.Error()
	return t
}

func (a *Agent) step(t *trajectory.Trajectory) error {
	if a.OnStep != nil {
		return a.OnStep(t)
	}
	return nil
}

func (a *Agent) logAssistant(msg model.Message) {
	switch {
	case msg.Content == "":
	case !a.Streamed:
		fmt.Fprintln(a.Out, msg.Content)
	case !strings.HasSuffix(msg.Content, "\n"):
		fmt.Fprintln(a.Out) // end the streamed line
	}
	if !a.Verbose {
		return
	}
	for _, c := range msg.ToolCalls {
		fmt.Fprintf(a.Out, "[%s] %s\n", c.Function.Name, c.Function.Arguments)
	}
}

func indent(s string) string {
	return strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n     ")
}

// interruptedResult is the synthetic result for a call that was pending when
// the previous invocation stopped. The call is never re-run.
const interruptedResult = "[interrupted: tool did not complete before the agent stopped; " +
	"it may have partially run — verify state before retrying]"

// closeInterrupted gives every pending call of a resumed checkpoint an error
// result, so the history is a complete tool turn again.
func (a *Agent) closeInterrupted(t *trajectory.Trajectory) {
	for _, c := range t.PendingCalls {
		t.Messages = append(t.Messages, model.Message{Role: "tool", ToolCallID: c.ID, Content: "error: " + interruptedResult})
		a.event("resume.interrupted_call", map[string]any{"id": c.ID, "name": c.Name})
		fmt.Fprintf(a.Err, "resume: tool call %s (%s) was interrupted; it was not re-run\n", c.ID, c.Name)
	}
	t.PendingCalls = nil
}

// lostProcesses reports and forgets the background processes the previous
// invocation had registered. It returns the notice text, or "".
func (a *Agent) lostProcesses(t *trajectory.Trajectory) string {
	if len(t.Processes) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("These background processes from the previous invocation are gone and cannot be reattached; start them again if needed:")
	for _, p := range t.Processes {
		fmt.Fprintf(&sb, "\n- %s (pid %d): %s", p.ID, p.PID, p.Command)
		a.event("resume.process_lost", map[string]any{"id": p.ID, "pid": p.PID, "cmd": p.Command})
	}
	t.Processes = nil
	return sb.String()
}

// recordProcesses stores the registered background processes in t.
func (a *Agent) recordProcesses(t *trajectory.Trajectory) {
	ps := a.Tools.Processes()
	if len(ps) == 0 {
		return
	}
	t.Processes = t.Processes[:0]
	for _, p := range ps {
		t.Processes = append(t.Processes, trajectory.ProcessInfo{ID: p.ID, PID: p.PID, Command: p.Command})
	}
}
