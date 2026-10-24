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
	"github.com/vicgupta/tinyagent/internal/model"
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
)

// maxAgentsMD caps how much of AGENTS.md goes into the system prompt.
const maxAgentsMD = 32 << 10

type Agent struct {
	Model     model.Model
	ModelName string
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
	// NoCompact turns off context compaction: the full history is always
	// sent, and a context-overflow error ends the run.
	NoCompact bool
	// OnStep is called after every step, e.g. to save the trajectory. A
	// non-nil error stops the run.
	OnStep func(*trajectory.Trajectory) error
	// Resume, when non-nil, continues that trajectory instead of starting a
	// fresh run: its messages, accounting totals, and steps are the starting
	// state, and a continuation notice is appended before the first request.
	Resume       *trajectory.Trajectory
	ResumeSource string // audit-only input path for resume provenance
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
		source := a.ResumeSource
		if source == "" {
			source = "memory"
		}
		t.Resume = &trajectory.ResumeInfo{
			Source: source, ExitStatus: t.ExitStatus, Submission: t.Submission,
			Steps: t.Steps, Message: len(t.Messages), Previous: t.Resume,
		}
		t.ExitStatus, t.Submission = "", ""
		t.Messages = append(t.Messages, model.Message{Role: "user", Content: resumeNotice})
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
			SchemaVersion:         trajectory.SchemaVersion,
			Root:                  a.Tools.Root,
			SystemPrompt:          system,
			SkipAgentsMD:          a.SkipAgentsMD,
			Tools:                 toolNames(a.Tools.Catalog()),
			ToolSignature:         ToolSignature(a.Tools.ReadOnly),
			MCPEnabled:            len(a.Tools.ExternalSpecs) > 0,
			ExternalTools:         externalToolNames(a.Tools.ExternalSpecs),
			ExternalToolSignature: externalToolSignature(a.Tools.ExternalSpecs),
			Messages: []model.Message{
				{Role: "system", Content: system},
				{Role: "user", Content: task},
			},
		}
	}
	specs := a.Tools.Catalog()
	v := newView(t.Messages)

	for {
		if status, ok := a.checkLimits(ctx, t); !ok {
			return a.finish(t, status, "")
		}
		if a.needsCompaction(v) {
			if err := a.compact(ctx, t, v, "proactive"); err != nil {
				return a.compactFailed(ctx, t, err)
			}
			if status, ok := a.checkLimits(ctx, t); !ok {
				return a.finish(t, status, "")
			}
		}

		msg, cost, err := a.Model.Query(ctx, v.msgs, specs)
		if err != nil && !a.NoCompact && model.IsContextOverflow(err) && ctx.Err() == nil {
			if cerr := a.compact(ctx, t, v, "overflow"); cerr != nil {
				return a.compactFailed(ctx, t, fmt.Errorf("%w: %v", cerr, err))
			}
			if status, ok := a.checkLimits(ctx, t); !ok {
				return a.finish(t, status, "")
			}
			msg, cost, err = a.Model.Query(ctx, v.msgs, specs)
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

		if strings.Contains(msg.Content, SubmitMarker) {
			sub := strings.TrimSpace(strings.Replace(msg.Content, SubmitMarker, "", 1))
			return a.finish(t, StatusSubmitted, sub)
		}
		if len(msg.ToolCalls) == 0 {
			return a.finish(t, StatusCompleted, msg.Content)
		}

		for _, call := range msg.ToolCalls {
			out := a.Tools.Run(ctx, call)
			t.Messages = append(t.Messages, model.Message{Role: "tool", ToolCallID: call.ID, Content: out})
			v.add(t, t.Messages[len(t.Messages)-1])
			if a.Verbose {
				fmt.Fprintf(a.Out, "  -> %s\n", indent(out))
			}
		}
		if err := a.step(t); err != nil {
			return a.saveFailed(t, err)
		}
	}
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
		system += agentsMD(a.Tools.Root, a.Err, a.Out, a.Verbose)
	}
	if a.Tools.ReadOnly {
		system += "\n\n" + readOnlyNotice
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
