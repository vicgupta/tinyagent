package agent

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/tools"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// ToolSignature fingerprints the definitions plus an execution contract
// version. Bump the prefix when tool semantics change without a spec change.
func ToolSignature(readOnly ...bool) string {
	readonly := len(readOnly) > 0 && readOnly[0]
	data, _ := json.Marshal(tools.Specs(readonly))
	prefix := "tools-v1:"
	if readonly {
		prefix = "tools-read-only-v1:"
	}
	return fmt.Sprintf("%s%x", prefix, sha256.Sum256(data))
}

// ValidateResume runs before provider setup, using only the explicit execution
// root and current config. Checkpoint paths and credentials are never executed
// or loaded from metadata.
func ValidateResume(t *trajectory.Trajectory, cfg config.AgentConfig, root string) error {
	if err := t.Validate(); err != nil {
		return err
	}
	if root != t.Root {
		return errors.New("--cwd does not match the checkpoint's canonical root")
	}
	if t.ToolSignature != ToolSignature(t.ReadOnly) {
		return errors.New("tool compatibility has changed; start a fresh task")
	}
	names := toolNames(tools.Specs(t.ReadOnly))
	if len(names) != len(t.Tools) {
		return errors.New("tool set has changed; start a fresh task")
	}
	for i, name := range names {
		if t.Tools[i] != name {
			return errors.New("tool set has changed; start a fresh task")
		}
	}
	system := strings.NewReplacer("{{cwd}}", root, "{{os}}", runtime.GOOS).Replace(cfg.SystemPrompt)
	if !t.SkipAgentsMD {
		section, err := loadAgentsMD(root, io.Discard, false)
		if err != nil {
			return errors.New("cannot reconstruct AGENTS.md instructions; inspect permissions and start a fresh task if instructions changed")
		}
		system += section
	}
	if t.ReadOnly {
		system += "\n\n" + readOnlyNotice
	}
	if system != t.SystemPrompt {
		return errors.New("effective system prompt or AGENTS.md has changed; start a fresh task")
	}
	return nil
}
