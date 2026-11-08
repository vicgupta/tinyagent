package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestGoalSubcommandComposesFlags(t *testing.T) {
	cases := []struct{ sub, flags []string }{
		{
			[]string{"goal", "ship the feature"},
			[]string{"--plan", "--plan-first", "--verify-on-stop", "--task", "ship the feature"},
		},
		{
			[]string{"goal", "-t", "ship the feature"},
			[]string{"--plan", "--plan-first", "--verify-on-stop", "-t", "ship the feature"},
		},
		{
			[]string{"goal", "ship the feature", "--check", "go test ./..."},
			[]string{"--plan", "--plan-first", "--verify-on-stop", "--task", "ship the feature", "--verify-check", "go test ./..."},
		},
		{
			[]string{"goal", "--check", "go test ./...", "-t", "ship the feature"},
			[]string{"--plan", "--plan-first", "--verify-on-stop", "--verify-check", "go test ./...", "-t", "ship the feature"},
		},
		{
			[]string{"goal", "--check", "go test ./...", "ship the feature"},
			[]string{"--plan", "--plan-first", "--verify-on-stop", "--verify-check", "go test ./...", "--task", "ship the feature"},
		},
		{
			[]string{"goal", "--check", "go test ./...", "--", "ship", "the", "feature"},
			[]string{"--plan", "--plan-first", "--verify-on-stop", "--verify-check", "go test ./...", "-t", "ship the feature"},
		},
		{
			[]string{"goal", "--check", "go vet ./...", "--check", "go test ./...", "-t", "x"},
			[]string{"--plan", "--plan-first", "--verify-on-stop", "--verify-check", "go vet ./...", "--verify-check", "go test ./...", "-t", "x"},
		},
		{
			[]string{"goal", "--handoff", "-t", "x"},
			[]string{"--plan", "--plan-first", "--verify-on-stop", "--handoff", "-t", "x"},
		},
		{
			[]string{"goal", "-i"},
			[]string{"--plan", "--plan-first", "--verify-on-stop", "-i"},
		},
	}
	for _, c := range cases {
		if a, b := parsedFlags(t, c.sub...), parsedFlags(t, c.flags...); !reflect.DeepEqual(a, b) {
			t.Errorf("%v = %v, want %v (%v)", c.sub, a, b, c.flags)
		}
	}
}

func TestGoalSubcommandHelp(t *testing.T) {
	for _, args := range [][]string{{"goal", "--help"}, {"goal", "-h"}, {"goal", "help"}, {"help", "goal"}} {
		code, out, _ := runWith(t, args...)
		if code != 0 || !strings.HasPrefix(out, "Usage: tinyagent goal") {
			t.Errorf("%v: exit %d, %q", args, code, out)
		}
		for _, want := range []string{
			"--plan --plan-first --verify-on-stop",
			"--check",
			"--verify-check",
			"docs/subcommands.md",
			"<goal>",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("%v: help lacks %q:\n%s", args, want, out)
			}
		}
	}
}

func TestGoalCheckRequiresValue(t *testing.T) {
	if _, _, err := expandSubcommand([]string{"goal", "--check"}); err == nil || !strings.Contains(err.Error(), "needs a value") {
		t.Fatalf("goal --check without a command: %v", err)
	}
}

func TestGoalExtraPositionalRejected(t *testing.T) {
	if _, _, err := expandSubcommand([]string{"goal", "one goal", "another"}); err == nil || !strings.Contains(err.Error(), "unexpected argument") {
		t.Fatalf("two positional goals: %v", err)
	}
}
