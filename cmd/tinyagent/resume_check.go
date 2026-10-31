package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/vicgupta/tinyagent/internal/resume"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// checkResumeFlags compares the explicitly set boolean flags with the
// checkpoint's recorded settings (internal/resume owns the table) and
// renders the first conflict. It returns 0 when the flags match.
func checkResumeFlags(fs *flag.FlagSet, o options, src *trajectory.Trajectory) int {
	skillsOn, skillsSet, err := skillsFlag(fs, o)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	opt := func(on bool, names ...string) resume.Opt {
		return resume.Opt{Set: flagWasSet(fs, names...), On: on}
	}
	req := resume.Request{Flags: resume.Flags{
		ReadOnly:       opt(o.readOnly, "read-only"),
		Plan:           opt(o.plan, "plan"),
		Images:         opt(o.images, "images"),
		ApplyPatch:     opt(o.applyPatch, "apply-patch"),
		Subagents:      opt(o.subagents, "subagents"),
		Workers:        opt(o.workers, "workers"),
		AskUser:        opt(o.askUser, "ask-user"),
		WebFetch:       opt(o.webFetch, "web-fetch"),
		LSP:            opt(o.lsp, "lsp"),
		ScopedAgentsMD: opt(o.scopedAgentsMD, "scoped-agents-md"),
		Skills:         resume.Opt{Set: skillsSet, On: skillsOn},
		Memory:         opt(o.memory.enabled, "memory"),
		NoAgentsMD:     opt(o.noAgentsMD, "no-agents-md"),
	}}
	return renderResumeCheck(resume.Check(src, req))
}

// renderResumeCheck prints a resume.Check result the way the CLI always
// has: "error: resume: <flag> conflicts with ...", exit 2 for usage-style
// conflicts and 1 for the rest.
func renderResumeCheck(err error) int {
	if err == nil {
		return 0
	}
	inc, ok := err.(*resume.Incompatibility)
	if ok && inc.Usage {
		fmt.Fprintln(os.Stderr, "error: resume:", inc)
		return 2
	}
	return fail(fmt.Errorf("resume: %w", err))
}
