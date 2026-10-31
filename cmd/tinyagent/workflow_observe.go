package main

import (
	"fmt"
	"io"
	"time"

	"github.com/vicgupta/tinyagent/internal/eventlog"
	"github.com/vicgupta/tinyagent/internal/harness"
	"github.com/vicgupta/tinyagent/internal/observer"
)

// observeWorkflow runs the --workflow-observe step after a workflow run:
// a failed or blocked run gets one proposed project rule, and a failure
// that recurs is flagged. Problems are warnings; they never change the
// run's exit code.
func observeWorkflow(res *harness.Result, cwd string, jl *eventlog.JSONL, stderr io.Writer) {
	ob, err := observer.Observe(res, cwd, eventlog.EnvRedactor().Redact, time.Now())
	for _, w := range ob.Warnings {
		fmt.Fprintln(stderr, "warning:", w)
	}
	if err != nil {
		fmt.Fprintln(stderr, "warning: observer:", err)
		return
	}
	if !ob.Observed {
		return
	}
	emit := func(kind string, f map[string]any) {
		if jl != nil {
			jl.Emit(kind, f)
		}
	}
	if p := ob.Proposal; p != nil {
		fmt.Fprintf(stderr, "observer: proposed project rule %s for stage %s in %s; apply it with:\n  %s\n", p.ID, p.Stage, observer.ProposalsPath(cwd), p.Command)
		emit("observer.proposed", map[string]any{"id": p.ID, "signature": p.Signature, "stage": p.Stage, "status": p.Status,
			"rule": p.Rule, "incident": p.Incident, "suggested_command": p.Command, "file": observer.ProposalsPath(cwd)})
		return
	}
	e := ob.Entry
	held := "no project rule from proposal " + e.ArtifactID + " has been applied yet"
	if ob.RuleID != "" {
		held = "project rule " + ob.RuleID + " was applied for it"
	}
	fmt.Fprintf(stderr, "warning: observer: gate did not hold: the same failure at stage %s has now happened %d times (first seen %s); %s\n", e.Stage, e.Count, e.FirstSeen, held)
	f := map[string]any{"signature": e.Signature, "stage": e.Stage, "count": e.Count, "first_seen": e.FirstSeen, "artifact_id": e.ArtifactID}
	if ob.RuleID != "" {
		f["rule_id"] = ob.RuleID
	}
	emit("observer.recurrence", f)
}
