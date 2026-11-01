package agent

import (
	"errors"
	"io"
	"runtime"
	"strings"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/resume"
	"github.com/vicgupta/tinyagent/internal/skills"
	"github.com/vicgupta/tinyagent/internal/tools"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// ToolSignature fingerprints the definitions plus an execution contract
// version (see tools.Selection.Signature). The opt-in tools are off, so
// checkpoints from earlier builds still resume.
func ToolSignature(readOnly ...bool) string {
	return tools.Selection{ReadOnly: len(readOnly) > 0 && readOnly[0]}.Signature()
}

// PlanToolSignature is ToolSignature with update_plan enabled or not.
func PlanToolSignature(readOnly, plan bool) string {
	return tools.Selection{ReadOnly: readOnly, Plan: plan}.Signature()
}

// ImagesToolSignature is ToolSignature with update_plan and view_image
// enabled or not. Without images it equals PlanToolSignature.
func ImagesToolSignature(readOnly, plan, images bool) string {
	return tools.Selection{ReadOnly: readOnly, Plan: plan, Images: images}.Signature()
}

// WebFetchToolSignature is the signature with every opt-in tool flag,
// web_fetch last.
func WebFetchToolSignature(readOnly, plan, images, applyPatch, delegate, webFetch bool) string {
	return tools.Selection{ReadOnly: readOnly, Plan: plan, Images: images, ApplyPatch: applyPatch, Delegate: delegate, WebFetch: webFetch}.Signature()
}

// LSPToolSignature is the signature with every opt-in tool flag, lsp
// last.
func LSPToolSignature(readOnly, plan, images, applyPatch, delegate, webFetch, lsp bool) string {
	return tools.Selection{ReadOnly: readOnly, Plan: plan, Images: images, ApplyPatch: applyPatch, Delegate: delegate, WebFetch: webFetch, LSP: lsp}.Signature()
}

// ApplyPatchToolSignature is ImagesToolSignature with apply_patch enabled or
// not. Without apply_patch it equals ImagesToolSignature.
func ApplyPatchToolSignature(readOnly, plan, images, applyPatch bool) string {
	return tools.Selection{ReadOnly: readOnly, Plan: plan, Images: images, ApplyPatch: applyPatch}.Signature()
}

// ValidateResume runs before provider setup, using only the explicit execution
// root and current config. Checkpoint paths and credentials are never executed
// or loaded from metadata.
func ValidateResume(t *trajectory.Trajectory, cfg config.AgentConfig, root string) error {
	if err := t.Validate(); err != nil {
		return err
	}
	var set *skills.Set
	if t.SkillsEnabled {
		set, _ = skills.Discover(root, skills.UserDir())
	}
	if err := resume.Check(t, resume.Request{Root: root, Skills: set}); err != nil {
		return err
	}
	system := strings.NewReplacer("{{cwd}}", root, "{{os}}", runtime.GOOS).Replace(cfg.SystemPrompt)
	if !t.SkipAgentsMD {
		section, err := loadAgentsMD(root, io.Discard, false)
		if err != nil {
			return errors.New("cannot reconstruct AGENTS.md instructions; inspect permissions and start a fresh task if instructions changed")
		}
		system += section
	}
	system += memorySection(checkpointMemory(t))
	system += set.Catalog()
	if t.ReadOnly {
		system += "\n\n" + readOnlyNotice
	}
	if t.AskUserEnabled {
		system += "\n\n" + askUserNotice
	}
	// The additional directories are per invocation, so their section is
	// left out of the comparison; a resume notice reports any change.
	if system != tools.StripExtraDirsNotice(t.SystemPrompt) {
		return errors.New("effective system prompt, AGENTS.md, project memory snapshot, or skills catalog has changed; start a fresh task")
	}
	return nil
}
