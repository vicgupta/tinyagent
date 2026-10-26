package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/vicgupta/tinyagent/internal/eventlog"
	"github.com/vicgupta/tinyagent/internal/trajectory"
	"github.com/vicgupta/tinyagent/internal/transcript"
)

// transcriptConflicts are the run and mode flags --transcript refuses.
var transcriptConflicts = []string{"task", "t", "resume", "rewind", "to", "dry-run", "check", "init-config", "update",
	"completion", "json", "output", "o", "review", "review-base", "review-commit", "worktree", "snapshots", "log-db", "otel"}

// runTranscript implements --transcript: it renders a saved trajectory as
// Markdown on stdout. It reads no config, needs no API key, makes no model
// or network request and writes no file. Every error exits 2 with nothing
// on stdout.
func runTranscript(o *options, fs *flag.FlagSet, w io.Writer) int {
	for _, name := range transcriptConflicts {
		if flagWasSet(fs, name) {
			dash := "--"
			if len(name) == 1 {
				dash = "-"
			}
			fmt.Fprintf(os.Stderr, "error: --transcript only reads a trajectory; it can't be combined with %s%s\n", dash, name)
			return 2
		}
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "error: unexpected positional argument %q\n", fs.Arg(0))
		return 2
	}
	t, err := trajectory.Read(o.transcript)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: transcript: %s: %v\n", o.transcript, err)
		return 2
	}
	var b bytes.Buffer
	if err := transcript.Render(&b, t, transcript.Options{Redactor: eventlog.EnvRedactor()}); err != nil {
		fmt.Fprintln(os.Stderr, "error: transcript:", err)
		return 1
	}
	if _, err := w.Write(b.Bytes()); err != nil {
		fmt.Fprintln(os.Stderr, "error: transcript:", err)
		return 1
	}
	return 0
}
