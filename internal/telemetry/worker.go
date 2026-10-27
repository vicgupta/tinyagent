package telemetry

// Worker subagents (the worker tool) get an "invoke_agent worker" span
// carrying their branch and commits. The worktree path is left out: the
// branch and commits identify the change without exporting local paths.

func setWorkerStarted(sp *span, fields map[string]any) {
	sp.set(attrSubagentKind, "worker")
	if b := str(fields["branch"]); b != "" {
		sp.set(attrWorkerBranch, b)
	}
	if c := str(fields["base_commit"]); c != "" {
		sp.set(attrWorkerBaseCommit, c)
	}
}

func setWorkerFinished(sp *span, fields map[string]any) {
	if c := str(fields["commit"]); c != "" {
		sp.set(attrWorkerCommit, c)
	}
	if _, ok := fields["files_changed"]; ok {
		sp.set(attrWorkerFilesChange, fields["files_changed"])
	}
}
