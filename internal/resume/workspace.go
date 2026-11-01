package resume

import (
	"strings"

	"github.com/vicgupta/tinyagent/internal/skills"
	"github.com/vicgupta/tinyagent/internal/tools"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// Messages for the workspace checks. They are the same sentences the agent
// printed before these checks moved here, so users see no change.
const (
	rootMessage        = "--cwd does not match the checkpoint's canonical root"
	fingerprintMessage = "tool compatibility has changed; start a fresh task"
	toolSetMessage     = "tool set has changed; start a fresh task"
)

// Field names a workspace Incompatibility reports.
const (
	FieldRoot          = "root"
	FieldToolSignature = "tool_signature"
	FieldTools         = "tools"
)

// RememberOffered reports whether a checkpoint offered the remember tool:
// memory was on, the run was not read-only, and remember is in its tool list.
func RememberOffered(t *trajectory.Trajectory) bool {
	if !t.MemoryEnabled || t.ReadOnly {
		return false
	}
	return hasTool(t, tools.RememberToolName)
}

// MemoryToolsOffered reports whether a checkpoint offered the read-only
// memory_search and memory_list tools (checkpoints from before they existed
// resume without them).
func MemoryToolsOffered(t *trajectory.Trajectory) bool {
	return RememberOffered(t) && hasTool(t, tools.MemorySearchToolName)
}

func hasTool(t *trajectory.Trajectory, name string) bool {
	for _, n := range t.Tools {
		if n == name {
			return true
		}
	}
	return false
}

// Selection is the tool selection a checkpoint recorded. set is the
// rediscovered skills (nil when skills were off). Command and external tools
// are not part of it: t.Tools omits command tools, and checkpoints with
// external tools are refused before this.
func Selection(t *trajectory.Trajectory, set *skills.Set) tools.Selection {
	return tools.Selection{
		ReadOnly:    t.ReadOnly,
		Plan:        t.PlanEnabled,
		Images:      t.ImagesEnabled,
		ApplyPatch:  t.ApplyPatchEnabled,
		Delegate:    t.SubagentsEnabled,
		Workers:     t.WorkersEnabled,
		WebFetch:    t.WebFetchEnabled,
		LSP:         t.LSPEnabled,
		AskUser:     t.AskUserEnabled,
		Remember:    RememberOffered(t),
		MemoryTools: MemoryToolsOffered(t),
		Skills:      set,
	}
}

// discoverSkills is the skills lookup Check uses when the request does not
// supply one.
func discoverSkills(root string) *skills.Set {
	set, _ := skills.Discover(root, skills.UserDir())
	return set
}

// skillsFor returns the skills a checkpoint's tool set is rebuilt with: nil
// when the checkpoint had skills off, otherwise the request's set or a fresh
// discovery under root.
func skillsFor(t *trajectory.Trajectory, req Request) *skills.Set {
	if !t.SkillsEnabled {
		return nil
	}
	if req.Skills != nil {
		return req.Skills
	}
	return discoverSkills(req.Root)
}

// checkWorkspace runs the root, tool fingerprint, and tool-name checks. It
// returns the first mismatch as an *Incompatibility, or nil.
func checkWorkspace(t *trajectory.Trajectory, req Request) error {
	if req.Root != t.Root {
		return &Incompatibility{
			Flag:     "--cwd",
			Field:    FieldRoot,
			Recorded: "canonical root",
			Want:     t.Root,
			Got:      req.Root,
			Remedy:   "pass the checkpoint's root with --cwd",
			Message:  rootMessage,
		}
	}
	sel := Selection(t, skillsFor(t, req))
	if sig := sel.Signature(); t.ToolSignature != sig {
		return &Incompatibility{
			Field:    FieldToolSignature,
			Recorded: "tool signature",
			Want:     t.ToolSignature,
			Got:      sig,
			Remedy:   Remedy,
			Message:  fingerprintMessage,
		}
	}
	if names := sel.Names(); !equalNames(names, t.Tools) {
		return &Incompatibility{
			Field:    FieldTools,
			Recorded: "tool set",
			Want:     strings.Join(t.Tools, ","),
			Got:      strings.Join(names, ","),
			Remedy:   Remedy,
			Message:  toolSetMessage,
		}
	}
	return nil
}

func equalNames(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
