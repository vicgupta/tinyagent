// Package trajectory records an agent run as JSON.
package trajectory

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/vicgupta/tinyagent/internal/model"
)

type Trajectory struct {
	ReadOnly     bool            `json:"read_only"`
	Task         string          `json:"task"`
	Model        string          `json:"model"`
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
	SkipAgentsMD  bool     `json:"skip_agents_md"`
	Tools         []string `json:"tools,omitempty"`          // tool names, in spec order
	ToolSignature string   `json:"tool_signature,omitempty"` // versioned spec/semantics fingerprint
	// Resume records the checkpoint this run continued, when it did.
	Resume *ResumeInfo `json:"resume,omitempty"`
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
const SchemaVersion = 1

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
}

// Save replaces path with a complete private file using a same-directory
// temporary file and rename. This does not guarantee power-loss durability.
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
	if err := tmp.Close(); err != nil {
		return err
	}
	if exclusive {
		return os.Link(tmp.Name(), path)
	}
	return os.Rename(tmp.Name(), path)
}
