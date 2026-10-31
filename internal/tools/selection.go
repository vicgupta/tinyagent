package tools

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/skills"
)

// Selection is the tool set for one run. Specs and Signature are the same
// policy seen two ways: what the model is offered, and what a checkpoint
// must match to resume. Adding an opt-in tool means adding its flag here
// (and its executor behavior), not editing a second list.
type Selection struct {
	// ReadOnly keeps only read_file, list_files and grep among the base
	// tools and drops every tool that writes (apply_patch, workers,
	// remember and the memory tools, command and external tools).
	ReadOnly bool
	// Plan offers update_plan.
	Plan bool
	// Images offers view_image.
	Images bool
	// ApplyPatch offers apply_patch (never in read-only mode).
	ApplyPatch bool
	// Delegate offers delegate.
	Delegate bool
	// Workers offers worker and apply_worker (never in read-only mode).
	Workers bool
	// WebFetch offers web_fetch.
	WebFetch bool
	// LSP offers lsp.
	LSP bool
	// AskUser offers ask_user.
	AskUser bool
	// Remember offers remember (never in read-only mode);
	// RememberMaxTextLen sizes its description (0 = the default).
	Remember           bool
	RememberMaxTextLen int
	// MemoryTools offers memory_search and memory_list next to remember.
	MemoryTools bool
	// Skills, when it holds at least one skill, offers the skill tool.
	Skills *skills.Set
	// TaskTypes adds an optional task_type argument to delegate and
	// worker (see WithTaskType).
	TaskTypes []string
	// CommandTools and External are the configured command tools and
	// external (MCP) specs, offered last and never in read-only mode.
	CommandTools []*CommandTool
	External     []model.ToolSpec
}

// Selection returns the tool set e offers, before any Only filter.
func (e *Executor) Selection() Selection {
	s := Selection{
		ReadOnly:     e.ReadOnly,
		Plan:         e.Plan,
		Images:       e.Images,
		ApplyPatch:   e.ApplyPatch,
		Delegate:     e.Delegate,
		Workers:      e.Workers,
		WebFetch:     e.WebFetch != nil,
		LSP:          e.LSP != nil,
		AskUser:      e.AskUser,
		Remember:     e.Remember != nil,
		MemoryTools:  e.MemoryTools,
		Skills:       e.Skills,
		TaskTypes:    e.TaskTypes,
		CommandTools: e.CommandTools,
		External:     e.ExternalSpecs,
	}
	if e.Remember != nil {
		s.RememberMaxTextLen = e.Remember.Limits.MaxTextLen
	}
	return s
}

// Specs returns the tool definitions the model is offered, in catalog
// order.
func (s Selection) Specs() []model.ToolSpec {
	out := Specs(s.ReadOnly)
	if s.Plan {
		out = append(out, PlanSpec())
	}
	if s.Images {
		out = append(out, ViewImageSpec())
	}
	if s.ApplyPatch && !s.ReadOnly {
		out = append(out, ApplyPatchSpec())
	}
	if s.Delegate {
		out = append(out, WithTaskType(DelegateSpec(), s.TaskTypes))
	}
	if s.Workers && !s.ReadOnly {
		out = append(out, WithTaskType(WorkerSpec(), s.TaskTypes), ApplyWorkerSpec())
	}
	if s.WebFetch {
		out = append(out, WebFetchSpec())
	}
	if s.LSP {
		out = append(out, LSPSpec())
	}
	if s.AskUser {
		out = append(out, AskUserSpec())
	}
	if s.Remember && !s.ReadOnly {
		out = append(out, RememberSpecFor(s.RememberMaxTextLen))
		if s.MemoryTools {
			out = append(out, MemorySearchSpec(), MemoryListSpec())
		}
	}
	if s.Skills.Len() > 0 {
		out = append(out, s.Skills.Spec())
	}
	if s.ReadOnly {
		return out
	}
	for _, c := range s.CommandTools {
		out = append(out, c.Spec())
	}
	return append(out, s.External...)
}

// Names returns the names of Specs, in order.
func (s Selection) Names() []string {
	specs := s.Specs()
	names := make([]string, len(specs))
	for i, sp := range specs {
		names[i] = sp.Function.Name
	}
	return names
}

// Offers reports whether name is in Specs.
func (s Selection) Offers(name string) bool {
	for _, sp := range s.Specs() {
		if sp.Function.Name == name {
			return true
		}
	}
	return false
}

// Signature fingerprints the built-in tool definitions plus a versioned
// execution-contract prefix; a checkpoint resumes only when it matches.
// Each opt-in flag adds its own prefix, so with every opt-in tool off the
// signature equals the one from builds that predate them. The hash covers
// the built-in subset in its historical form (plain delegate spec; no
// workers, memory, skill, command or external tools, which are checked
// separately), so existing checkpoints keep resuming. Bump a prefix when
// tool semantics change without a spec change.
func (s Selection) Signature() string {
	data, _ := json.Marshal(s.fingerprintSpecs())
	prefix := "tools-v1:"
	if s.ReadOnly {
		prefix = "tools-read-only-v1:"
	}
	for _, p := range []struct {
		on     bool
		prefix string
	}{
		{s.Plan, "plan-v1:"},
		{s.Images, "images-v1:"},
		{s.ApplyPatch, "apply-patch-v1:"},
		{s.Delegate, "subagents-v1:"},
		{s.WebFetch, "web-fetch-v1:"},
		{s.LSP, "lsp-v1:"},
		{s.AskUser, "ask-user-v1:"},
	} {
		if p.on {
			prefix = p.prefix + prefix
		}
	}
	return fmt.Sprintf("%s%x", prefix, sha256.Sum256(data))
}

// fingerprintSpecs is the subset of Specs that Signature hashes: the
// built-in tools, with delegate in its plain form. Workers, memory, skill,
// command and external tools are left out (resume checks them by name or
// refuses them), which keeps the hash of existing checkpoints unchanged.
func (s Selection) fingerprintSpecs() []model.ToolSpec {
	b := s
	b.TaskTypes = nil
	b.Workers = false
	b.Remember, b.MemoryTools = false, false
	b.Skills = nil
	b.CommandTools, b.External = nil, nil
	return b.Specs()
}
