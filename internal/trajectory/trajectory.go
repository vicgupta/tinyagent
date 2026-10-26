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
	// AllowedDomains is set in allowlist mode: commands could reach only
	// these hosts, through tinyagent's proxy.
	AllowedDomains []string `json:"allowed_domains,omitempty"`
}

// WebFetchInfo records the web_fetch allowlist of one invocation.
type WebFetchInfo struct {
	AllowedDomains []string `json:"allowed_domains"`
}

// NetworkDenial is one request the sandbox allowlist proxy refused.
type NetworkDenial struct {
	Host   string `json:"host"`
	Port   int    `json:"port"`
	Reason string `json:"reason"`
}

// EnvironmentInfo records the non-local environment bash commands ran in.
type EnvironmentInfo struct {
	Name    string `json:"name"`
	Runtime string `json:"runtime,omitempty"`
	Image   string `json:"image,omitempty"`
}

// CommandEnvInfo records the command_env policy bash and start_process
// commands ran with: names only, never values.
type CommandEnvInfo struct {
	Inherit        string   `json:"inherit"`
	ExcludeSecrets bool     `json:"exclude_secrets"`
	Passed         []string `json:"passed"`
}

// AdditionalDir is one additional directory: an absolute path and its
// effective mode, "read" or "write".
type AdditionalDir struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
}

// WorktreeInfo records the Git worktree a --worktree run worked in: its
// path, branch, the commit it was branched from and the main repository.
type WorktreeInfo struct {
	Path       string `json:"path"`
	Branch     string `json:"branch"`
	BaseCommit string `json:"base_commit"`
	RepoRoot   string `json:"repo_root"`
}

// FileAccessInfo records the file_access rules the built-in file tools
// enforced.
type FileAccessInfo struct {
	DenyRead  []string `json:"deny_read"`
	DenyWrite []string `json:"deny_write"`
}

type Trajectory struct {
	// Environment is set when bash and start_process ran outside the host,
	// e.g. in a container.
	Environment *EnvironmentInfo `json:"environment,omitempty"`
	// Sandbox is set when bash and start_process ran in an OS sandbox.
	Sandbox *SandboxInfo `json:"sandbox,omitempty"`
	// CommandEnv is set when a command_env policy filtered the environment
	// of bash and start_process; it records this invocation's policy.
	CommandEnv *CommandEnvInfo `json:"command_env,omitempty"`
	// FileAccess is set when file_access rules restricted the file tools;
	// it records this invocation's rules.
	FileAccess *FileAccessInfo `json:"file_access,omitempty"`
	// Worktree is set when the run was started with --worktree: Root is
	// that worktree (or its subdirectory).
	Worktree *WorktreeInfo `json:"worktree,omitempty"`
	// AdditionalDirs lists the directories outside Root the file tools could
	// reach in this invocation, with their effective mode.
	AdditionalDirs []AdditionalDir `json:"additional_dirs,omitempty"`
	// SandboxNetworkDenials lists the requests the allowlist proxy refused.
	SandboxNetworkDenials []NetworkDenial `json:"sandbox_network_denials,omitempty"`
	ReadOnly              bool            `json:"read_only"`
	Task                  string          `json:"task"`
	Model                 string          `json:"model"`
	// ReasoningEffort is the reasoning_effort sent with every request, if any.
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	// OpenAIAPI is the openai provider's resolved transport, "responses" or
	// "chat"; empty for other providers and older trajectories.
	OpenAIAPI string `json:"openai_api,omitempty"`
	// ProviderAPI and ProviderBaseURL describe a config-defined provider
	// (its id is the model spec's prefix); empty for built-in providers.
	// Keys and header values are never recorded.
	ProviderAPI     string `json:"provider_api,omitempty"`
	ProviderBaseURL string `json:"provider_base_url,omitempty"`
	// FallbackModels is the fallback chain of the latest invocation
	// (fallback_models, --fallback-model); empty without one.
	FallbackModels []string `json:"fallback_models,omitempty"`
	// ModelSwitches records each move down the fallback chain, in order.
	ModelSwitches []ModelSwitch `json:"model_switches,omitempty"`
	// ActiveModel is the model that served the latest request when a
	// fallback chain is configured; Model stays the primary. Assistant
	// messages then carry the serving model in Extra["model"].
	ActiveModel  string          `json:"active_model,omitempty"`
	Messages     []model.Message `json:"messages"`
	Steps        int             `json:"steps"`
	Cost         float64         `json:"cost"`
	InputTokens  int             `json:"input_tokens"`
	OutputTokens int             `json:"output_tokens"`
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
	// Truncations counts responses cut off at the output-token limit over
	// the whole run, and ConsecutiveTruncations the current run of them; it
	// is restored on resume. MaxOutputTokens is the effective per-request
	// output budget (0 = provider default).
	Truncations            int `json:"truncations,omitempty"`
	ConsecutiveTruncations int `json:"consecutive_truncations,omitempty"`
	MaxOutputTokens        int `json:"max_output_tokens,omitempty"`
	// StopContinuations counts the times stop hooks sent the model back to
	// work; it is restored on resume. StopReason is the last stop-hook
	// feedback of a VerificationFailed run.
	StopContinuations int    `json:"stop_continuations,omitempty"`
	StopReason        string `json:"stop_reason,omitempty"`
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
	// ApplyPatchEnabled (schema 2) records whether the opt-in apply_patch
	// tool was enabled. A resume must use the same setting.
	ApplyPatchEnabled bool `json:"apply_patch_enabled,omitempty"`
	// SubagentsEnabled (schema 2) records whether the opt-in delegate tool
	// was offered. A resume must use the same setting.
	SubagentsEnabled bool `json:"subagents_enabled,omitempty"`
	// WebFetchEnabled (schema 2) records whether the opt-in web_fetch tool
	// was offered. A resume must use the same setting.
	WebFetchEnabled bool `json:"web_fetch_enabled,omitempty"`
	// WebFetch is set when web_fetch was offered; it records this
	// invocation's allowlist.
	WebFetch *WebFetchInfo `json:"web_fetch,omitempty"`
	// LSPEnabled (schema 2) records whether language servers (the lsp
	// tool and post-edit diagnostics) were on. A resume must use the same
	// setting.
	LSPEnabled bool `json:"lsp_enabled,omitempty"`
	// LSPServers names this invocation's configured language servers.
	LSPServers []string `json:"lsp_servers,omitempty"`
	// ScopedAgentsMDEnabled (schema 2) records whether AGENTS.md files
	// below Root were delivered with file tool results (--scoped-agents-md).
	ScopedAgentsMDEnabled bool `json:"scoped_agents_md_enabled,omitempty"`
	// ScopedAgentsMD lists the scoped AGENTS.md paths (relative to Root,
	// slash separated) whose delivering tool result is still in context.
	ScopedAgentsMD []string `json:"scoped_agents_md,omitempty"`
	// ScopedAgentsMDLoads records every scoped AGENTS.md delivery, in order.
	ScopedAgentsMDLoads []ScopedAgentsMDLoad `json:"scoped_agents_md_loads,omitempty"`
	// ScopedAgentsMDSince is when the run first started with scoped
	// AGENTS.md on (RFC 3339): files modified after it are not delivered.
	ScopedAgentsMDSince string `json:"scoped_agents_md_since,omitempty"`
	// Subagents records each delegate call's child run, in call order,
	// with its full history. Their tokens and cost are included in the
	// run totals; their model calls are counted in SubagentSteps, not
	// Steps.
	Subagents     []Subagent `json:"subagents,omitempty"`
	SubagentSteps int        `json:"subagent_steps,omitempty"`
	// ToolOutputFiles (schema 2) lists the over-limit tool results whose
	// full text was saved with tool_output_files on.
	ToolOutputFiles []ToolOutputFile `json:"tool_output_files,omitempty"`
	// SkillsEnabled records whether Agent Skills discovery was on. A resume
	// rediscovers skills and must rebuild the same catalog. Older
	// checkpoints, without the field, resume with skills off.
	SkillsEnabled bool `json:"skills_enabled,omitempty"`
	// Skills is the discovered catalog (not the skill bodies).
	Skills []SkillInfo `json:"skills,omitempty"`
	// SnapshotsEnabled records whether the last invocation took opt-in
	// workspace snapshots (--snapshots). It is per invocation: a resume may
	// turn snapshots on or off.
	SnapshotsEnabled bool `json:"snapshots_enabled,omitempty"`
	// Snapshots indexes the workspace snapshots taken for this run, oldest
	// first, across resumes and rewinds. The file contents live in the
	// snapshot store named by each entry, never in the trajectory.
	Snapshots []Snapshot `json:"snapshots,omitempty"`
}

// Snapshot is the trajectory's record of one workspace snapshot.
type Snapshot struct {
	ID string `json:"id"`
	// Step is the step count when it was taken: 0 for the first baseline,
	// N for the snapshot taken before step N's tool batch ran.
	Step int `json:"step"`
	// Kind is "baseline", "step", "final" or "pre-rewind".
	Kind string `json:"kind"`
	Time string `json:"time"` // RFC 3339
	// Files is how many files and symlinks were captured, Bytes their total
	// size.
	Files int   `json:"files"`
	Bytes int64 `json:"bytes"`
	// SkippedIgnored counts gitignored paths left out (an ignored directory
	// counts once); SkippedLarge counts files over max_file_bytes, and
	// LargePaths lists the first few of them.
	SkippedIgnored int      `json:"skipped_ignored,omitempty"`
	SkippedLarge   int      `json:"skipped_large,omitempty"`
	LargePaths     []string `json:"large_paths,omitempty"`
	// Store is the snapshot store directory.
	Store string `json:"store"`
}

// SkillInfo identifies one discovered skill.
type SkillInfo struct {
	Name  string `json:"name"`
	Scope string `json:"scope"`
	Path  string `json:"path"`
}

// ToolOutputFile records where one tool result's full text was saved.
type ToolOutputFile struct {
	ToolCallID  string `json:"tool_call_id"`
	OutputFile  string `json:"output_file"` // relative to the run's root, slash separated
	OutputBytes int    `json:"output_bytes"`
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
	Source     string `json:"source"`      // path of the input checkpoint
	ExitStatus string `json:"exit_status"` // previous termination status
	Steps      int    `json:"steps"`       // steps at the previous termination
	Submission string `json:"submission,omitempty"`
	Message    int    `json:"message"` // continuation notice index in Messages
	// FollowUp marks a boundary where a finished (Submitted or Completed)
	// run was continued with a new user instruction. Its message starts with
	// FollowUpNotice instead of ContinuationNotice.
	FollowUp bool        `json:"follow_up,omitempty"`
	Previous *ResumeInfo `json:"previous,omitempty"`
}

// SchemaVersion is the resume-metadata version written by this build.
// Version 1 checkpoints (no pending calls, processes, or plan) still load.
const SchemaVersion = 2

// ModelSwitch is one fallback switch: at step Step (the step being
// requested), the request failed on From with Reason after every retry and
// was re-sent to To.
type ModelSwitch struct {
	Step   int    `json:"step"`
	From   string `json:"from"`
	To     string `json:"to"`
	Reason string `json:"reason"`
}

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

// Subagent is one delegate child run.
type Subagent struct {
	ID         string `json:"id"`
	ToolCallID string `json:"tool_call_id"`
	Task       string `json:"task"`
	Label      string `json:"label,omitempty"`
	Model      string `json:"model"`
	// ParentStep is the parent's step count when the call ran.
	ParentStep       int             `json:"parent_step"`
	Status           string          `json:"status"`
	Steps            int             `json:"steps"`
	InputTokens      int             `json:"input_tokens"`
	OutputTokens     int             `json:"output_tokens"`
	CacheReadTokens  int             `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int             `json:"cache_write_tokens,omitempty"`
	Cost             float64         `json:"cost"`
	Report           string          `json:"report,omitempty"`
	Messages         []model.Message `json:"messages"`
}

// ScopedAgentsMDLoad is one scoped AGENTS.md delivered with a tool result.
type ScopedAgentsMDLoad struct {
	// Path is the file, relative to Root and slash separated.
	Path string `json:"path"`
	// ScopeDir is its directory with a trailing slash, e.g. "pkg/a/".
	ScopeDir string `json:"scope_dir"`
	// Bytes is the file's size on disk.
	Bytes     int  `json:"bytes"`
	Truncated bool `json:"truncated,omitempty"`
	// Delivered is the number of content bytes appended (after the cap).
	Delivered  int    `json:"delivered_bytes"`
	Tool       string `json:"tool"`
	ToolCallID string `json:"tool_call_id"`
	Step       int    `json:"step"`
	// Message is the index in Messages of the tool result carrying it.
	Message int `json:"message"`
}
