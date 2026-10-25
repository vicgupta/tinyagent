// Package trajectory records an agent run as JSON.
package trajectory

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/vicgupta/tinyagent/internal/model"
)

// SandboxInfo records the OS sandbox that confined bash commands.
type SandboxInfo struct {
	Backend  string   `json:"backend"`
	Writable []string `json:"writable"`
	Network  bool     `json:"network"`
}

// EnvironmentInfo records the non-local environment bash commands ran in.
type EnvironmentInfo struct {
	Name    string `json:"name"`
	Runtime string `json:"runtime,omitempty"`
	Image   string `json:"image,omitempty"`
}

type Trajectory struct {
	// Environment is set when bash and start_process ran outside the host,
	// e.g. in a container.
	Environment *EnvironmentInfo `json:"environment,omitempty"`
	// Sandbox is set when bash and start_process ran in an OS sandbox.
	Sandbox  *SandboxInfo `json:"sandbox,omitempty"`
	ReadOnly bool         `json:"read_only"`
	Task     string       `json:"task"`
	Model    string       `json:"model"`
	// ReasoningEffort is the reasoning_effort sent with every request, if any.
	ReasoningEffort string          `json:"reasoning_effort,omitempty"`
	Messages        []model.Message `json:"messages"`
	Steps           int             `json:"steps"`
	Cost            float64         `json:"cost"`
	InputTokens     int             `json:"input_tokens"`
	OutputTokens    int             `json:"output_tokens"`
	// Prompt-cache totals, included in InputTokens.
	CacheReadTokens  int    `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int    `json:"cache_write_tokens,omitempty"`
	ExitStatus       string `json:"exit_status"`
	Submission       string `json:"submission"`
	// StructuredOutput is the validated result of an --output-schema run
	// that ended Submitted; Submission holds the same JSON as a string.
	StructuredOutput json.RawMessage `json:"structured_output,omitempty"`
	// OutputSchema and OutputSchemaDigest record the --output-schema used.
	OutputSchema       map[string]any `json:"output_schema,omitempty"`
	OutputSchemaDigest string         `json:"output_schema_digest,omitempty"`
	// Compactions records each time the context sent to the model was
	// compacted. Messages always keeps the full, original history.
	Compactions []Compaction `json:"compactions,omitempty"`
	// BackgroundProcesses summarizes background processes still registered
	// when the run ended (all are killed): status and output tail of each.
	BackgroundProcesses string `json:"background_processes,omitempty"`

	// Resume metadata, present on trajectories written by this version. It
	// lets a later invocation validate and continue an unfinished run.
	SchemaVersion int    `json:"schema_version,omitempty"`
	Root          string `json:"root,omitempty"` // canonical working directory
	// SystemPrompt is the effective system prompt (with {{cwd}}/{{os}}
	// substituted and AGENTS.md appended) the run used.
	SystemPrompt string `json:"system_prompt,omitempty"`
	// SkipAgentsMD records whether AGENTS.md was excluded from the prompt.
	SkipAgentsMD          bool     `json:"skip_agents_md"`
	Tools                 []string `json:"tools,omitempty"`          // tool names, in spec order
	ToolSignature         string   `json:"tool_signature,omitempty"` // versioned spec/semantics fingerprint
	MCPEnabled            bool     `json:"mcp_enabled,omitempty"`
	ExternalTools         []string `json:"external_tools,omitempty"`
	ExternalToolSignature string   `json:"external_tool_signature,omitempty"`
	// FormatErrors counts malformed tool calls over the whole run, and
	// ConsecutiveFormatErrors the current run of steps whose calls were all
	// malformed; it is restored on resume. FormatErrorMessages lists the
	// indexes in Messages of tool results that were format-error corrections.
	FormatErrors            int    `json:"format_errors,omitempty"`
	ConsecutiveFormatErrors int    `json:"consecutive_format_errors,omitempty"`
	FormatErrorMessages     []int  `json:"format_error_messages,omitempty"`
	LastFormatError         string `json:"last_format_error,omitempty"`
	// Resume records the checkpoint this run continued, when it did.
	Resume *ResumeInfo `json:"resume,omitempty"`

	// PendingCalls (schema 2) is the write-ahead record of the last assistant
	// turn's tool calls that have started or are about to start but have no
	// result yet. It is empty at every completed step boundary.
	PendingCalls []PendingCall `json:"pending_calls,omitempty"`
	// Processes (schema 2) lists the background processes registered at the
	// last save. None survive the invocation that started them.
	Processes []ProcessInfo `json:"processes,omitempty"`
	// PlanEnabled (schema 2) records whether the opt-in update_plan tool was
	// offered. A resume must use the same setting.
	PlanEnabled bool         `json:"plan_enabled,omitempty"`
	Plan        *Plan        `json:"plan,omitempty"`
	PlanHistory []PlanChange `json:"plan_history,omitempty"`
	// ImagesEnabled (schema 2) records whether the opt-in --images setting
	// (view_image and MCP image pass-through) was on. A resume must use the
	// same setting. Image bytes are stored inline, base64, in Messages.
	ImagesEnabled bool `json:"images_enabled,omitempty"`
	// SkillsEnabled records whether Agent Skills discovery was on. A resume
	// rediscovers skills and must rebuild the same catalog. Older
	// checkpoints, without the field, resume with skills off.
	SkillsEnabled bool `json:"skills_enabled,omitempty"`
	// Skills is the discovered catalog (not the skill bodies).
	Skills []SkillInfo `json:"skills,omitempty"`
}

// SkillInfo identifies one discovered skill.
type SkillInfo struct {
	Name  string `json:"name"`
	Scope string `json:"scope"`
	Path  string `json:"path"`
}

// PendingCall identifies a tool call without a recorded result.
type PendingCall struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ProcessInfo describes a background process registered by start_process.
type ProcessInfo struct {
	ID      string `json:"id"`
	PID     int    `json:"pid,omitempty"`
	Command string `json:"command"`
}

// Plan is the model's persistent task plan, kept outside Messages so
// compaction never removes it.
type Plan struct {
	Version     int        `json:"version"`
	Items       []PlanItem `json:"items"`
	Note        string     `json:"note,omitempty"`
	UpdatedStep int        `json:"updated_step"`
}

// PlanItem is one plan entry.
type PlanItem struct {
	ID     string `json:"id"`
	Text   string `json:"text"`
	Status string `json:"status"` // pending, in_progress, done, or blocked
}

// PlanChange audits one update_plan call.
type PlanChange struct {
	Step    int    `json:"step"`
	Version int    `json:"version"`
	Diff    string `json:"diff"`
}

// ResumeInfo records the checkpoint a run continued, so the previous
// termination stays distinguishable from the active result.
type ResumeInfo struct {
	Source     string      `json:"source"`      // path of the input checkpoint
	ExitStatus string      `json:"exit_status"` // previous termination status
	Steps      int         `json:"steps"`       // steps at the previous termination
	Submission string      `json:"submission,omitempty"`
	Message    int         `json:"message"` // continuation notice index in Messages
	Previous   *ResumeInfo `json:"previous,omitempty"`
}

// SchemaVersion is the resume-metadata version written by this build.
// Version 1 checkpoints (no pending calls, processes, or plan) still load.
const SchemaVersion = 2

// Compaction describes one compaction of the model's view of the history.
// Indexes refer to Trajectory.Messages.
type Compaction struct {
	Step         int    `json:"step"`   // steps completed when it ran
	Reason       string `json:"reason"` // "proactive" or "overflow"
	Stage        string `json:"stage"`  // "elide" or "summarize"
	TokensBefore int    `json:"tokens_before"`
	TokensAfter  int    `json:"tokens_after"`
	// Elided lists messages whose tool output was replaced by a placeholder.
	Elided []int `json:"elided,omitempty"`
	// Summarized lists messages replaced by Summary.
	Summarized []int   `json:"summarized,omitempty"`
	Summary    string  `json:"summary,omitempty"`
	USD        float64 `json:"usd,omitempty"` // cost of the summarization call
	// View is what the model is sent next: indexes into Messages, with -1
	// for the summary message.
	View []int `json:"view"`
	// PlanVersion is the plan version pinned in the view, when enabled.
	PlanVersion int `json:"plan_version,omitempty"`
}

// Save replaces path with a complete private file using a same-directory
// temporary file and rename. The file and its directory are fsynced so a
// completed save survives power loss on filesystems that honor fsync.
func (t *Trajectory) Save(path string) error {
	return t.save(path, false)
}

// SaveNew publishes the starting checkpoint without replacing any existing
// destination, including symlinks. Linking the completed temporary file makes
// publication atomic and exclusive on filesystems supporting hard links.
func (t *Trajectory) SaveNew(path string) error {
	return t.save(path, true)
}

func (t *Trajectory) save(path string, exclusive bool) error {
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tinyagent-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if exclusive {
		err = os.Link(tmp.Name(), path)
	} else {
		err = os.Rename(tmp.Name(), path)
	}
	if err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

// syncDir makes a rename or link in dir durable. Windows cannot fsync a
// directory; there the rename is left to the filesystem.
func syncDir(dir string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := d.Sync(); err != nil && !errors.Is(err, syscall.EINVAL) && !errors.Is(err, syscall.ENOTSUP) {
		return err
	}
	return nil
}
