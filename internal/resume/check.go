// Package resume owns what a --resume invocation must match in its
// checkpoint. The CLI parses flags into a Request, calls Check, and renders
// the typed *Incompatibility it returns; the flag-versus-checkpoint table
// lives here so adding a recorded setting is a one-line change.
package resume

import (
	"fmt"

	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// Opt is one boolean CLI setting. Set reports whether the flag was given on
// the command line; an unset flag never conflicts with the checkpoint.
type Opt struct {
	Set bool
	On  bool
}

// Flags holds the boolean settings a checkpoint records. NoAgentsMD is On
// when AGENTS.md loading is skipped (--no-agents-md); Skills is the resolved
// --skills/--no-skills value.
type Flags struct {
	ReadOnly       Opt
	Plan           Opt
	Images         Opt
	ApplyPatch     Opt
	Subagents      Opt
	Workers        Opt
	AskUser        Opt
	WebFetch       Opt
	LSP            Opt
	ScopedAgentsMD Opt
	Skills         Opt
	Memory         Opt
	NoAgentsMD     Opt
}

// Request is the CLI-derived input a resume is checked against.
type Request struct {
	Flags Flags
}

// Remedy is the advice every recorded-setting conflict carries.
const Remedy = "start a fresh task"

// Incompatibility reports one CLI setting that contradicts the checkpoint.
type Incompatibility struct {
	Flag     string // "--images"
	Field    string // checkpoint JSON field, "images_enabled"
	Recorded string // how the message names the recorded value, "recorded images setting"
	Want     string // the checkpoint's value, "on" or "off"
	Got      string // the flag's value, "on" or "off"
	Remedy   string // "start a fresh task"
	// Usage marks conflicts the CLI reports as usage errors (exit 2);
	// the others exit 1, as they did before this table existed.
	Usage bool
}

func (e *Incompatibility) Error() string {
	return fmt.Sprintf("%s conflicts with the checkpoint's %s; %s", e.Flag, e.Recorded, e.Remedy)
}

// ExitCode is the process exit code the CLI uses for this conflict.
func (e *Incompatibility) ExitCode() int {
	if e.Usage {
		return 2
	}
	return 1
}

// setting is one row of the flag-versus-checkpoint table.
type setting struct {
	flag, field, recorded string
	usage                 bool
	opt                   func(*Flags) Opt
	recordedOn            func(*trajectory.Trajectory) bool
}

// settings lists every recorded boolean setting in the order Check reports
// them. Add a row here when a checkpoint starts recording a new flag.
var settings = []setting{
	{"--read-only", "read_only", "recorded mode", false,
		func(f *Flags) Opt { return f.ReadOnly }, func(t *trajectory.Trajectory) bool { return t.ReadOnly }},
	{"--plan", "plan_enabled", "recorded plan setting", false,
		func(f *Flags) Opt { return f.Plan }, func(t *trajectory.Trajectory) bool { return t.PlanEnabled }},
	{"--images", "images_enabled", "recorded images setting", false,
		func(f *Flags) Opt { return f.Images }, func(t *trajectory.Trajectory) bool { return t.ImagesEnabled }},
	{"--apply-patch", "apply_patch_enabled", "recorded apply_patch setting", true,
		func(f *Flags) Opt { return f.ApplyPatch }, func(t *trajectory.Trajectory) bool { return t.ApplyPatchEnabled }},
	{"--subagents", "subagents_enabled", "recorded subagents setting", true,
		func(f *Flags) Opt { return f.Subagents }, func(t *trajectory.Trajectory) bool { return t.SubagentsEnabled }},
	{"--workers", "workers_enabled", "recorded workers setting", true,
		func(f *Flags) Opt { return f.Workers }, func(t *trajectory.Trajectory) bool { return t.WorkersEnabled }},
	{"--ask-user", "ask_user_enabled", "recorded ask_user setting", true,
		func(f *Flags) Opt { return f.AskUser }, func(t *trajectory.Trajectory) bool { return t.AskUserEnabled }},
	{"--web-fetch", "web_fetch_enabled", "recorded web_fetch setting", true,
		func(f *Flags) Opt { return f.WebFetch }, func(t *trajectory.Trajectory) bool { return t.WebFetchEnabled }},
	{"--lsp", "lsp_enabled", "recorded lsp setting", true,
		func(f *Flags) Opt { return f.LSP }, func(t *trajectory.Trajectory) bool { return t.LSPEnabled }},
	{"--scoped-agents-md", "scoped_agents_md_enabled", "recorded scoped_agents_md setting", true,
		func(f *Flags) Opt { return f.ScopedAgentsMD }, func(t *trajectory.Trajectory) bool { return t.ScopedAgentsMDEnabled }},
	{"--skills/--no-skills", "skills_enabled", "recorded skills setting", false,
		func(f *Flags) Opt { return f.Skills }, func(t *trajectory.Trajectory) bool { return t.SkillsEnabled }},
	{"--memory", "memory_enabled", "recorded memory setting", true,
		func(f *Flags) Opt { return f.Memory }, func(t *trajectory.Trajectory) bool { return t.MemoryEnabled }},
	{"--no-agents-md", "skip_agents_md", "recorded loading mode", false,
		func(f *Flags) Opt { return f.NoAgentsMD }, func(t *trajectory.Trajectory) bool { return t.SkipAgentsMD }},
}

// Check compares every explicitly set flag in req with the checkpoint and
// returns the first conflict as an *Incompatibility, or nil.
func Check(t *trajectory.Trajectory, req Request) error {
	for _, s := range settings {
		o := s.opt(&req.Flags)
		want := s.recordedOn(t)
		if !o.Set || o.On == want {
			continue
		}
		return &Incompatibility{
			Flag:     s.flag,
			Field:    s.field,
			Recorded: s.recorded,
			Want:     onOff(want),
			Got:      onOff(o.On),
			Remedy:   Remedy,
			Usage:    s.usage,
		}
	}
	return nil
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
