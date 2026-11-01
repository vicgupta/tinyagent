package resume

import (
	"errors"
	"testing"

	"github.com/vicgupta/tinyagent/internal/trajectory"
)

func TestCheckFlagMismatches(t *testing.T) {
	on := Opt{Set: true, On: true}
	off := Opt{Set: true, On: false}
	tests := []struct {
		name  string
		flags Flags
		cp    trajectory.Trajectory
		flag  string
		field string
		msg   string
		exit  int
	}{
		{"read-only", Flags{ReadOnly: on}, trajectory.Trajectory{}, "--read-only", "read_only",
			"--read-only conflicts with the checkpoint's recorded mode; start a fresh task", 1},
		{"plan", Flags{Plan: off}, trajectory.Trajectory{PlanEnabled: true}, "--plan", "plan_enabled",
			"--plan conflicts with the checkpoint's recorded plan setting; start a fresh task", 1},
		{"images", Flags{Images: on}, trajectory.Trajectory{}, "--images", "images_enabled",
			"--images conflicts with the checkpoint's recorded images setting; start a fresh task", 1},
		{"apply-patch", Flags{ApplyPatch: on}, trajectory.Trajectory{}, "--apply-patch", "apply_patch_enabled",
			"--apply-patch conflicts with the checkpoint's recorded apply_patch setting; start a fresh task", 2},
		{"subagents", Flags{Subagents: off}, trajectory.Trajectory{SubagentsEnabled: true}, "--subagents", "subagents_enabled",
			"--subagents conflicts with the checkpoint's recorded subagents setting; start a fresh task", 2},
		{"workers", Flags{Workers: on}, trajectory.Trajectory{}, "--workers", "workers_enabled",
			"--workers conflicts with the checkpoint's recorded workers setting; start a fresh task", 2},
		{"ask-user", Flags{AskUser: on}, trajectory.Trajectory{}, "--ask-user", "ask_user_enabled",
			"--ask-user conflicts with the checkpoint's recorded ask_user setting; start a fresh task", 2},
		{"web-fetch", Flags{WebFetch: off}, trajectory.Trajectory{WebFetchEnabled: true}, "--web-fetch", "web_fetch_enabled",
			"--web-fetch conflicts with the checkpoint's recorded web_fetch setting; start a fresh task", 2},
		{"lsp", Flags{LSP: on}, trajectory.Trajectory{}, "--lsp", "lsp_enabled",
			"--lsp conflicts with the checkpoint's recorded lsp setting; start a fresh task", 2},
		{"scoped-agents-md", Flags{ScopedAgentsMD: on}, trajectory.Trajectory{}, "--scoped-agents-md", "scoped_agents_md_enabled",
			"--scoped-agents-md conflicts with the checkpoint's recorded scoped_agents_md setting; start a fresh task", 2},
		{"skills", Flags{Skills: off}, trajectory.Trajectory{SkillsEnabled: true}, "--skills/--no-skills", "skills_enabled",
			"--skills/--no-skills conflicts with the checkpoint's recorded skills setting; start a fresh task", 1},
		{"memory", Flags{Memory: on}, trajectory.Trajectory{}, "--memory", "memory_enabled",
			"--memory conflicts with the checkpoint's recorded memory setting; start a fresh task", 2},
		{"no-agents-md", Flags{NoAgentsMD: on}, trajectory.Trajectory{}, "--no-agents-md", "skip_agents_md",
			"--no-agents-md conflicts with the checkpoint's recorded loading mode; start a fresh task", 1},
	}
	if len(tests) != len(settings) {
		t.Fatalf("table covers %d settings, Check knows %d", len(tests), len(settings))
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cp := tc.cp
			err := Check(&cp, Request{Flags: tc.flags})
			var inc *Incompatibility
			if !errors.As(err, &inc) {
				t.Fatalf("Check = %v, want *Incompatibility", err)
			}
			if inc.Flag != tc.flag || inc.Field != tc.field || inc.Remedy != Remedy {
				t.Errorf("got %+v", inc)
			}
			if inc.Want == inc.Got {
				t.Errorf("Want %q and Got %q should differ", inc.Want, inc.Got)
			}
			if err.Error() != tc.msg {
				t.Errorf("message = %q, want %q", err.Error(), tc.msg)
			}
			if inc.ExitCode() != tc.exit {
				t.Errorf("exit = %d, want %d", inc.ExitCode(), tc.exit)
			}
		})
	}
}

func TestCheckMatchingAndUnsetFlagsPass(t *testing.T) {
	cp := trajectory.Trajectory{ReadOnly: true, PlanEnabled: true, SkillsEnabled: true}
	req := Request{Flags: Flags{
		ReadOnly: Opt{Set: true, On: true},
		Plan:     Opt{Set: true, On: true},
		// Unset flags never conflict, whatever their zero value says.
		Skills: Opt{On: false},
		Images: Opt{On: true},
	}}
	if err := Check(&cp, req); err != nil {
		t.Fatalf("Check = %v, want nil", err)
	}
	if err := Check(&trajectory.Trajectory{}, Request{}); err != nil {
		t.Fatalf("empty request: Check = %v, want nil", err)
	}
}

func TestCheckReportsFirstConflictInTableOrder(t *testing.T) {
	cp := trajectory.Trajectory{}
	err := Check(&cp, Request{Flags: Flags{
		Memory:   Opt{Set: true, On: true},
		ReadOnly: Opt{Set: true, On: true},
	}})
	var inc *Incompatibility
	if !errors.As(err, &inc) || inc.Flag != "--read-only" {
		t.Fatalf("Check = %v, want the --read-only conflict first", err)
	}
	if inc.Want != "off" || inc.Got != "on" {
		t.Errorf("Want/Got = %q/%q, want off/on", inc.Want, inc.Got)
	}
}
