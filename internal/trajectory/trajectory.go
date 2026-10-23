// Package trajectory records an agent run as JSON.
package trajectory

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/vicgupta/tinyagent/internal/model"
)

type Trajectory struct {
	Task       string          `json:"task"`
	Model      string          `json:"model"`
	Messages   []model.Message `json:"messages"`
	Steps      int             `json:"steps"`
	Cost       float64         `json:"cost"`
	ExitStatus string          `json:"exit_status"`
	Submission string          `json:"submission"`
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
