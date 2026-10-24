// Package agent runs the step loop: query the model, execute tool calls, repeat.
package agent

import (
	"context"
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
	// OnStep is called after every step, e.g. to save the trajectory.
	OnStep func(*trajectory.Trajectory)
}

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

	system := strings.NewReplacer("{{cwd}}", a.Tools.Root, "{{os}}", runtime.GOOS).Replace(a.Config.SystemPrompt)
	if !a.SkipAgentsMD {
		system += a.agentsMD()
	}
	t := &trajectory.Trajectory{
		Task:  task,
		Model: a.ModelName,
		Messages: []model.Message{
			{Role: "system", Content: system},
			{Role: "user", Content: task},
		},
	}
	specs := tools.Specs()

	for {
		if status, ok := a.checkLimits(ctx, t); !ok {
			return a.finish(t, status, "")
		}

		msg, cost, err := a.Model.Query(ctx, t.Messages, specs)
		if err != nil {
			if status, ok := ctxStatus(ctx); ok {
				return a.finish(t, status, "")
			}
			return a.finish(t, StatusError, err.Error())
		}
		t.Steps++
		t.Cost += cost.USD
		t.InputTokens += cost.InputTokens
		t.OutputTokens += cost.OutputTokens
		t.Messages = append(t.Messages, msg)
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
			if a.Verbose {
				fmt.Fprintf(a.Out, "  -> %s\n", indent(out))
			}
		}
		a.step(t)
	}
}

// agentsMD returns the project's AGENTS.md as a system prompt section, or ""
// when there is none.
func (a *Agent) agentsMD() string {
	path := findAgentsMD(a.Tools.Root)
	if path == "" {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintln(a.Err, "warning: reading AGENTS.md:", err)
		}
		return ""
	}
	if a.Verbose {
		fmt.Fprintf(a.Out, "loaded AGENTS.md from %s (%d bytes)\n", path, len(b))
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
	return "\n\n## Project instructions (AGENTS.md)\n\n" + s
}

// findAgentsMD returns the nearest AGENTS.md at or above root, stopping at the
// repository root: the nearest directory containing .git. Without a .git above
// root, only root itself is checked. It returns "" when there is no such file.
func findAgentsMD(root string) string {
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
			return path
		}
		if dir == stop {
			return ""
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

func (a *Agent) finish(t *trajectory.Trajectory, status, submission string) *trajectory.Trajectory {
	t.ExitStatus = status
	t.Submission = submission
	a.step(t)
	return t
}

func (a *Agent) step(t *trajectory.Trajectory) {
	if a.OnStep != nil {
		a.OnStep(t)
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
