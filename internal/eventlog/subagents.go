package eventlog

import "time"

// subagentOps returns the subagents-table writes for a subagent.started,
// subagent.finished or worker.applied event (delegate and worker
// subagents alike), or nil for any other event.
func (s *SQLite) subagentOps(kind string, fields map[string]any, now time.Time) []op {
	id := str(fields["id"])
	if id == "" {
		return nil
	}
	switch kind {
	case "subagent.started":
		k := str(fields["kind"])
		if k == "" {
			k = "delegate"
		}
		var label any
		if l := str(fields["label"]); l != "" {
			label = truncate(s.opts.Redactor.Redact(l), resultSummaryBytes)
		}
		s.subStart[id] = now
		return []op{{`INSERT INTO subagents (run_id, subagent_id, kind, tool_call_id, parent_step, label, model, branch, base_commit, started_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			[]any{s.runID, id, k, nullStr(fields["tool_call_id"]), num(fields["parent_step"]), label, nullStr(fields["model"]),
				nullStr(fields["branch"]), nullStr(fields["base_commit"]), now.UnixMilli()}}}
	case "subagent.finished":
		var dur any
		if start, ok := s.subStart[id]; ok {
			dur = now.Sub(start).Milliseconds()
			delete(s.subStart, id)
		}
		return []op{{`UPDATE subagents SET status = ?, steps = ?, input_tokens = ?, output_tokens = ?, cost_usd = ?, files_changed = COALESCE(?, files_changed), branch = COALESCE(?, branch), commit_sha = ?, ended_at = ?, duration_ms = ? WHERE id = (SELECT id FROM subagents WHERE run_id = ? AND subagent_id = ? AND ended_at IS NULL ORDER BY id DESC LIMIT 1)`,
			[]any{str(fields["status"]), num(fields["steps"]), num(fields["input_tokens"]), num(fields["output_tokens"]), fields["cost_usd"],
				num(fields["files_changed"]), nullStr(fields["branch"]), nullStr(fields["commit"]), now.UnixMilli(), dur, s.runID, id}}}
	case "worker.applied":
		// A worker started by an earlier invocation (before --resume) has
		// no row in this run, so the update may match nothing.
		return []op{{`UPDATE subagents SET applied = 1 WHERE id = (SELECT id FROM subagents WHERE run_id = ? AND subagent_id = ? AND kind = 'worker' ORDER BY id DESC LIMIT 1)`,
			[]any{s.runID, id}}}
	}
	return nil
}

func nullStr(v any) any {
	if s := str(v); s != "" {
		return s
	}
	return nil
}
