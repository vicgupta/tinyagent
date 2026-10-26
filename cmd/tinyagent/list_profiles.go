package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/vicgupta/tinyagent/internal/config"
)

// listProfilesConflicts are the task, profile and mode flags --list-profiles
// refuses, so discovery never starts a run, makes a request or writes a file.
var listProfilesConflicts = []string{
	"task", "t", "profile", "check", "review", "review-base", "review-commit",
	"resume", "rewind", "to", "dry-run", "acp", "transcript", "completion",
	"init-config", "update", "worktree",
}

// runListProfiles implements --list-profiles: it prints the profile names in
// the selected config file, sorted lexicographically, one per line on stdout,
// and exits without reading credentials, making a model or network request,
// starting hooks/MCP servers/processes or writing anything. Every error (a
// missing or unreadable file, malformed YAML, or an invalid profiles:
// section) exits 2 with a file-qualified diagnostic and no partial list.
func runListProfiles(o *options, fs *flag.FlagSet, w io.Writer) int {
	if o.showVersion {
		fmt.Fprintln(w, "tinyagent", currentVersion())
		return 0
	}
	for _, name := range listProfilesConflicts {
		if flagWasSet(fs, name) {
			dash := "--"
			if len(name) == 1 {
				dash = "-"
			}
			fmt.Fprintf(os.Stderr, "error: --list-profiles only lists profile names; it can't be combined with %s%s\n", dash, name)
			return 2
		}
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "error: unexpected positional argument %q\n", fs.Arg(0))
		return 2
	}
	names, err := config.ProfileNames(config.Find(o.configPath))
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	for _, n := range names {
		fmt.Fprintln(w, n)
	}
	return 0
}
