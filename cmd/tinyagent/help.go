package main

import (
	"flag"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
)

// helpBrief is the default --help output (issue #164): usage, one sentence,
// and a pointer to the detailed view.
const helpBrief = `Usage: tinyagent -t "<prompt>" [flags]
Runs an AI coding agent on a task in --cwd, using tools to read, edit and run code.
Run tinyagent --help --verbose for all options.
`

// helpWidth is the column at which verbose descriptions wrap.
const helpWidth = 100

// maxDefaultListLines is the longest option list shown in default mode
// before it is cut to truncatedListLines plus an "…and N more" line.
const (
	maxDefaultListLines = 10
	truncatedListLines  = 5
)

// toolsGroup lists the tool and feature flags that usageGroups leaves out,
// so --help --verbose shows every flag.
var toolsGroup = struct {
	name  string
	flags [][3]string
}{"Tools", [][3]string{{"", "apply-patch", ""}, {"", "image", "path"}, {"", "images", ""}, {"", "lsp", ""}, {"", "no-skills", ""}, {"", "plan", ""}, {"", "plan-file", "path"}, {"", "redact-secrets", ""}, {"", "skills", ""}, {"", "subagents", ""}, {"", "tool-output-files", ""}, {"", "web-fetch", ""}, {"", "web-fetch-allow-domain", "host"}}}

// evalGroup lists the --eval flags; --dry-run is shared with Rewind.
var evalGroup = struct {
	name  string
	flags [][3]string
}{"Eval", [][3]string{{"", "eval", "suite.yaml"}, {"", "eval-out", "dir"}, {"", "eval-keep", ""}, {"", "eval-min-pass-rate", "rate"}}}

// verboseGroups is usageGroups with toolsGroup before Output and evalGroup
// after Rewind.
func verboseGroups() []struct {
	name  string
	flags [][3]string
} {
	var gs []struct {
		name  string
		flags [][3]string
	}
	for _, g := range usageGroups {
		if g.name == "Output" {
			gs = append(gs, toolsGroup)
		}
		gs = append(gs, g)
		if g.name == "Rewind" {
			gs = append(gs, evalGroup)
		}
	}
	return gs
}

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
	fmt.Fprint(w, helpBrief)
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
