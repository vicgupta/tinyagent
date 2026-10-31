package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/vicgupta/tinyagent/internal/triage"
)

// triageConflicts are flags that start a run or another mode; --triage is an
// offline report and runs no model.
var triageConflicts = []string{"t", "task", "resume", "rewind", "workflow", "eval", "interactive", "i",
	"acp", "check", "review", "worktree", "update", "init-config", "attempts"}

// triageNow is the clock for stale checks; tests replace it.
var triageNow = time.Now

// runTriage ranks the issues in --triage and writes the next-up document to
// --triage-out (or stdout) and a Markdown table to stderr. Usage errors and
// unreadable or malformed input exit 2; a failed write exits 1.
func runTriage(o *options, fs *flag.FlagSet, stdin io.Reader, stdout, stderr io.Writer) int {
	for _, f := range triageConflicts {
		if fs.Lookup(f) != nil && flagWasSet(fs, f) {
			fmt.Fprintf(fs.Output(), "error: --triage can't be combined with -%s: it ranks issues offline and runs no task\n", dashName(f))
			return 2
		}
	}
	if o.triageStaleDays < 0 {
		fmt.Fprintln(fs.Output(), "error: --triage-stale-days must be 0 or more")
		return 2
	}
	var (
		b   []byte
		err error
	)
	if o.triage == "-" {
		b, err = io.ReadAll(stdin)
	} else {
		b, err = os.ReadFile(o.triage)
	}
	if err != nil {
		fmt.Fprintln(fs.Output(), "error: --triage:", err)
		return 2
	}
	issues, err := triage.Decode(b)
	if err != nil {
		fmt.Fprintf(fs.Output(), "error: --triage %s: %v\n", o.triage, err)
		return 2
	}
	opts := triage.Options{StaleDays: o.triageStaleDays}
	ranked := triage.Rank(issues, triageNow(), opts)
	doc, err := triage.Encode(ranked, opts)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	if o.triageOut != "" {
		if err := os.WriteFile(o.triageOut, doc, 0o644); err != nil {
			fmt.Fprintln(stderr, "error: --triage-out:", err)
			return 1
		}
	} else if _, err := stdout.Write(doc); err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	fmt.Fprint(stderr, triage.Markdown(ranked))
	fmt.Fprintf(stderr, "%d issues ranked\n", len(ranked))
	return 0
}
