package attempts

import (
	"bytes"
	"encoding/json"
	"io"
	"strconv"
)

// Event types written to Options.Events (--json with --attempts).
const (
	EventAttemptStarted = "attempt.started"
	EventAttemptCheck   = "attempt.check"
	EventAttemptResult  = "attempt.result"
	EventAttemptsResult = "attempts.result"
)

// eventWriter writes the attempts' own JSONL events. After the first
// failed write it stops writing.
type eventWriter struct {
	out io.Writer
	err error
}

func (e *eventWriter) emit(kind string, fields map[string]any) {
	if e == nil || e.out == nil || e.err != nil {
		return
	}
	event := make(map[string]any, len(fields)+1)
	for k, v := range fields {
		event[k] = v
	}
	event["type"] = kind
	line, err := json.Marshal(event)
	if err != nil {
		e.err = err
		return
	}
	_, e.err = e.out.Write(append(line, '\n'))
}

func attemptStartedFields(n, total int, model, worktree string) map[string]any {
	return map[string]any{"attempt": n, "attempts_requested": total, "model": model, "worktree_name": worktree}
}

// checkFields describes one check result. The command text goes through
// redact too, since a check command can embed a secret value.
func checkFields(n int, c CheckResult, redact func(string) string) map[string]any {
	cmd := c.Command
	if redact != nil {
		cmd = redact(cmd)
	}
	return map[string]any{"attempt": n, "command": cmd, "exit_code": c.ExitCode,
		"duration_ms": c.DurationMS, "timed_out": c.TimedOut, "skipped": c.Skipped, "output": c.Output}
}

func attemptResultFields(a Attempt) map[string]any {
	f := map[string]any{"attempt": a.Attempt, "model": a.Model, "status": a.Status, "passed": a.Passed,
		"steps": a.Steps, "input_tokens": a.InputTokens, "output_tokens": a.OutputTokens,
		"cost_usd": a.CostUSD, "wall_time_ms": a.WallTimeMS, "worktree": a.Worktree,
		"branch": a.Branch, "files_changed": a.FilesChanged}
	if a.Trajectory != "" {
		f["trajectory"] = a.Trajectory
	}
	if a.Error != "" {
		f["error"] = a.Error
	}
	return f
}

func attemptsResultFields(rep Report, reportPath string) map[string]any {
	removed := []int{}
	for _, a := range rep.Attempts {
		if a.Removed {
			removed = append(removed, a.Attempt)
		}
	}
	f := map[string]any{"schema_version": rep.SchemaVersion, "attempts_requested": rep.Requested,
		"attempts_run": len(rep.Attempts), "winner": rep.Winner, "stop_reason": rep.StopReason,
		"stop_on_pass": rep.StopOnPass, "keep": rep.Keep, "removed": removed,
		"total_cost_usd": rep.TotalCostUSD, "total_wall_time_ms": rep.TotalWallMS}
	if rep.AttemptsID != "" {
		f["attempts_id"] = rep.AttemptsID
	}
	if rep.WinnerBranch != "" {
		f["winner_branch"] = rep.WinnerBranch
	}
	if reportPath != "" {
		f["report"] = reportPath
	}
	return f
}

// tagWriter forwards a child run's JSONL stdout, adding "attempt": n to
// every line that is a JSON object. Other lines pass through unchanged.
type tagWriter struct {
	out     io.Writer
	attempt int
	buf     []byte
	err     error
}

func (t *tagWriter) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	for {
		i := bytes.IndexByte(t.buf, '\n')
		if i < 0 {
			break
		}
		line := t.buf[:i]
		t.writeLine(line)
		t.buf = t.buf[i+1:]
	}
	return len(p), nil
}

// Flush writes a final unterminated line, if any.
func (t *tagWriter) Flush() error {
	if len(t.buf) > 0 {
		t.writeLine(t.buf)
		t.buf = nil
	}
	return t.err
}

func (t *tagWriter) writeLine(line []byte) {
	if t.err != nil {
		return
	}
	out := tagLine(line, t.attempt)
	_, t.err = t.out.Write(append(out, '\n'))
}

// tagLine adds "attempt": n to a JSON object line, replacing any existing
// attempt field. A line that isn't a JSON object is returned unchanged.
func tagLine(line []byte, n int) []byte {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) < 2 || trimmed[0] != '{' {
		return append([]byte(nil), line...)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &obj); err != nil || obj == nil {
		return append([]byte(nil), line...)
	}
	obj["attempt"] = json.RawMessage(strconv.Itoa(n))
	b, err := json.Marshal(obj)
	if err != nil {
		return append([]byte(nil), line...)
	}
	return b
}
