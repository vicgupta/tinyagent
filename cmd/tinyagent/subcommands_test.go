package main

import (
	"flag"
	"reflect"
	"strings"
	"testing"
)

// parsedFlags expands args and parses them, returning every flag that was set
// with its value, so subcommand and flag spellings can be compared.
func parsedFlags(t *testing.T, args ...string) map[string]string {
	t.Helper()
	out, help, err := expandSubcommand(args)
	if err != nil || help != nil {
		t.Fatalf("%v: err %v help %v", args, err, help)
	}
	fs := newFlagSet(&options{})
	if err := fs.Parse(out); err != nil {
		t.Fatalf("%v -> %v: %v", args, out, err)
	}
	got := map[string]string{}
	fs.Visit(func(f *flag.Flag) { got[f.Name] = f.Value.String() })
	got["#args"] = strings.Join(fs.Args(), " ")
	return got
}

func TestSubcommandsMatchFlags(t *testing.T) {
	cases := []struct{ sub, flags []string }{
		{[]string{"sandbox", "-t", "x"}, []string{"--sandbox", "-t", "x"}},
		{[]string{"sandbox", "network", "-t", "x"}, []string{"--sandbox", "--sandbox-network", "-t", "x"}},
		{[]string{"sandbox", "--network", "--allowed-domains", "a.com", "--allow-domain=b.com", "-t", "x"}, []string{"--sandbox", "--sandbox-network", "--sandbox-allow-domain", "a.com", "--sandbox-allow-domain", "b.com", "-t", "x"}},
		{[]string{"sandbox", "--backend", "bwrap", "-t", "x"}, []string{"--sandbox", "--sandbox-backend", "bwrap", "-t", "x"}},
		{[]string{"sandbox", "network", "--backend=landlock", "-t", "x"}, []string{"--sandbox", "--sandbox-network", "--sandbox-backend", "landlock", "-t", "x"}},
		{[]string{"memory", "-t", "x", "--cwd", "d"}, []string{"--memory", "-t", "x", "--cwd", "d"}},
		{[]string{"memory", "list", "--json"}, []string{"--memory-list", "--json"}},
		{[]string{"memory", "path"}, []string{"--memory-path"}},
		{[]string{"memory", "approve", "abc"}, []string{"--memory-approve", "abc"}},
		{[]string{"memory", "forget", "all", "--yes"}, []string{"--memory-forget", "all", "--memory-yes"}},
		{[]string{"memory", "config"}, []string{"--memory-help"}},
		{[]string{"planner", "-t", "x"}, []string{"--plan", "-t", "x"}},
		{[]string{"planner", "--first", "--file", "p.md", "--", "fix", "the", "bug"}, []string{"--plan", "--plan-first", "--plan-file", "p.md", "-t", "fix the bug"}},
		{[]string{"plugins", "list"}, []string{"--list-plugins"}},
	}
	for _, c := range cases {
		if a, b := parsedFlags(t, c.sub...), parsedFlags(t, c.flags...); !reflect.DeepEqual(a, b) {
			t.Errorf("%v = %v, want %v (%v)", c.sub, a, b, c.flags)
		}
	}
}

func TestOldFlagFormsUnchanged(t *testing.T) {
	for _, args := range [][]string{
		{"--sandbox", "--sandbox-network", "-t", "x"},
		{"--memory-list"},
		{"--plan", "--plan-first", "-t", "sandbox the build"},
		{"-t", "memory leaks in the parser"},
		{},
	} {
		out, help, err := expandSubcommand(args)
		if err != nil || help != nil || !reflect.DeepEqual(out, args) {
			t.Errorf("%v rewritten to %v (%v, %v)", args, out, help, err)
		}
	}
}

// A prompt whose first word is a subcommand name is never hijacked: it is
// left for the normal parser, which asks for -t.
func TestSubcommandWordInPromptNotHijacked(t *testing.T) {
	args := []string{"memory", "leaks", "in", "the", "parser"}
	if out, help, err := expandSubcommand(args); err != nil || help != nil || !reflect.DeepEqual(out, args) {
		t.Fatalf("rewritten: %v %v %v", out, help, err)
	}
	code, _, stderr := runWith(t, args...)
	if code != 2 || !strings.Contains(stderr, "pass the task with -t") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
}

func TestUnknownSubcommandOption(t *testing.T) {
	code, _, stderr := runWith(t, "sandbox", "--bogus")
	if code != 2 || !strings.Contains(stderr, "unknown sandbox option --bogus") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if _, _, err := expandSubcommand([]string{"memory", "approve"}); err == nil {
		t.Error("memory approve without id accepted")
	}
	if _, _, err := expandSubcommand([]string{"help", "nope"}); err == nil || !strings.Contains(err.Error(), "unknown subcommand") {
		t.Errorf("help nope: %v", err)
	}
}

func TestSubcommandHelp(t *testing.T) {
	for _, s := range subcommands {
		for _, args := range [][]string{{s.name, "--help"}, {s.name, "-h"}, {s.name, "help"}, {"help", s.name}} {
			code, out, _ := runWith(t, args...)
			if !strings.Contains(out, "docs/subcommands.md") {
				t.Errorf("%s help has no docs pointer", s.name)
			}
			if code != 0 || !strings.HasPrefix(out, "Usage: tinyagent "+s.name) {
				t.Errorf("%v: exit %d, %q", args, code, out)
			}
			for _, f := range s.options {
				if !strings.Contains(out, "--"+f) {
					t.Errorf("%v: help lacks --%s", args, f)
				}
			}
		}
	}
	for _, args := range [][]string{{"--memory-help"}, {"memory", "config"}} {
		code := 0
		out := captureStdout(t, func() { code, _, _ = runWith(t, args...) })
		if code != 0 || !strings.Contains(out, "memory.toml") {
			t.Errorf("%v: exit %d", args, code)
		}
	}
}

// TestBareSubcommandPrintsHelp checks every registered subcommand typed
// alone at a terminal prints exactly "<sub> --help" and exits the same way;
// with stdin piped, subcommands with base flags keep their old result
// (--task is required, exit 2); any action or option is unchanged.
func TestBareSubcommandPrintsHelp(t *testing.T) {
	t.Setenv("AGENT_MODEL", "")
	defer func(f func() bool) { stdinIsTerminal = f }(stdinIsTerminal)
	for _, s := range subcommands {
		t.Run(s.name, func(t *testing.T) {
			stdinIsTerminal = func() bool { return true }
			wantCode, want, wantErr := runWith(t, s.name, "--help")
			code, got, errOut := runWith(t, s.name)
			if code != wantCode || got != want || errOut != wantErr {
				t.Errorf("bare %s at a terminal: exit %d, stderr %q, stdout:\n%s\nwant exit %d and:\n%s", s.name, code, errOut, got, wantCode, want)
			}
			stdinIsTerminal = func() bool { return false }
			code, got, _ = runWith(t, s.name)
			if len(s.base) > 0 {
				if code != 2 || got == want {
					t.Errorf("bare %s with piped stdin: exit %d, want 2 without help", s.name, code)
				}
				// With a flag it runs as before: base flags plus that flag.
				args, sh, err := expandSubcommand([]string{s.name, "-t", "x"})
				if err != nil || sh != nil || !reflect.DeepEqual(args, append(append([]string{}, s.base...), "--t", "x")) {
					t.Errorf("%s -t x expanded to %v, %v, %v", s.name, args, sh, err)
				}
			} else if code != wantCode || got != want {
				t.Errorf("bare %s with piped stdin: exit %d, want its help (unchanged)", s.name, code)
			}
			for a, act := range s.actions {
				if act.arg || act.arg2 {
					continue
				}
				args, sh, err := expandSubcommand([]string{s.name, a})
				if err != nil || sh != nil || !strings.Contains(strings.Join(args, " "), "--"+act.flag) {
					t.Errorf("%s %s expanded to %v, %v, %v", s.name, a, args, sh, err)
				}
			}
		})
	}
	// A task starting with a subcommand word is still a task.
	stdinIsTerminal = func() bool { return true }
	for _, args := range [][]string{{"-t", "memory leaks in the parser"}, {"memory", "leaks"}} {
		if got, sh, _ := expandSubcommand(args); sh != nil || !reflect.DeepEqual(got, args) {
			t.Errorf("%v rewritten to %v (help %v)", args, got, sh != nil)
		}
	}
}
