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
	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/skills"
	"github.com/vicgupta/tinyagent/internal/tools"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// ToolSignature fingerprints the definitions plus an execution contract
// version. Bump the prefix when tool semantics change without a spec change.
// The plan tool is opt-in: without it the signature is unchanged, so
// checkpoints from earlier builds still resume.
func ToolSignature(readOnly ...bool) string {
	return toolSignature(len(readOnly) > 0 && readOnly[0], false)
}

// PlanToolSignature is ToolSignature with update_plan enabled or not.
func PlanToolSignature(readOnly, plan bool) string { return toolSignature(readOnly, plan) }

// ImagesToolSignature is ToolSignature with update_plan and view_image
// enabled or not. Without images it equals PlanToolSignature.
func ImagesToolSignature(readOnly, plan, images bool) string {
	return toolSignature(readOnly, plan, images)
}

// WebFetchToolSignature is the signature with every opt-in tool flag,
// web_fetch last.
func WebFetchToolSignature(readOnly, plan, images, applyPatch, delegate, webFetch bool) string {
	return toolSignature(readOnly, plan, images, applyPatch, delegate, webFetch)
}

// ApplyPatchToolSignature is ImagesToolSignature with apply_patch enabled or
// not. Without apply_patch it equals ImagesToolSignature.
func ApplyPatchToolSignature(readOnly, plan, images, applyPatch bool) string {
	return toolSignature(readOnly, plan, images, applyPatch)
}

// toolSignature takes the opt-in images, apply_patch and delegate settings,
// in that order, after readonly and plan.
func toolSignature(readonly, plan bool, opt ...bool) string {
	img := len(opt) > 0 && opt[0]
	patch := len(opt) > 1 && opt[1]
	delegate := len(opt) > 2 && opt[2]
	webFetch := len(opt) > 3 && opt[3]
	data, _ := json.Marshal(builtinSpecs(readonly, plan, img, patch, delegate, webFetch))
	prefix := "tools-v1:"
	if readonly {
		prefix = "tools-read-only-v1:"
	}
	if plan {
		prefix = "plan-v1:" + prefix
	}
	if img {
		prefix = "images-v1:" + prefix
	}
	if patch {
		prefix = "apply-patch-v1:" + prefix
	}
	if delegate {
		prefix = "subagents-v1:" + prefix
	}
	if webFetch {
		prefix = "web-fetch-v1:" + prefix
	}
	return fmt.Sprintf("%s%x", prefix, sha256.Sum256(data))
}

func builtinSpecs(readOnly, plan bool, opt ...bool) []model.ToolSpec {
	specs := tools.Specs(readOnly)
	if plan {
		specs = append(specs, tools.PlanSpec())
	}
	if len(opt) > 0 && opt[0] {
		specs = append(specs, tools.ViewImageSpec())
	}
	if len(opt) > 1 && opt[1] && !readOnly {
		specs = append(specs, tools.ApplyPatchSpec())
	}
	if len(opt) > 2 && opt[2] {
		specs = append(specs, tools.DelegateSpec())
	}
	if len(opt) > 3 && opt[3] {
		specs = append(specs, tools.WebFetchSpec())
	}
	return specs
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
	if t.ToolSignature != toolSignature(t.ReadOnly, t.PlanEnabled, t.ImagesEnabled, t.ApplyPatchEnabled, t.SubagentsEnabled, t.WebFetchEnabled) {
		return errors.New("tool compatibility has changed; start a fresh task")
	}
	names := toolNames(builtinSpecs(t.ReadOnly, t.PlanEnabled, t.ImagesEnabled, t.ApplyPatchEnabled, t.SubagentsEnabled, t.WebFetchEnabled))
	var set *skills.Set
	if t.SkillsEnabled {
		set, _ = skills.Discover(root, skills.UserDir())
		if set.Len() > 0 {
			names = append(names, skills.ToolName)
		}
	}
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
	system += set.Catalog()
	if t.ReadOnly {
		system += "\n\n" + readOnlyNotice
	}
	// The additional directories are per invocation, so their section is
	// left out of the comparison; a resume notice reports any change.
	if system != tools.StripExtraDirsNotice(t.SystemPrompt) {
		return errors.New("effective system prompt, AGENTS.md, or skills catalog has changed; start a fresh task")
	}
	return nil
}
