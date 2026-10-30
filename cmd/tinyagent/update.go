package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/vicgupta/tinyagent/internal/update"
)

// runUpdate runs --update with step-by-step progress on stderr (a '#' bar
// while downloading when stderr is a terminal) and the final message on
// stdout. With --json, steps and the result are JSONL events on stdout and
// stderr gets nothing but errors.
func runUpdate(u *update.Updater, jsonOut bool, stdout, stderr io.Writer, tty bool) int {
	if jsonOut {
		u.Report = &update.JSONReporter{W: stdout}
	} else {
		u.Report = &update.TextReporter{W: stderr, Bar: tty}
	}
	msg, err := u.Run(context.Background())
	if jsonOut {
		ev := map[string]string{"type": "update.result", "status": "ok", "message": msg}
		if err != nil {
			ev["status"], ev["message"] = "error", err.Error()
		}
		b, _ := json.Marshal(ev)
		fmt.Fprintf(stdout, "%s\n", b)
	}
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	if !jsonOut {
		fmt.Fprintln(stdout, msg)
	}
	return 0
}
