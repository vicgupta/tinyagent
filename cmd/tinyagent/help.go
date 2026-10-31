package main

import (
	"flag"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// helpIntro is the usage line and one-sentence description that start
// every top-level --help view.
const helpIntro = `Usage: tinyagent [subcommand] -t "<prompt>" [flags]
Runs an AI coding agent on a task in --cwd, using tools to read, edit and run code.
`

// helpFooter points at the detailed views and the full reference.
const helpFooter = `Run tinyagent <subcommand> --help for its actions and options,
tinyagent --help --verbose for every flag, or see docs/subcommands.md.
`

// helpBrief is the default --help output: usage, one sentence, every
// subcommand with a one-line description (built from subcommands, so none
// can be left out), and pointers to the detailed views.
var helpBrief = helpIntro + "\n" + helpEssentials + helpSubcommands() + "\n" + helpFooter

// helpEssentials is the brief help's Examples and Common options: how to
// pass the task, pick the model and provider, and the most-used flags.
// TestBriefHelpShowsEssentials checks each flag named here exists.
var helpEssentials = `Examples:
  tinyagent -t "fix the failing test in ./pkg"
  git diff | tinyagent -t - -m anthropic/claude-sonnet-4-5 --read-only

Common options:
  -t, --task string    the task/prompt; "-" reads it from stdin
  -m, --model string   provider/model: openai, anthropic, openrouter or ollama
                       (default $AGENT_MODEL, else config model, else ` + builtinModel + `)
  -c, --config path    YAML config (default: ./tinyagent.yaml in --cwd)
      --cwd dir        working directory (default .)
      --resume path    continue or follow up a run saved with -o <path>
      --read-only      only read files; no edits or commands
  -o, --output path    write the trajectory JSON
      --json           write run events as JSONL to stdout
      --check          show resolved config, model and key status; no model call
      --update         update to the latest release, showing each step
  -V, --version        print the version

`

// helpHeader is helpIntro plus the Subcommands section; --help --verbose
// prints it before the flag sections and helpFooter after them.
var helpHeader = buildHelpHeader()

func buildHelpHeader() string {
	return helpIntro + "\n" + helpSubcommands()
}

// helpSubcommands is the Subcommands section: every subcommand with its
// one-line description.
func helpSubcommands() string {
	var b strings.Builder
	b.WriteString("Subcommands:\n")
	width := 0
	for _, s := range subcommands {
		width = max(width, len(s.name))
	}
	for _, s := range subcommands {
		fmt.Fprintf(&b, "  %-*s   %s\n", width, s.name, s.brief)
	}
	return b.String()
}

// helpWidth is the column at which verbose descriptions wrap.
const helpWidth = 100

// maxDefaultListLines is the longest option list shown in default mode
// before it is cut to truncatedListLines plus an "…and N more" line.
const (
	maxDefaultListLines = 10
	truncatedListLines  = 5
)

type flagGroup = struct {
	name  string
	flags [][3]string
}

// helpGroups lays out --help --verbose: each flag's shorthand, long name and
// value placeholder, by section. Help text and defaults come from the
// FlagSet. TestVerboseHelp checks that every registered flag is listed.
var helpGroups = []flagGroup{
	{"", [][3]string{{"h", "help", ""}}},
	{"Task", [][3]string{{"t", "task", "string"}, {"", "cwd", "dir"}, {"", "worktree", "[=name]"}, {"", "resume", "path"}, {"", "ask-user", ""}, {"", "acp", ""}, {"", "image", "path"}, {"", "images", ""}, {"", "untrusted-task", ""}}},
	{"Model and provider", [][3]string{{"m", "model", "string"}, {"", "reasoning-effort", "level"}, {"", "fallback-model", "spec"}, {"", "no-fallback", ""}, {"", "aux-model", "spec"}, {"", "delegate-model", "spec"}, {"", "route", "task=spec"}, {"", "no-routing", ""}, {"", "openai-api", "api"}, {"c", "config", "path"}, {"", "profile", "name"}}},
	{"Run control", [][3]string{{"", "step-limit", "n"}, {"", "cost-limit", "usd"}, {"", "token-limit", "n"}, {"", "time-limit", "sec"}, {"", "max-format-errors", "n"}, {"", "max-output-tokens", "n"}, {"", "no-compact", ""}, {"", "subagents", ""}, {"", "workers", ""}}},
	{"Verify before stop", verifyGroup.flags},
	{"Attempts (best of N)", attemptsGroup.flags},
	{"Review", [][3]string{{"", "review", ""}, {"", "review-base", "ref"}, {"", "review-commit", "rev"}}},
	{"Output and logging", [][3]string{{"o", "output", "path"}, {"v", "verbose", ""}, {"", "no-stream", ""}, {"", "json", ""}, {"", "output-schema", "path"}, {"", "log-db", "[=path]"}, {"", "otel", ""}, {"", "tool-output-files", ""}, {"", "redact-secrets", ""}, {"", "transcript", "path"}}},
	{"Sandbox and permissions", [][3]string{{"", "read-only", ""}, {"", "add-dir-read", "path"}, {"", "add-dir-write", "path"}, {"", "sandbox", ""}, {"", "sandbox-network", ""}, {"", "sandbox-backend", "name"}, {"", "sandbox-allow-domain", "host"}, {"", "env", "name"}, {"", "command-env", "mode"}, {"", "lockdown", ""}, {"", "web-fetch", ""}, {"", "web-fetch-allow-domain", "host"}}},
	{"Context and tools", [][3]string{{"", "no-agents-md", ""}, {"", "scoped-agents-md", ""}, {"", "skills", ""}, {"", "no-skills", ""}, {"", "apply-patch", ""}, {"", "lsp", ""}, {"", "snapshots", ""}}},
	ratchetGroup,
	{"Memory and planner", [][3]string{{"", "memory", ""}, {"", "memory-approve", "id|all"}, {"", "memory-forget", "id|all"}, {"", "memory-help", ""}, {"", "memory-list", ""}, {"", "memory-path", ""}, {"", "memory-yes", ""}, {"", "handoff", ""}, {"", "plan", ""}, {"", "plan-file", "path"}, {"", "plan-first", ""}}},
	{"Plugins and TinyGo", [][3]string{{"", "list-plugins", ""}, {"", "plugins-prune", ""}, {"", "plugins-keep", "n"}, {"", "plugins-rollback", "module@sha256"}, {"", "tinygo-install", ""}, {"", "tinygo-update", ""}, {"", "tinygo-status", ""}}},
	{"Rewind", [][3]string{{"", "rewind", "path"}, {"", "to", "target"}, {"", "dry-run", ""}}},
	{"Workflow and eval", [][3]string{{"", "workflow", "file.yaml"}, {"", "workflow-name", "name"}, {"", "skills-validate", "path"}, {"", "eval", "suite.yaml"}, {"", "eval-out", "dir"}, {"", "eval-keep", ""}, {"", "eval-min-pass-rate", "rate"}}},
	{"Setup and info", [][3]string{{"V", "version", ""}, {"", "update", ""}, {"", "check", ""}, {"", "list-profiles", ""}, {"", "init-config", ""}, {"", "init-force", ""}, {"", "init-migrate", ""}, {"", "trust-config", ""}, {"", "completion", "shell"}}},
}

// verboseGroups returns the --help --verbose sections.
func verboseGroups() []flagGroup { return helpGroups }

// printHelp writes --help: the short brief, or with verbose (--help
// --verbose / -h -v) the grouped per-option view.
func printHelp(w io.Writer, fs *flag.FlagSet, verbose bool) {
	if !verbose {
		fmt.Fprint(w, helpBrief)
		return
	}
	printVerboseHelp(w, fs)
}

// truncateList shortens an option list shown in default mode: over
// maxDefaultListLines lines it keeps the first truncatedListLines and adds
// a line naming exactly how many were hidden.
func truncateList(lines []string) []string {
	if len(lines) <= maxDefaultListLines {
		return lines
	}
	out := append([]string{}, lines[:truncatedListLines]...)
	return append(out, fmt.Sprintf("…and %d more. Run tinyagent --help --verbose to see all.", len(lines)-truncatedListLines))
}

// printVerboseHelp prints the brief, then every flag under its group
// heading, sorted by long name within the group, one blank line between
// groups, descriptions aligned in one column and wrapped lines indented two
// spaces past that column.
func printVerboseHelp(w io.Writer, fs *flag.FlagSet) {
	left := func(f [3]string) string {
		s := "      --" + f[1]
		if f[0] != "" {
			s = "  -" + f[0] + ", --" + f[1]
		}
		if f[2] != "" {
			s += " " + f[2]
		}
		return s
	}
	fmt.Fprint(w, helpHeader)
	for _, g := range verboseGroups() {
		flags := append([][3]string{}, g.flags...)
		sort.Slice(flags, func(i, j int) bool { return flags[i][1] < flags[j][1] })
		width := 0
		for _, f := range flags {
			width = max(width, len(left(f)))
		}
		col := width + 3
		fmt.Fprintln(w)
		if g.name != "" {
			fmt.Fprintf(w, "%s:\n", g.name)
		}
		for _, f := range flags {
			fl := fs.Lookup(f[1])
			help := fl.Usage
			if d := fl.DefValue; d != "" && d != "0" && d != "false" {
				if _, err := strconv.Atoi(d); err == nil {
					help += fmt.Sprintf(" (default: %s)", d)
				} else {
					help += fmt.Sprintf(" (default: %q)", d)
				}
			}
			lines := wrapWords(help, max(helpWidth-col, 30))
			fmt.Fprintf(w, "%-*s%s\n", col, left(f), lines[0])
			for _, l := range lines[1:] {
				fmt.Fprintf(w, "%s%s\n", strings.Repeat(" ", col+2), l)
			}
		}
	}
	fmt.Fprintln(w)
	fmt.Fprint(w, helpFooter)
}

// wrapWords splits s into lines of at most width characters at spaces
// (continuation lines get two fewer, for their extra indent); a word longer
// than the width gets a line of its own.
func wrapWords(s string, width int) []string {
	var lines []string
	cur := ""
	for _, word := range strings.Fields(s) {
		limit := width
		if len(lines) > 0 {
			limit = width - 2
		}
		if cur != "" && len(cur)+1+len(word) > limit {
			lines = append(lines, cur)
			cur = word
			continue
		}
		if cur == "" {
			cur = word
		} else {
			cur += " " + word
		}
	}
	return append(lines, cur)
}
