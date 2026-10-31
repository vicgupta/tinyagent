package agent

import (
	"errors"
	"fmt"
	"io"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/hooks"
	"github.com/vicgupta/tinyagent/internal/lockdown"
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/outschema"
	"github.com/vicgupta/tinyagent/internal/permissions"
	"github.com/vicgupta/tinyagent/internal/routing"
	"github.com/vicgupta/tinyagent/internal/tools"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// Options are the settings that define a run. New checks them together and
// builds the Agent from them; each field has the meaning of the Agent field
// with the same name. Per-run sinks wired after construction (OnStep,
// OnEvent, PermissionApprover, Plugins, Memory, ...) are still set on the
// returned Agent.
type Options struct {
	// Identity: what runs and how it is recorded.
	Model           model.Model
	ModelName       string
	FallbackModels  []string
	ReasoningEffort string
	OpenAIAPI       string
	Profile         string
	ProviderAPI     string
	ProviderBaseURL string
	Routes          []routing.Route
	Helpers         map[string]model.Model

	// Tools and the limits, policy and hooks around them.
	Tools         *tools.Executor
	Config        config.AgentConfig
	RedactSecrets *trajectory.RedactSecretsInfo
	Hooks         *hooks.Runner
	Policy        *permissions.Policy
	Lockdown      *lockdown.State
	Subagents     *Subagents
	Workers       *Workers
	OutputSchema  *outschema.Schema
	Snapshots     Snapshotter

	// Output.
	Out      io.Writer
	Err      io.Writer
	Verbose  bool
	Streamed bool

	// Context and instructions.
	SkipAgentsMD   bool
	ScopedAgentsMD bool
	NoCompact      bool
	CompactFirst   bool
	TaskImages     []model.Image
	Worktree       *trajectory.WorktreeInfo
	Review         *trajectory.ReviewInfo

	// Resume: what this run continues.
	Resume       *trajectory.Trajectory
	ResumeSource string
	FollowUp     string
	Continue     bool
}

// New validates o and returns the Agent it describes. An error names the
// invalid combination; nothing is started.
func New(o Options) (*Agent, error) {
	if err := o.validate(); err != nil {
		return nil, fmt.Errorf("agent: %w", err)
	}
	return &Agent{
		Model:           o.Model,
		ModelName:       o.ModelName,
		FallbackModels:  o.FallbackModels,
		ReasoningEffort: o.ReasoningEffort,
		OpenAIAPI:       o.OpenAIAPI,
		Profile:         o.Profile,
		ProviderAPI:     o.ProviderAPI,
		ProviderBaseURL: o.ProviderBaseURL,
		Routes:          o.Routes,
		Helpers:         o.Helpers,
		Tools:           o.Tools,
		Config:          o.Config,
		RedactSecrets:   o.RedactSecrets,
		Hooks:           o.Hooks,
		Policy:          o.Policy,
		Lockdown:        o.Lockdown,
		Subagents:       o.Subagents,
		Workers:         o.Workers,
		OutputSchema:    o.OutputSchema,
		Snapshots:       o.Snapshots,
		Out:             o.Out,
		Err:             o.Err,
		Verbose:         o.Verbose,
		Streamed:        o.Streamed,
		SkipAgentsMD:    o.SkipAgentsMD,
		ScopedAgentsMD:  o.ScopedAgentsMD,
		NoCompact:       o.NoCompact,
		CompactFirst:    o.CompactFirst,
		TaskImages:      o.TaskImages,
		Worktree:        o.Worktree,
		Review:          o.Review,
		Resume:          o.Resume,
		ResumeSource:    o.ResumeSource,
		FollowUp:        o.FollowUp,
		Continue:        o.Continue,
	}, nil
}

// ErrMCPResume rejects resuming a loaded checkpoint whose run used MCP or
// plugin tools. Run's checkpoint validation refuses the same checkpoints;
// New reports it before anything is built.
var ErrMCPResume = errors.New("MCP-enabled runs cannot be resumed; start a fresh task")

func (o *Options) validate() error {
	if o.Model == nil {
		return errors.New("Model is required")
	}
	if o.Tools == nil {
		return errors.New("Tools is required")
	}
	if (o.Subagents != nil) != o.Tools.Delegate {
		return fmt.Errorf("Subagents (set: %t) and Tools.Delegate (%t) must agree", o.Subagents != nil, o.Tools.Delegate)
	}
	if (o.Workers != nil) != o.Tools.Workers {
		return fmt.Errorf("Workers (set: %t) and Tools.Workers (%t) must agree", o.Workers != nil, o.Tools.Workers)
	}
	if err := o.validateLimits(); err != nil {
		return err
	}
	if err := o.validateOutputSchema(); err != nil {
		return err
	}
	return o.validateResume()
}

func (o *Options) validateLimits() error {
	c := o.Config
	switch {
	case c.StepLimit < 0:
		return fmt.Errorf("step limit must be >= 0, got %d", c.StepLimit)
	case c.CostLimit < 0:
		return fmt.Errorf("cost limit must be >= 0, got %v", c.CostLimit)
	case c.WallTimeSec < 0:
		return fmt.Errorf("time limit must be >= 0, got %d", c.WallTimeSec)
	case c.TokenLimit < 0:
		return fmt.Errorf("token limit must be >= 0, got %d", c.TokenLimit)
	}
	// A session turn's limits apply to that turn, so they are offset by what
	// the conversation used before it: a positive limit must exceed it.
	if t := o.Resume; t != nil && o.Continue {
		switch {
		case c.StepLimit > 0 && c.StepLimit <= t.Steps:
			return fmt.Errorf("continued step limit %d is not offset by the %d steps already used", c.StepLimit, t.Steps)
		case c.CostLimit > 0 && c.CostLimit <= t.Cost:
			return fmt.Errorf("continued cost limit %v is not offset by the $%v already spent", c.CostLimit, t.Cost)
		case c.TokenLimit > 0 && c.TokenLimit <= t.InputTokens+t.OutputTokens:
			return fmt.Errorf("continued token limit %d is not offset by the %d tokens already used", c.TokenLimit, t.InputTokens+t.OutputTokens)
		}
	}
	return nil
}

func (o *Options) validateOutputSchema() error {
	s := o.OutputSchema
	if s == nil {
		return nil
	}
	switch {
	case s.Params == nil || s.Digest == "":
		return errors.New("OutputSchema needs Params and Digest (load it with outschema.Load)")
	case o.Resume != nil:
		return errors.New("runs with an output schema cannot be resumed")
	case o.Tools.AskUser:
		return errors.New("ask_user can't be combined with an output schema: those runs can't be resumed to answer a question")
	}
	for _, spec := range o.Tools.ExternalSpecs {
		if spec.Function.Name == SubmitToolName {
			return fmt.Errorf("external tool %q collides with the output schema's submit tool", SubmitToolName)
		}
	}
	return nil
}

func (o *Options) validateResume() error {
	t := o.Resume
	if t == nil {
		switch {
		case o.FollowUp != "":
			return errors.New("FollowUp needs Resume")
		case o.Continue:
			return errors.New("Continue needs Resume")
		case o.ResumeSource != "":
			return errors.New("ResumeSource needs Resume")
		}
		return nil
	}
	// A loaded checkpoint that used MCP or plugin tools can't be resumed (its
	// servers' state is gone); a Continue turn keeps its live session's tools.
	if !o.Continue && (t.MCPEnabled || len(t.ExternalTools) > 0) {
		return ErrMCPResume
	}
	_, _, err := resumeMode(t, o.FollowUp, o.Continue, len(o.TaskImages))
	return err
}

// resumeMode classifies how a run continues t: finished (a follow-up
// continues a finished checkpoint, or any turn of a Continue session that
// isn't waiting for an answer) or asked (FollowUp answers t's ask_user
// question). The error explains a FollowUp, Continue or TaskImages
// combination that can't continue t.
func resumeMode(t *trajectory.Trajectory, followUp string, cont bool, images int) (finished, asked bool, err error) {
	finished = trajectory.Finished(t.ExitStatus) || (cont && t.ExitStatus != StatusNeedsInput)
	asked = t.ExitStatus == StatusNeedsInput
	switch {
	case asked && followUp == "":
		err = errors.New("checkpoint is waiting for an answer to its question; answer it with a follow-up task")
	case asked && images > 0:
		err = errors.New("image attachments can't be added to an answer")
	case finished && followUp == "":
		err = errors.New("checkpoint already finished; continue it with a follow-up task")
	case !finished && !asked && followUp != "":
		err = errors.New("a follow-up task can only continue a finished checkpoint")
	case !finished && images > 0:
		err = errors.New("image attachments need a follow-up task")
	}
	return finished, asked, err
}
