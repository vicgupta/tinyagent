package resume

import (
	"errors"
	"testing"

	"github.com/vicgupta/tinyagent/internal/tools"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// checkpoint returns a checkpoint whose recorded tool signature and tool
// names match sel, rooted at root.
func checkpoint(root string, sel tools.Selection, edit func(*trajectory.Trajectory)) *trajectory.Trajectory {
	t := &trajectory.Trajectory{
		Root:              root,
		ReadOnly:          sel.ReadOnly,
		PlanEnabled:       sel.Plan,
		ImagesEnabled:     sel.Images,
		ApplyPatchEnabled: sel.ApplyPatch,
		SubagentsEnabled:  sel.Delegate,
		WorkersEnabled:    sel.Workers,
		WebFetchEnabled:   sel.WebFetch,
		LSPEnabled:        sel.LSP,
		AskUserEnabled:    sel.AskUser,
		MemoryEnabled:     sel.Remember,
		ToolSignature:     sel.Signature(),
		Tools:             sel.Names(),
	}
	if edit != nil {
		edit(t)
	}
	return t
}

func TestCheckWorkspace(t *testing.T) {
	const root = "/work/repo"
	plain := tools.Selection{}
	memory := tools.Selection{Remember: true, MemoryTools: true}
	tests := []struct {
		name  string
		cp    *trajectory.Trajectory
		root  string
		field string
		msg   string
	}{
		{"match", checkpoint(root, plain, nil), root, "", ""},
		{"match with memory tools", checkpoint(root, memory, nil), root, "", ""},
		{"root mismatch", checkpoint(root, plain, nil), "/elsewhere", FieldRoot,
			"--cwd does not match the checkpoint's canonical root"},
		{"fingerprint mismatch", checkpoint(root, plain, func(t *trajectory.Trajectory) {
			t.ToolSignature = "stale"
		}), root, FieldToolSignature, "tool compatibility has changed; start a fresh task"},
		{"recorded setting changes fingerprint", checkpoint(root, plain, func(t *trajectory.Trajectory) {
			t.PlanEnabled = true
		}), root, FieldToolSignature, "tool compatibility has changed; start a fresh task"},
		{"tool-name mismatch", checkpoint(root, plain, func(t *trajectory.Trajectory) {
			t.Tools = append([]string{}, t.Tools...)
			t.Tools[0], t.Tools[1] = t.Tools[1], t.Tools[0]
		}), root, FieldTools, "tool set has changed; start a fresh task"},
		{"tool-name count mismatch", checkpoint(root, plain, func(t *trajectory.Trajectory) {
			t.Tools = t.Tools[:len(t.Tools)-1]
		}), root, FieldTools, "tool set has changed; start a fresh task"},
		{"remember dropped from tool list", checkpoint(root, memory, func(t *trajectory.Trajectory) {
			// Without remember in the list the checkpoint reads as memory
			// tools off, so the rebuilt tool names no longer match.
			var kept []string
			for _, n := range t.Tools {
				if n != tools.RememberToolName {
					kept = append(kept, n)
				}
			}
			t.Tools = kept
		}), root, FieldTools, "tool set has changed; start a fresh task"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := Check(tc.cp, Request{Root: tc.root})
			if tc.field == "" {
				if err != nil {
					t.Fatalf("Check = %v, want nil", err)
				}
				return
			}
			var inc *Incompatibility
			if !errors.As(err, &inc) {
				t.Fatalf("Check = %v, want *Incompatibility", err)
			}
			if inc.Field != tc.field || inc.Error() != tc.msg || inc.ExitCode() != 1 {
				t.Fatalf("got field=%q msg=%q exit=%d, want field=%q msg=%q exit=1",
					inc.Field, inc.Error(), inc.ExitCode(), tc.field, tc.msg)
			}
		})
	}
}

func TestCheckFlagsOnlyWithoutRoot(t *testing.T) {
	cp := checkpoint("/work/repo", tools.Selection{}, func(t *trajectory.Trajectory) { t.ToolSignature = "stale" })
	if err := Check(cp, Request{}); err != nil {
		t.Fatalf("Check without Root = %v, want nil", err)
	}
}

func TestCheckFlagConflictBeforeWorkspace(t *testing.T) {
	cp := checkpoint("/work/repo", tools.Selection{}, nil)
	err := Check(cp, Request{Root: "/elsewhere", Flags: Flags{Images: Opt{Set: true, On: true}}})
	var inc *Incompatibility
	if !errors.As(err, &inc) || inc.Flag != "--images" {
		t.Fatalf("Check = %v, want the --images conflict first", err)
	}
}

func TestOfferedTools(t *testing.T) {
	cp := checkpoint("/r", tools.Selection{Remember: true, MemoryTools: true}, nil)
	if !RememberOffered(cp) || !MemoryToolsOffered(cp) {
		t.Fatal("memory checkpoint should offer remember and memory tools")
	}
	cp.ReadOnly = true
	if RememberOffered(cp) || MemoryToolsOffered(cp) {
		t.Fatal("read-only checkpoint must not offer remember")
	}
}
