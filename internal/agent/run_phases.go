package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/tools"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// The phases of Agent.Run (issue #416). Each phase that can end the run
// returns the final trajectory, or nil when the run goes on. The order of
// the phases, and of the work inside each, is the order Run has always used:
// the append-only history, write-ahead checkpoint saves and "every tool call
// gets a result" guarantees in ARCHITECTURE.md depend on it.

// prepareRun fills in the default writers and wraps ctx with the wall-clock
// limit and the retry observer. The returned cancel func is always non-nil.
func (a *Agent) prepareRun(ctx context.Context) (context.Context, context.CancelFunc) {
	if a.Out == nil {
		a.Out = io.Discard
	}
	if a.Err == nil {
		a.Err = io.Discard
	}
	cancel := context.CancelFunc(func() {})
	if a.Config.WallTimeSec > 0 {
		ctx, cancel = context.WithTimeout(ctx, time.Duration(a.Config.WallTimeSec)*time.Second)
	}
	if a.OnEvent != nil {
		ctx = model.WithRetryObserver(ctx, func(r model.Retry) {
			a.event("provider.retry", map[string]any{"attempt": r.Attempt, "reason": r.Reason, "backoff_ms": r.Backoff.Milliseconds()})
		})
	}
	return ctx, cancel
}

// startTrajectory returns the run's starting trajectory: a validated
// resume of a.Resume, or a fresh one for task. done reports that the run
// already ended (a resume that failed validation or its first save).
func (a *Agent) startTrajectory(task string) (t *trajectory.Trajectory, done bool) {
	if a.Resume != nil {
		return a.resumeTrajectory()
	}
	return a.newTrajectory(task), false
}

// resumeTrajectory validates a.Resume against this run's settings, appends
// the continuation (or follow-up) notice or the ask_user answer, and saves
// the validated starting state before the first request.
func (a *Agent) resumeTrajectory() (*trajectory.Trajectory, bool) {
	t := a.Resume
	finished, asked, err := a.resumePreflight(t)
	if err != nil {
		t.ExitStatus, t.Submission = StatusError, err.Error()
		return t, true
	}
	a.recordResumeInfo(t, finished, asked)
	if msg := a.resumeSettingMismatch(t); msg != "" {
		t.ExitStatus, t.Submission = StatusError, msg
		return t, true
	}
	a.adoptResume(t, asked)
	notes := a.resumeNotes(t)
	t.SchemaVersion = trajectory.SchemaVersion
	if asked {
		// The answer becomes the pending ask_user call's result; no
		// user message is added.
		a.answer(t, strings.Join(notes, "\n\n"))
	} else {
		a.appendResumeNotice(t, finished, notes)
	}
	// Persist the validated starting state before the first request.
	if err := a.step(t); err != nil {
		return a.saveFailed(t, err), true
	}
	return t, false
}

// resumePreflight runs the checks that come before anything is recorded
// in t: the model, the checkpoint's validity (skipped for an in-process
// continuation), and the resume mode.
func (a *Agent) resumePreflight(t *trajectory.Trajectory) (finished, asked bool, err error) {
	if a.ModelName != t.Model {
		return false, false, errors.New("resume model differs from checkpoint")
	}
	if !a.Continue {
		if err := ValidateResume(t, a.Config, a.Tools.Root); err != nil {
			return false, false, err
		}
	}
	return resumeMode(t, a.FollowUp, a.Continue, len(a.TaskImages))
}

// recordResumeInfo records where this resume came from and how the
// checkpoint ended, chaining any earlier resume.
func (a *Agent) recordResumeInfo(t *trajectory.Trajectory, finished, asked bool) {
	source := a.ResumeSource
	if source == "" {
		source = "memory"
	}
	t.Resume = &trajectory.ResumeInfo{
		Source: source, ExitStatus: t.ExitStatus, Submission: t.Submission,
		Steps: t.Steps, Message: len(t.Messages), FollowUp: finished, Answer: asked, Previous: t.Resume,
	}
}

// resumeSettingMismatch returns the error for the first tool setting that
// differs between the checkpoint and this run, or "". The checks run in
// this fixed order, so the reported setting is stable.
func (a *Agent) resumeSettingMismatch(t *trajectory.Trajectory) string {
	for _, c := range resumeSettingChecks {
		if c.saved(t) != c.cur(a) {
			return "resume " + c.name + " setting differs from checkpoint"
		}
	}
	return ""
}

// resumeSettingChecks are the tool settings a resume must match, in the
// order they are checked.
var resumeSettingChecks = []struct {
	name  string
	saved func(*trajectory.Trajectory) bool
	cur   func(*Agent) bool
}{
	{"plan", func(t *trajectory.Trajectory) bool { return t.PlanEnabled }, func(a *Agent) bool { return a.Tools.Plan }},
	{"images", func(t *trajectory.Trajectory) bool { return t.ImagesEnabled }, func(a *Agent) bool { return a.Tools.Images }},
	{"apply_patch", func(t *trajectory.Trajectory) bool { return t.ApplyPatchEnabled }, func(a *Agent) bool { return a.Tools.ApplyPatch }},
	{"subagents", func(t *trajectory.Trajectory) bool { return t.SubagentsEnabled }, func(a *Agent) bool { return a.Tools.Delegate }},
	{"workers", func(t *trajectory.Trajectory) bool { return t.WorkersEnabled }, func(a *Agent) bool { return a.workersOn() }},
	{"web_fetch", func(t *trajectory.Trajectory) bool { return t.WebFetchEnabled }, func(a *Agent) bool { return a.Tools.WebFetch != nil }},
	{"web_search", func(t *trajectory.Trajectory) bool { return t.WebSearchEnabled }, func(a *Agent) bool { return a.Tools.WebSearch != nil }},
	{"lsp", func(t *trajectory.Trajectory) bool { return t.LSPEnabled }, func(a *Agent) bool { return a.Tools.LSP != nil }},
	{"scoped_agents_md", func(t *trajectory.Trajectory) bool { return t.ScopedAgentsMDEnabled }, func(a *Agent) bool { return a.scopedOn() }},
	{"ask_user", func(t *trajectory.Trajectory) bool { return t.AskUserEnabled }, func(a *Agent) bool { return a.Tools.AskUser }},
}

// adoptResume clears the checkpoint's end state, records this run's
// provider settings, and closes calls an interrupted run left pending
// (a pending ask_user call is answered instead).
func (a *Agent) adoptResume(t *trajectory.Trajectory, asked bool) {
	t.ExitStatus, t.Submission = "", ""
	if t.OpenAIAPI == "" {
		t.OpenAIAPI = a.OpenAIAPI
	}
	if a.Profile != "" {
		t.Profile = a.Profile
	}
	t.ProviderAPI, t.ProviderBaseURL = a.ProviderAPI, a.ProviderBaseURL
	if !asked {
		a.closeInterrupted(t)
	}
}

// resumeNotes lists what changed since the checkpoint: lost background
// processes, additional directories and command tools.
func (a *Agent) resumeNotes(t *trajectory.Trajectory) []string {
	var notes []string
	if lost := a.lostProcesses(t); lost != "" {
		notes = append(notes, lost)
	}
	if cur := AdditionalDirsInfo(a.Tools.ExtraDirs, a.Tools.ReadOnly); !sameDirs(t.AdditionalDirs, cur) {
		notes = append(notes, dirsChangedNotice(cur))
	}
	if cur := CommandToolsInfo(a.Tools); !sameCommandTools(t.CommandTools, cur) {
		names := commandToolNames(cur)
		fmt.Fprintf(a.Err, "resume: command tools differ from the checkpoint's (%s); this run uses the current ones\n", strings.Join(names, ", "))
		notes = append(notes, commandToolsChangedNotice(names))
	}
	return notes
}

// appendResumeNotice appends the continuation notice (or, for a finished
// checkpoint, the follow-up notice, instruction and images) as one user
// message.
func (a *Agent) appendResumeNotice(t *trajectory.Trajectory, finished bool, notes []string) {
	notice := resumeNotice
	if finished {
		notice = trajectory.FollowUpNotice
	}
	for _, n := range notes {
		notice += "\n\n" + n
	}
	var images []model.Image
	if finished {
		notice += "\n\n" + a.FollowUp
		if a.Verbose {
			fmt.Fprintf(a.Out, "follow-up task: %s\n", a.FollowUp)
		}
		images = a.TaskImages
		notice += model.AttachmentLabels(images)
	}
	t.Messages = append(t.Messages, model.Message{Role: "user", Content: notice, Images: images})
}

// newTrajectory builds a fresh run's trajectory: the system prompt, the
// task message and the pinned tool settings.
func (a *Agent) newTrajectory(task string) *trajectory.Trajectory {
	system := a.systemPrompt()
	t := &trajectory.Trajectory{
		Task:                  task,
		ReadOnly:              a.Tools.ReadOnly,
		Model:                 a.ModelName,
		ReasoningEffort:       a.ReasoningEffort,
		OpenAIAPI:             a.OpenAIAPI,
		Profile:               a.Profile,
		ProviderAPI:           a.ProviderAPI,
		ProviderBaseURL:       a.ProviderBaseURL,
		SchemaVersion:         trajectory.SchemaVersion,
		Root:                  a.Tools.Root,
		SystemPrompt:          system,
		SkipAgentsMD:          a.SkipAgentsMD,
		Tools:                 toolNames(withoutCommandTools(a.Tools.Catalog(), a.Tools.CommandTools)),
		ScopedTools:           append([]string(nil), a.Tools.Only...),
		ToolSignature:         a.Tools.Selection().Signature(),
		PlanEnabled:           a.Tools.Plan,
		ImagesEnabled:         a.Tools.Images,
		ApplyPatchEnabled:     a.Tools.ApplyPatch,
		SubagentsEnabled:      a.Tools.Delegate,
		WorkersEnabled:        a.workersOn(),
		WebFetchEnabled:       a.Tools.WebFetch != nil,
		WebSearchEnabled:      a.Tools.WebSearch != nil,
		LSPEnabled:            a.Tools.LSP != nil,
		ScopedAgentsMDEnabled: a.scopedOn(),
		AskUserEnabled:        a.Tools.AskUser,
		MCPEnabled:            len(a.Tools.ExternalSpecs) > 0,
		ExternalTools:         externalToolNames(a.Tools.ExternalSpecs),
		ExternalToolSignature: externalToolSignature(a.Tools.ExternalSpecs),
		OutputSchema:          a.outputSchemaParams(),
		OutputSchemaDigest:    a.outputSchemaDigest(),
		SkillsEnabled:         a.SkillsEnabled,
		Skills:                skillInfos(a.Tools.Skills),
		Plugins:               a.Plugins,
		Worktree:              a.Worktree,
		Review:                a.Review,
		Messages: []model.Message{
			{Role: "system", Content: system},
			{Role: "user", Content: a.TaskPreamble + task + model.AttachmentLabels(a.TaskImages), Images: a.TaskImages},
		},
	}
	if a.scopedOn() {
		t.ScopedAgentsMDSince = time.Now().UTC().Format(time.RFC3339Nano)
	}
	a.recordMemory(t)
	return t
}

// watchFallback records the fallback chain and, with a fallback model,
// returns ctx with an observer that records each model switch.
func (a *Agent) watchFallback(ctx context.Context, t *trajectory.Trajectory) context.Context {
	t.FallbackModels, t.ActiveModel = a.FallbackModels, ""
	fb := a.fallback()
	if fb == nil {
		return ctx
	}
	t.ActiveModel = fb.Active()
	return model.WithFallbackObserver(ctx, func(s model.Switch) {
		step := t.Steps + 1
		t.ModelSwitches = append(t.ModelSwitches, trajectory.ModelSwitch{Step: step, From: s.From, To: s.To, Reason: s.Reason})
		t.ActiveModel = s.To
		fmt.Fprintf(a.Err, "model fallback: %s -> %s (%s) at step %d\n", s.From, s.To, s.Reason, step)
		a.event("model.fallback", map[string]any{"step": step, "from": s.From, "to": s.To, "reason": s.Reason, "attempts": s.Attempts})
	})
}

// recordRunSettings records this invocation's settings, which may differ
// from a resumed checkpoint's: output budget, redaction, file access,
// permissions, command tools, directories, web_fetch, LSP and the
// command environment.
func (a *Agent) recordRunSettings(t *trajectory.Trajectory) {
	// The output budget may change on resume, so the current one is recorded.
	t.MaxOutputTokens = a.Config.MaxOutputTokens
	t.Sandbox, t.Environment, t.CommandEnv = nil, nil, nil
	t.RedactSecrets = a.RedactSecrets
	t.FileAccess = FileAccessInfo(a.Tools.FileAccess)
	t.Permissions = PermissionsInfo(a.Policy)
	t.CommandTools = CommandToolsInfo(a.Tools)
	for _, c := range t.CommandTools {
		if !slices.Contains(t.CommandToolNames, c.Name) {
			t.CommandToolNames = append(t.CommandToolNames, c.Name)
		}
	}
	t.AdditionalDirs = AdditionalDirsInfo(a.Tools.ExtraDirs, a.Tools.ReadOnly)
	t.WebFetch = nil
	if wf := a.Tools.WebFetch; wf != nil {
		t.WebFetch = &trajectory.WebFetchInfo{AllowedDomains: wf.Domains}
	}
	t.LSPServers = nil
	if l := a.Tools.LSP; l != nil && l.Manager != nil {
		t.LSPServers = l.Manager.Names()
	}
	a.recordEnvironment(t)
}

// recordEnvironment records where commands run: the local sandbox and
// command environment, or the container.
func (a *Agent) recordEnvironment(t *trajectory.Trajectory) {
	switch env := a.Tools.Env.(type) {
	case tools.Local:
		if sb := env.Sandbox; sb != nil {
			t.Sandbox = &trajectory.SandboxInfo{Backend: sb.Backend, Writable: sb.Writable, Network: sb.Network, AllowedDomains: sb.AllowedDomains}
		}
		t.CommandEnv = CommandEnvInfo(env.CommandEnv)
	case *tools.Container:
		t.Environment = &trajectory.EnvironmentInfo{Name: "container", Runtime: env.Runtime, Image: env.Image}
	}
}

// runSpecs is the tool catalog sent with every request, plus submit_result
// for an --output-schema run.
func (a *Agent) runSpecs() []model.ToolSpec {
	specs := a.Tools.Catalog()
	if a.OutputSchema != nil {
		specs = append(specs, SubmitToolSpec(a.OutputSchema))
	}
	return specs
}

// baselineSnapshot takes and saves the --snapshots baseline before the
// first request.
func (a *Agent) baselineSnapshot(t *trajectory.Trajectory) *trajectory.Trajectory {
	t.SnapshotsEnabled = a.Snapshots != nil
	if a.Snapshots == nil {
		return nil
	}
	if err := a.snapshot(t, "baseline"); err != nil {
		return a.finish(t, StatusError, "workspace snapshot failed: "+err.Error())
	}
	if err := a.step(t); err != nil {
		return a.saveFailed(t, err)
	}
	return nil
}

// runStep is one iteration of the loop: limits and compaction, one model
// request, then the reply's handling (truncation, structured output,
// finishing, or the tool batch).
func (a *Agent) runStep(ctx context.Context, t *trajectory.Trajectory, v *view, specs []model.ToolSpec, invalidOutputs *int) *trajectory.Trajectory {
	if done := a.beforeRequest(ctx, t, v); done != nil {
		return done
	}
	msg, cost, done := a.queryStep(ctx, t, v, specs)
	if done != nil {
		return done
	}
	a.recordReply(t, v, msg, cost)
	if cut, reason := model.Truncated(msg); cut {
		return a.truncated(t, v, msg, reason)
	}
	t.ConsecutiveTruncations = 0
	if a.OutputSchema != nil {
		return a.structuredReply(ctx, t, v, msg, invalidOutputs)
	}
	if done, handled := a.finishReply(ctx, t, v, msg); handled {
		return done
	}
	return a.toolBatchStep(ctx, t, v, msg)
}

// beforeRequest checks the limits and compacts the history when it is
// requested or due, checking the limits again after a compaction.
func (a *Agent) beforeRequest(ctx context.Context, t *trajectory.Trajectory, v *view) *trajectory.Trajectory {
	if status, ok := a.checkLimits(ctx, t); !ok {
		return a.finish(t, status, "")
	}
	a.warnBudget(t, v)
	reason := a.compactionReason(t, v)
	if reason == "" {
		return nil
	}
	if err := a.compact(ctx, t, v, reason); err != nil {
		return a.compactFailed(ctx, t, err)
	}
	if status, ok := a.checkLimits(ctx, t); !ok {
		return a.finish(t, status, "")
	}
	return nil
}

// compactionReason is why the history must be compacted before the next
// request ("requested" once for CompactFirst, "proactive" near the context
// window), or "".
func (a *Agent) compactionReason(t *trajectory.Trajectory, v *view) string {
	if a.CompactFirst && !a.NoCompact && len(v.msgs) > 2 {
		a.CompactFirst = false
		return "requested"
	}
	if a.needsCompaction(t, v) {
		return "proactive"
	}
	return ""
}

// queryStep sends one request. A context overflow compacts the history and
// retries once (unless compaction is off); a failure ends the run.
func (a *Agent) queryStep(ctx context.Context, t *trajectory.Trajectory, v *view, specs []model.ToolSpec) (model.Message, model.Cost, *trajectory.Trajectory) {
	queried := time.Now()
	msg, cost, err := a.queryModel(ctx, a.request(t, v), specs)
	a.requested(t, queried, cost, err)
	if err != nil && !a.NoCompact && model.IsContextOverflow(err) && ctx.Err() == nil {
		if cerr := a.compact(ctx, t, v, "overflow"); cerr != nil {
			return msg, cost, a.compactFailed(ctx, t, fmt.Errorf("%w: %v", cerr, err))
		}
		if status, ok := a.checkLimits(ctx, t); !ok {
			return msg, cost, a.finish(t, status, "")
		}
		queried = time.Now()
		msg, cost, err = a.queryModel(ctx, a.request(t, v), specs)
		a.requested(t, queried, cost, err)
		if err != nil && model.IsContextOverflow(err) {
			err = fmt.Errorf("%w: %v", errStillTooLong, err)
		}
	}
	if err != nil {
		if status, ok := ctxStatus(ctx); ok {
			return msg, cost, a.finish(t, status, "")
		}
		return msg, cost, a.finish(t, StatusError, err.Error())
	}
	return msg, cost, nil
}

// recordReply counts the step and its cost, appends the reply to the
// history, and reports it.
func (a *Agent) recordReply(t *trajectory.Trajectory, v *view, msg model.Message, cost model.Cost) {
	v.queried(cost)
	t.Steps++
	t.Cost += cost.USD
	t.InputTokens += cost.InputTokens
	t.OutputTokens += cost.OutputTokens
	t.CacheReadTokens += cost.CacheReadTokens
	t.CacheWriteTokens += cost.CacheWriteTokens
	role, task := a.role()
	served := a.ModelName
	if t.ActiveModel != "" {
		served = t.ActiveModel
	}
	addUsage(t, role, task, served, cost)
	t.Messages = append(t.Messages, msg)
	v.add(t, msg)
	v.pendingChars = 0 // already counted in cost.OutputTokens
	// The provider's count is the context that request actually
	// sent, plus this reply. Anything appended after it is estimated.
	if cost.InputTokens > 0 {
		t.LastUsage = &trajectory.LastUsage{
			InputTokens:  cost.InputTokens,
			OutputTokens: cost.OutputTokens,
			Messages:     len(t.Messages),
			Step:         t.Steps,
		}
	}
	a.logAssistant(msg)
	if msg.Content != "" {
		a.event("assistant.message", map[string]any{"text": msg.Content})
	}
}

// structuredReply handles a reply in an --output-schema run: a valid
// submit_result finishes (subject to stop hooks), and too many invalid
// attempts in a row end the run InvalidOutput.
func (a *Agent) structuredReply(ctx context.Context, t *trajectory.Trajectory, v *view, msg model.Message, invalidOutputs *int) *trajectory.Trajectory {
	done, result := a.structuredStep(ctx, t, v, msg, invalidOutputs)
	if done != nil {
		return done
	}
	if result != nil {
		return a.finishSuccess(ctx, t, v, msg, StatusSubmitted, string(result), result)
	}
	if *invalidOutputs >= MaxInvalidOutputs {
		return a.finish(t, StatusInvalidOutput, fmt.Sprintf("no valid %s after %d consecutive invalid attempts", SubmitToolName, *invalidOutputs))
	}
	if err := a.step(t); err != nil {
		return a.saveFailed(t, err)
	}
	return nil
}

// finishReply handles a reply that submits (the marker) or stops calling
// tools: verify-before-stop first, then a successful finish subject to
// stop hooks. handled is false for a reply that only calls tools.
func (a *Agent) finishReply(ctx context.Context, t *trajectory.Trajectory, v *view, msg model.Message) (done *trajectory.Trajectory, handled bool) {
	submitted := strings.Contains(msg.Content, SubmitMarker)
	if !submitted && len(msg.ToolCalls) > 0 {
		return nil, false
	}
	if done, again := a.verifyStop(ctx, t, v, msg); again || done != nil {
		return done, true
	}
	if submitted {
		sub := strings.TrimSpace(strings.Replace(msg.Content, SubmitMarker, "", 1))
		return a.finishSuccess(ctx, t, v, msg, StatusSubmitted, sub, nil), true
	}
	return a.finishSuccess(ctx, t, v, msg, StatusCompleted, msg.Content, nil), true
}

// toolBatchStep runs the reply's tool calls: a mixed ask_user batch is
// rejected, a mutating batch is snapshotted first, the pending calls are
// saved before any tool runs (write-ahead), and every call gets a result.
func (a *Agent) toolBatchStep(ctx context.Context, t *trajectory.Trajectory, v *view, msg model.Message) *trajectory.Trajectory {
	if a.askUserMixed(msg.ToolCalls) {
		if err := a.rejectMixedAskUser(t, v, msg); err != nil {
			return a.saveFailed(t, err)
		}
		return nil
	}
	if done := a.stepSnapshot(t, v, msg); done != nil {
		return done
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
	batch := a.runWorkerBatch(ctx, t, msg.ToolCalls)
	for _, call := range msg.ToolCalls {
		formatError, done := a.toolCallStep(ctx, t, v, call, batch)
		if done != nil {
			return done
		}
		if formatError {
			malformed++
		}
	}
	return a.countFormatErrors(t, malformed, len(msg.ToolCalls))
}

// stepSnapshot snapshots the workspace before a batch that can change
// files. When the snapshot fails no tool runs: every call gets a "not run"
// result and the run ends.
func (a *Agent) stepSnapshot(t *trajectory.Trajectory, v *view, msg model.Message) *trajectory.Trajectory {
	if a.Snapshots == nil || !mutating(msg.ToolCalls) {
		return nil
	}
	err := a.snapshot(t, "step")
	if err == nil {
		return nil
	}
	// No tool runs without its snapshot; record why, with a tool
	// result for every call so the history stays a complete turn.
	reason := fmt.Sprintf("workspace snapshot before step %d failed: %v", t.Steps, err)
	out := "error: not run: " + reason
	for _, call := range msg.ToolCalls {
		a.event("tool.call", map[string]any{"id": call.ID, "name": call.Function.Name, "arguments": eventArguments(call.Function.Arguments)})
		a.recordToolResult(t, v, call, out, true)
	}
	return a.finish(t, StatusError, reason+"; the tool calls were not run")
}

// countFormatErrors updates the consecutive format-error count after a
// batch and ends the run when it reaches the cap.
func (a *Agent) countFormatErrors(t *trajectory.Trajectory, malformed, calls int) *trajectory.Trajectory {
	if malformed == calls {
		t.ConsecutiveFormatErrors++
	} else {
		t.ConsecutiveFormatErrors = 0
	}
	if t.ConsecutiveFormatErrors >= a.maxFormatErrors() {
		return a.finish(t, StatusFormatError, "")
	}
	return nil
}

// callOutcome is the result of one tool call in a batch.
type callOutcome struct {
	res         tools.Result
	out         string
	images      []model.Image
	isError     bool
	formatError bool
	// limitStop is the limit status a delegate child hit; the run ends
	// with it once this call's result is saved.
	limitStop string
}

// toolCallStep runs one call of a batch, appends its result, pops it off
// the pending calls and saves. formatError reports a malformed call.
func (a *Agent) toolCallStep(ctx context.Context, t *trajectory.Trajectory, v *view, call model.ToolCall, batch *workerBatch) (formatError bool, done *trajectory.Trajectory) {
	a.event("tool.call", map[string]any{"id": call.ID, "name": call.Function.Name, "arguments": eventArguments(call.Function.Arguments)})
	o, done := a.executeCall(ctx, t, call, batch)
	if done != nil {
		return false, done
	}
	fields := map[string]any{"id": call.ID, "name": call.Function.Name, "output": o.out, "is_error": o.isError}
	if o.formatError {
		fields["format_error"] = true
	}
	a.noteOutputFile(t, call, o.res, fields)
	a.event("tool.result", fields)
	t.Messages = append(t.Messages, model.Message{Role: "tool", ToolCallID: call.ID, Content: o.out, Images: o.images})
	t.PendingCalls = t.PendingCalls[1:]
	if len(t.PendingCalls) == 0 {
		t.PendingCalls = nil
	}
	a.recordProcesses(t)
	v.add(t, t.Messages[len(t.Messages)-1])
	if a.Verbose {
		fmt.Fprintf(a.Out, "  -> %s\n", indent(o.out))
	}
	if err := a.step(t); err != nil {
		return o.formatError, a.saveFailed(t, err)
	}
	if o.limitStop != "" {
		return o.formatError, a.finish(t, o.limitStop, "")
	}
	return o.formatError, nil
}

// executeCall decides one call's result: a format correction, the ask_user
// question (a lone valid one ends the run NeedsInput), the plan tool, the
// plan-first gate, or the tool itself.
func (a *Agent) executeCall(ctx context.Context, t *trajectory.Trajectory, call model.ToolCall, batch *workerBatch) (callOutcome, *trajectory.Trajectory) {
	var o callOutcome
	reason := a.Tools.FormatError(call)
	var question *trajectory.Question
	if reason == "" && call.Function.Name == tools.AskUserToolName && a.Tools.AskUser {
		q, opts, why := tools.ParseAskUser(call.Function.Arguments)
		question, reason = &trajectory.Question{ToolCallID: call.ID, Question: q, Options: opts}, why
	}
	switch {
	case reason != "":
		t.FormatErrors++
		t.LastFormatError = reason
		t.FormatErrorMessages = append(t.FormatErrorMessages, len(t.Messages))
		o.out, o.isError, o.formatError = a.Tools.FormatCorrection(reason), true, true
	case question != nil:
		if t.QuestionsAsked < a.maxQuestions() {
			// A lone, valid ask_user call ends the run; the call
			// stays pending until a resume answers it. No hook runs.
			return o, a.needsInput(t, question)
		}
		o.out, o.isError = askUserBudgetResult, true
	case call.Function.Name == tools.PlanToolName && a.Tools.Plan:
		o.out, o.isError = a.updatePlan(t, call)
	default:
		if gate := a.planGate(t, call); gate != "" {
			o.out, o.isError = gate, true
			return o, nil
		}
		return a.runCall(ctx, t, call, batch)
	}
	return o, nil
}

// runCall runs a permitted call (or takes its worker batch result),
// records the side channels it may have fed, and handles a delegate child
// that must also stop this run.
func (a *Agent) runCall(ctx context.Context, t *trajectory.Trajectory, call model.ToolCall, batch *workerBatch) (callOutcome, *trajectory.Trajectory) {
	var o callOutcome
	if r, ok := a.batched(batch, call); ok {
		o.res = r
	} else {
		o.res = a.runTool(ctx, t, call)
	}
	a.recordWebFetches()
	a.recordWebSearches()
	a.recordMemorySaves()
	a.recordLSPEvents()
	o.out, o.images, o.isError = o.res.Text, o.res.Images, o.res.IsError
	a.noteVerify(t, call, o.res)
	switch stop := a.takeDelegateStop(); stop {
	case StatusInterrupted, StatusTimeLimit:
		// The delegate call stays pending; a resume closes it
		// as interrupted, like any call cut off mid-run.
		return o, a.finish(t, stop, "")
	case StatusCostLimit, StatusTokenLimit:
		o.limitStop = stop
	}
	return o, nil
}
