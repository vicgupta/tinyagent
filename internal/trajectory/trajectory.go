// Package trajectory records an agent run as JSON.
package trajectory

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/vicgupta/tinyagent/internal/model"
)

type Trajectory struct {
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
}

// Save writes the trajectory to path atomically, so a crash mid-write never
// leaves a truncated file.
func (t *Trajectory) Save(path string) error {
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
