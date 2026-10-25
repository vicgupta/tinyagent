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
	"github.com/vicgupta/tinyagent/internal/sandboxnet"
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
	// StatusOutputTruncated: too many consecutive responses were cut off at
	// the output-token limit.
	StatusOutputTruncated = "OutputTruncated"
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
	// ProviderAPI and ProviderBaseURL describe a config-defined provider,
	// recorded in trajectories and run.started; empty for built-ins.
	ProviderAPI     string
	ProviderBaseURL string
	Tools           *tools.Executor
	Config          config.AgentConfig
	Out             io.Writer // progress log; nil discards
	Err             io.Writer // warnings; nil discards
	Verbose         bool
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
	// Snapshots, when non-nil, captures the workspace: a baseline before
	// the first request, before every tool batch that can change files, and
	// at the end of the run (--snapshots).
	Snapshots Snapshotter
	// Worktree, when non-nil, records the --worktree checkout a fresh run
	// works in; it is copied into the new trajectory.
	Worktree *trajectory.WorktreeInfo

	cur  *trajectory.Trajectory
	view *view
	// snapFailed stops further snapshots after one fails.
	snapFailed bool

	// Subagents runs the opt-in delegate tool's children; Tools.Delegate
	// offers the tool. nil leaves delegate calls failing with an error.
	Subagents *Subagents
	// subagentID is set on a delegate child (d1, d2, ...).
	subagentID string
	// delegateStop is the status a delegate child ended with when it must
	// also end the parent: Interrupted, TimeLimitExceeded or
	// CostLimitExceeded.
	delegateStop string
}

// Snapshotter captures the working directory.
type Snapshotter interface {
	Take(kind string, step int) (trajectory.Snapshot, error)
}

// readOnlyTools never change files, so a batch of only these needs no
// snapshot.
var readOnlyTools = map[string]bool{
	"read_file": true, "list_files": true, "grep": true, "read_process": true,
	tools.PlanToolName: true, tools.ViewImageToolName: true, "skill": true, SubmitToolName: true,
	tools.DelegateToolName: true,
}

// mutating reports whether any call in the batch may change the workspace.
func mutating(calls []model.ToolCall) bool {
	for _, c := range calls {
		if !readOnlyTools[c.Function.Name] {
			return true
		}
	}
	return false
}

// snapshot takes one workspace snapshot, records it in t, and reports it.
func (a *Agent) snapshot(t *trajectory.Trajectory, kind string) error {
	start := time.Now()
	s, err := a.Snapshots.Take(kind, t.Steps)
	if err != nil {
		a.snapFailed = true
		return err
	}
	t.Snapshots = append(t.Snapshots, s)
	ms := time.Since(start).Milliseconds()
	fields := map[string]any{"id": s.ID, "step": s.Step, "kind": s.Kind, "time": s.Time, "files": s.Files, "bytes": s.Bytes,
		"skipped_ignored": s.SkippedIgnored, "skipped_large": s.SkippedLarge, "store": s.Store, "duration_ms": ms}
	if len(s.LargePaths) > 0 {
		fields["large_paths"] = s.LargePaths
	}
	a.event("workspace.snapshot", fields)
	if a.Verbose && a.Err != nil {
		fmt.Fprintf(a.Err, "snapshot %s (%s, step %d): %d files, %d bytes, %d ignored, %d over size limit, %dms\n",
			s.ID, s.Kind, s.Step, s.Files, s.Bytes, s.SkippedIgnored, s.SkippedLarge, ms)
	}
	return nil
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
		if t.ApplyPatchEnabled != a.Tools.ApplyPatch {
			t.ExitStatus, t.Submission = StatusError, "resume apply_patch setting differs from checkpoint"
			return t
		}
		if t.SubagentsEnabled != a.Tools.Delegate {
			t.ExitStatus, t.Submission = StatusError, "resume subagents setting differs from checkpoint"
			return t
		}
		if t.WebFetchEnabled != (a.Tools.WebFetch != nil) {
			t.ExitStatus, t.Submission = StatusError, "resume web_fetch setting differs from checkpoint"
			return t
		}
		t.ExitStatus, t.Submission = "", ""
		if t.OpenAIAPI == "" {
			t.OpenAIAPI = a.OpenAIAPI
		}
		t.ProviderAPI, t.ProviderBaseURL = a.ProviderAPI, a.ProviderBaseURL
		a.closeInterrupted(t)
		notice := resumeNotice
		if finished {
			notice = trajectory.FollowUpNotice
		}
		if lost := a.lostProcesses(t); lost != "" {
			notice += "\n\n" + lost
		}
		if cur := AdditionalDirsInfo(a.Tools.ExtraDirs, a.Tools.ReadOnly); !sameDirs(t.AdditionalDirs, cur) {
			notice += "\n\n" + dirsChangedNotice(cur)
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
			ProviderAPI:           a.ProviderAPI,
			ProviderBaseURL:       a.ProviderBaseURL,
			SchemaVersion:         trajectory.SchemaVersion,
			Root:                  a.Tools.Root,
			SystemPrompt:          system,
			SkipAgentsMD:          a.SkipAgentsMD,
			Tools:                 toolNames(a.Tools.Catalog()),
			ToolSignature:         toolSignature(a.Tools.ReadOnly, a.Tools.Plan, a.Tools.Images, a.Tools.ApplyPatch, a.Tools.Delegate, a.Tools.WebFetch != nil),
			PlanEnabled:           a.Tools.Plan,
			ImagesEnabled:         a.Tools.Images,
			ApplyPatchEnabled:     a.Tools.ApplyPatch,
			SubagentsEnabled:      a.Tools.Delegate,
			WebFetchEnabled:       a.Tools.WebFetch != nil,
			MCPEnabled:            len(a.Tools.ExternalSpecs) > 0,
			ExternalTools:         externalToolNames(a.Tools.ExternalSpecs),
			ExternalToolSignature: externalToolSignature(a.Tools.ExternalSpecs),
			OutputSchema:          a.outputSchemaParams(),
			OutputSchemaDigest:    a.outputSchemaDigest(),
			SkillsEnabled:         a.SkillsEnabled,
			Skills:                skillInfos(a.Tools.Skills),
			Worktree:              a.Worktree,
			Messages: []model.Message{
				{Role: "system", Content: system},
				{Role: "user", Content: task},
			},
		}
	}
	a.cur = t
	// The output budget may change on resume, so the current one is recorded.
	t.MaxOutputTokens = a.Config.MaxOutputTokens
	t.Sandbox, t.Environment, t.CommandEnv = nil, nil, nil
	t.FileAccess = FileAccessInfo(a.Tools.FileAccess)
	t.AdditionalDirs = AdditionalDirsInfo(a.Tools.ExtraDirs, a.Tools.ReadOnly)
	t.WebFetch = nil
	if wf := a.Tools.WebFetch; wf != nil {
		t.WebFetch = &trajectory.WebFetchInfo{AllowedDomains: wf.Domains}
	}
	switch env := a.Tools.Env.(type) {
	case tools.Local:
		if sb := env.Sandbox; sb != nil {
			t.Sandbox = &trajectory.SandboxInfo{Backend: sb.Backend, Writable: sb.Writable, Network: sb.Network, AllowedDomains: sb.AllowedDomains}
		}
		t.CommandEnv = CommandEnvInfo(env.CommandEnv)
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
	t.SnapshotsEnabled = a.Snapshots != nil
	if a.Snapshots != nil {
		if err := a.snapshot(t, "baseline"); err != nil {
			return a.finish(t, StatusError, "workspace snapshot failed: "+err.Error())
		}
		if err := a.step(t); err != nil {
			return a.saveFailed(t, err)
		}
	}

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
		if cut, reason := model.Truncated(msg); cut {
			if done := a.truncated(t, v, msg, reason); done != nil {
				return done
			}
			continue
		}
		t.ConsecutiveTruncations = 0

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

		if a.Snapshots != nil && mutating(msg.ToolCalls) {
			if err := a.snapshot(t, "step"); err != nil {
				// No tool runs without its snapshot; record why.
				reason := fmt.Sprintf("workspace snapshot before step %d failed: %v", t.Steps, err)
				for _, call := range msg.ToolCalls {
					t.Messages = append(t.Messages, model.Message{Role: "tool", ToolCallID: call.ID, Content: "error: not run: " + reason})
					v.add(t, t.Messages[len(t.Messages)-1])
				}
				return a.finish(t, StatusError, reason+"; the tool calls were not run")
			}
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
		costStop := false
		for _, call := range msg.ToolCalls {
			a.event("tool.call", map[string]any{"id": call.ID, "name": call.Function.Name, "arguments": eventArguments(call.Function.Arguments)})
			var out string
			var images []model.Image
			var isError bool
			var res tools.Result
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
				res = a.runTool(ctx, t, call)
				a.recordWebFetches()
				out, images, isError = res.Text, res.Images, res.IsError
				switch stop := a.takeDelegateStop(); stop {
				case StatusInterrupted, StatusTimeLimit:
					// The delegate call stays pending; a resume closes it
					// as interrupted, like any call cut off mid-run.
					return a.finish(t, stop, "")
				case StatusCostLimit:
					costStop = true
				}
			}
			fields := map[string]any{"id": call.ID, "name": call.Function.Name, "output": out, "is_error": isError}
			if formatError {
				fields["format_error"] = true
			}
			a.noteOutputFile(t, call, res, fields)
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
			if costStop {
				return a.finish(t, StatusCostLimit, "")
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
	if !a.Hooks.HasStop() || a.subagentID != "" {
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
	res := a.runTool(ctx, t, call)
	// The next limit check ends a structured run the delegate stopped.
	a.takeDelegateStop()
	a.recordResult(t, v, call, res)
	return res.IsError
}

// noteOutputFile adds a saved result's file to its tool.result event
// fields and to the trajectory.
func (a *Agent) noteOutputFile(t *trajectory.Trajectory, call model.ToolCall, res tools.Result, fields map[string]any) {
	if res.OutputFile == "" {
		return
	}
	fields["output_file"] = res.OutputFile
	fields["output_bytes"] = res.OutputBytes
	t.ToolOutputFiles = append(t.ToolOutputFiles, trajectory.ToolOutputFile{ToolCallID: call.ID, OutputFile: res.OutputFile, OutputBytes: res.OutputBytes})
}

func (a *Agent) recordToolResult(t *trajectory.Trajectory, v *view, call model.ToolCall, out string, isError bool, images ...model.Image) {
	a.recordResult(t, v, call, tools.Result{Text: out, IsError: isError, Images: images})
}

func (a *Agent) recordResult(t *trajectory.Trajectory, v *view, call model.ToolCall, res tools.Result) {
	out, isError, images := res.Text, res.IsError, res.Images
	fields := map[string]any{"id": call.ID, "name": call.Function.Name, "output": out, "is_error": isError}
	a.noteOutputFile(t, call, res, fields)
	a.event("tool.result", fields)
	t.Messages = append(t.Messages, model.Message{Role: "tool", ToolCallID: call.ID, Content: out, Images: images})
	v.add(t, t.Messages[len(t.Messages)-1])
	if a.Verbose {
		fmt.Fprintf(a.Out, "  -> %s\n", indent(out))
	}
}

// truncated handles a response cut off at the output-token limit: it never
// ends the run successfully and none of its tool calls run. Each call gets a
// "not run" result, the model gets a correction, and after max_truncations
// such responses in a row the run ends OutputTruncated. It returns the final
// trajectory, or nil to continue.
func (a *Agent) truncated(t *trajectory.Trajectory, v *view, msg model.Message, reason string) *trajectory.Trajectory {
	t.Truncations++
	t.ConsecutiveTruncations++
	limit := a.outputLimit()
	a.event("model.truncated", map[string]any{
		"step": t.Steps, "reason": reason, "max_output_tokens": a.Config.MaxOutputTokens,
		"tool_calls_dropped": len(msg.ToolCalls), "consecutive": t.ConsecutiveTruncations,
	})
	fmt.Fprintf(a.Err, "warning: response cut off at the output-token limit (%s, %s); %d tool calls not run\n", reason, limit, len(msg.ToolCalls))
	result := fmt.Sprintf("error: [not run: your response was cut off at the output-token limit (%s) before this call was complete]", limit)
	for _, c := range msg.ToolCalls {
		a.event("tool.result", map[string]any{"id": c.ID, "name": c.Function.Name, "output": result, "is_error": true, "truncated": true})
		t.Messages = append(t.Messages, model.Message{Role: "tool", ToolCallID: c.ID, Content: result})
		v.add(t, t.Messages[len(t.Messages)-1])
	}
	if t.ConsecutiveTruncations >= a.maxTruncations() {
		return a.finish(t, StatusOutputTruncated, "")
	}
	t.Messages = append(t.Messages, model.Message{Role: "user", Content: fmt.Sprintf(truncationNotice, limit)})
	v.add(t, t.Messages[len(t.Messages)-1])
	if err := a.step(t); err != nil {
		return a.saveFailed(t, err)
	}
	return nil
}

// truncationNotice is the correction after a cut-off response; %s is the
// output limit.
const truncationNotice = "Your previous response was cut off at the output-token limit (%s), so it was not accepted and none of its tool calls ran. Continue in smaller steps: write large files in parts, prefer edit_file over rewriting whole files, and keep your final answer concise."

// outputLimit describes the per-request output budget for messages.
func (a *Agent) outputLimit() string {
	if n := a.Config.MaxOutputTokens; n > 0 {
		return fmt.Sprintf("%d tokens", n)
	}
	if strings.HasPrefix(a.ModelName, "anthropic/") {
		return fmt.Sprintf("%d tokens", model.DefaultAnthropicMaxTokens)
	}
	return "provider default"
}

// maxTruncations is the configured cap on consecutive truncated responses;
// an unset (zero) config uses the default.
func (a *Agent) maxTruncations() int {
	if a.Config.MaxTruncations > 0 {
		return a.Config.MaxTruncations
	}
	return config.DefaultMaxTruncations
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
func (a *Agent) runTool(ctx context.Context, t *trajectory.Trajectory, call model.ToolCall) tools.Result {
	if a.Hooks == nil || !a.Tools.Permits(call.Function.Name) {
		return a.execTool(ctx, t, call)
	}
	in := hooks.Input{
		ToolName: call.Function.Name, ToolCallID: call.ID, Arguments: eventArguments(call.Function.Arguments),
		Root: a.Tools.Root, ReadOnly: a.Tools.ReadOnly, Step: t.Steps,
		Subagent: a.subagentID,
	}
	for _, r := range a.Hooks.Pre(ctx, in) {
		a.hookRan(call, r)
		if r.Denied {
			return tools.Result{Text: tools.Truncate(fmt.Sprintf("error: denied by pre_tool hook %s: %s", r.Command, r.Reason), a.Tools.MaxOutputChars), IsError: true}
		}
	}
	// Hooks see only the text, whose placeholders stand in for images.
	res := a.execTool(ctx, t, call)
	in.OutputFile = res.OutputFile
	var feedback strings.Builder
	for _, r := range a.Hooks.Post(ctx, in, res.Text, res.IsError) {
		a.hookRan(call, r)
		feedback.WriteString(r.Feedback)
	}
	if feedback.Len() > 0 {
		res.Text = withFeedback(res, feedback.String(), a.Tools.MaxOutputChars)
	}
	return res
}

// withFeedback appends post_tool feedback to a result, re-truncating to max.
// A notice pointing at saved output stays whole at the end.
func withFeedback(res tools.Result, feedback string, max int) string {
	if res.Notice != "" {
		body := strings.TrimSuffix(strings.TrimSuffix(res.Text, res.Notice), "\n")
		if text, ok := tools.TruncateWithNotice(body+feedback, res.Notice, max); ok {
			return text
		}
	}
	return tools.Truncate(res.Text+feedback, max)
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
	system += tools.ExtraDirsNotice(effectiveDirs(a.Tools.ExtraDirs, a.Tools.ReadOnly))
	if a.Tools.ReadOnly {
		system += "\n\n" + readOnlyNotice
	}
	if a.OutputSchema != nil {
		system += "\n\n" + structuredOutputNotice
	}
	if a.subagentID != "" {
		system += "\n\n" + subagentNotice
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
	if a.Snapshots != nil && !a.snapFailed {
		if err := a.snapshot(t, "final"); err != nil && a.Err != nil {
			fmt.Fprintf(a.Err, "warning: final workspace snapshot failed: %v\n", err)
		}
	}
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
	a.recordNetworkDenials(t)
	if a.OnStep != nil {
		return a.OnStep(t)
	}
	return nil
}

// recordNetworkDenials records the requests the sandbox allowlist proxy
// refused since the last step, from commands and background processes.
func (a *Agent) recordNetworkDenials(t *trajectory.Trajectory) {
	src, ok := a.Tools.Env.(interface{ TakeNetworkDenials() []sandboxnet.Denial })
	if !ok {
		return
	}
	for _, d := range src.TakeNetworkDenials() {
		t.SandboxNetworkDenials = append(t.SandboxNetworkDenials, trajectory.NetworkDenial{Host: d.Host, Port: d.Port, Reason: d.Reason})
		a.event("sandbox.network_denied", map[string]any{"host": d.Host, "port": d.Port, "reason": d.Reason})
		if a.Verbose {
			fmt.Fprintf(a.Err, "sandbox: network denied: %s port %d: %s\n", d.Host, d.Port, d.Reason)
		}
	}
}

// recordWebFetches emits a web_fetch event, and with -v a stderr line, for
// each fetch the last tool call made.
func (a *Agent) recordWebFetches() {
	if a.Tools.WebFetch == nil {
		return
	}
	for _, r := range a.Tools.WebFetch.TakeRecords() {
		a.event("web_fetch", r.Fields())
		if !a.Verbose {
			continue
		}
		switch {
		case r.DeniedReason != "":
			fmt.Fprintf(a.Err, "web_fetch: %s denied: %s\n", r.URL, r.DeniedReason)
		case r.Error != "" && r.Status == 0:
			fmt.Fprintf(a.Err, "web_fetch: %s failed: %s (%dms)\n", r.URL, r.Error, r.Duration.Milliseconds())
		default:
			trunc := ""
			if r.Truncated {
				trunc = ", truncated"
			}
			fmt.Fprintf(a.Err, "web_fetch: %s -> %d %s, %d bytes%s (%dms)\n", r.URL, r.Status, r.ContentType, r.Bytes, trunc, r.Duration.Milliseconds())
		}
	}
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

// FileAccessInfo records f's rules for trajectories and run.started; nil
// when f is nil.
func FileAccessInfo(f *tools.FileAccess) *trajectory.FileAccessInfo {
	if f == nil {
		return nil
	}
	read, write := f.Rules()
	return &trajectory.FileAccessInfo{DenyRead: append([]string{}, read...), DenyWrite: append([]string{}, write...)}
}

// effectiveDirs returns dirs with every mode read when readOnly.
func effectiveDirs(dirs []tools.ExtraDir, readOnly bool) []tools.ExtraDir {
	if !readOnly || len(dirs) == 0 {
		return dirs
	}
	out := make([]tools.ExtraDir, len(dirs))
	for i, d := range dirs {
		out[i] = tools.ExtraDir{Path: d.Path, Mode: tools.DirRead}
	}
	return out
}

// AdditionalDirsInfo records dirs, with their effective modes, for
// trajectories and run.started; nil when there are none.
func AdditionalDirsInfo(dirs []tools.ExtraDir, readOnly bool) []trajectory.AdditionalDir {
	var out []trajectory.AdditionalDir
	for _, d := range effectiveDirs(dirs, readOnly) {
		out = append(out, trajectory.AdditionalDir{Path: d.Path, Mode: d.Mode})
	}
	return out
}

func sameDirs(a, b []trajectory.AdditionalDir) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// dirsChangedNotice tells a resumed model that the additional directories
// differ from the ones its system prompt lists.
func dirsChangedNotice(cur []trajectory.AdditionalDir) string {
	if len(cur) == 0 {
		return "The additional directories have changed for this resumed run: the file tools can now reach only the working directory."
	}
	var b strings.Builder
	b.WriteString("The additional directories have changed for this resumed run; the file tools can now reach these, by absolute path, besides the working directory:")
	for _, d := range cur {
		fmt.Fprintf(&b, "\n- %s (%s)", d.Path, d.Mode)
	}
	return b.String()
}

// CommandEnvInfo summarizes c by name for trajectories and run.started; nil
// when no policy is active.
func CommandEnvInfo(c *tools.CommandEnv) *trajectory.CommandEnvInfo {
	if c == nil {
		return nil
	}
	return &trajectory.CommandEnvInfo{Inherit: c.Inherit, ExcludeSecrets: c.ExcludeSecrets, Passed: append([]string{}, c.Passed...)}
}
