package eventlog

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

// fakeClock returns a clock that advances by step on every call.
func fakeClock(step time.Duration) func() time.Time {
	t := time.UnixMilli(1_700_000_000_000)
	return func() time.Time {
		t = t.Add(step)
		return t
	}
}

func emitSubagents(s *SQLite) {
	s.Emit("run.started", map[string]any{"schema_version": 1, "model": "fake"})
	s.Emit("subagent.started", map[string]any{"id": "d1", "tool_call_id": "c1", "model": "openai/mini", "parent_step": 1, "label": "look"})
	s.Emit("subagent.finished", map[string]any{"id": "d1", "status": "Submitted", "steps": 2, "input_tokens": 5, "output_tokens": 1, "cost_usd": 0.01})
	s.Emit("subagent.started", map[string]any{"id": "w1", "kind": "worker", "tool_call_id": "c2", "model": "openai/mini", "parent_step": 2,
		"branch": "tinyagent/worker-w1", "worktree": "/tmp/wt", "base_commit": "base111"})
	s.Emit("subagent.started", map[string]any{"id": "w2", "kind": "worker", "tool_call_id": "c3", "model": "openai/mini", "parent_step": 2,
		"branch": "tinyagent/worker-w2", "worktree": "/tmp/wt2", "base_commit": "base111"})
	s.Emit("subagent.finished", map[string]any{"id": "w2", "kind": "worker", "status": "Submitted", "steps": 1, "input_tokens": 2, "output_tokens": 1, "cost_usd": 0.0, "files_changed": 0})
	s.Emit("subagent.finished", map[string]any{"id": "w1", "kind": "worker", "status": "Submitted", "steps": 3, "input_tokens": 7, "output_tokens": 2, "cost_usd": 0.02,
		"files_changed": 2, "branch": "tinyagent/worker-w1", "commit": "abc123"})
	s.Emit("worker.applied", map[string]any{"id": "w1", "branch": "tinyagent/worker-w1", "commit": "abc123", "files_changed": 2})
	s.Emit("worker.applied", map[string]any{"id": "w9", "branch": "b", "commit": "c", "files_changed": 1}) // from an earlier invocation
	s.Emit("result", map[string]any{"status": "Submitted", "steps": 3})
}

type subRow struct {
	kind, toolCall, model, status string
	branch, base, commit, label   sql.NullString
	parentStep, steps, in, out    int
	files, dur, ended             sql.NullInt64
	applied                       int
	cost                          float64
}

func readSub(t *testing.T, db *sql.DB, id string) subRow {
	t.Helper()
	var r subRow
	err := db.QueryRow(`SELECT kind, tool_call_id, model, status, branch, base_commit, commit_sha, label, parent_step, steps, input_tokens, output_tokens, files_changed, duration_ms, ended_at, applied, cost_usd FROM subagents WHERE subagent_id = ?`, id).
		Scan(&r.kind, &r.toolCall, &r.model, &r.status, &r.branch, &r.base, &r.commit, &r.label, &r.parentStep, &r.steps, &r.in, &r.out, &r.files, &r.dur, &r.ended, &r.applied, &r.cost)
	if err != nil {
		t.Fatalf("subagent %s: %v", id, err)
	}
	return r
}

func TestSubagentRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.db")
	s := openTest(t, Options{Path: path, Now: fakeClock(time.Millisecond)})
	emitSubagents(s)
	s.Close()
	db := rawDB(t, path)
	if n := count(t, db, `SELECT COUNT(*) FROM subagents WHERE run_id = ?`, s.RunID()); n != 3 {
		t.Fatalf("subagents rows = %d, want 3", n)
	}

	d := readSub(t, db, "d1")
	if d.kind != "delegate" || d.toolCall != "c1" || d.model != "openai/mini" || d.status != "Submitted" || d.steps != 2 || d.in != 5 || d.cost != 0.01 ||
		d.label.String != "look" || d.branch.Valid || d.commit.Valid || d.files.Valid || d.applied != 0 || !d.dur.Valid || d.dur.Int64 <= 0 {
		t.Errorf("delegate row %+v", d)
	}

	w1 := readSub(t, db, "w1")
	if w1.kind != "worker" || w1.branch.String != "tinyagent/worker-w1" || w1.base.String != "base111" || w1.commit.String != "abc123" ||
		w1.files.Int64 != 2 || w1.applied != 1 || w1.steps != 3 || w1.parentStep != 2 || !w1.ended.Valid || !w1.dur.Valid {
		t.Errorf("worker w1 row %+v", w1)
	}
	// w2 changed nothing: its branch was removed, so no commit, not applied.
	w2 := readSub(t, db, "w2")
	if w2.kind != "worker" || w2.commit.Valid || !w2.files.Valid || w2.files.Int64 != 0 || w2.applied != 0 || w2.status != "Submitted" {
		t.Errorf("worker w2 row %+v", w2)
	}
	// The raw events are still logged alongside the rows.
	if n := count(t, db, `SELECT COUNT(*) FROM events WHERE type IN ('subagent.started', 'subagent.finished', 'worker.applied')`); n != 8 {
		t.Errorf("subagent events = %d, want 8", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM events WHERE payload LIKE '%"worktree":"/tmp/wt"%'`); n != 1 {
		t.Errorf("worktree path missing from the started event payload")
	}
}

func TestSubagentLabelRedacted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.db")
	s := openTest(t, Options{Path: path, Redactor: NewValueRedactor([]string{"tok-SECRET-123456"}, false)})
	s.Emit("subagent.started", map[string]any{"id": "w1", "kind": "worker", "label": "use tok-SECRET-123456"})
	s.Close()
	db := rawDB(t, path)
	var label string
	if err := db.QueryRow(`SELECT label FROM subagents`).Scan(&label); err != nil {
		t.Fatal(err)
	}
	if label != "use "+Redacted {
		t.Errorf("label = %q", label)
	}
}

func TestRetentionPrunesSubagents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.db")
	for range 3 {
		s := openTest(t, Options{Path: path, MaxRuns: 2})
		emitSubagents(s)
		s.Close()
	}
	db := rawDB(t, path)
	if n := count(t, db, `SELECT COUNT(*) FROM subagents`); n != 6 {
		t.Errorf("subagents rows = %d, want 6 (two runs kept)", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM subagents WHERE run_id NOT IN (SELECT id FROM runs)`); n != 0 {
		t.Errorf("%d orphaned subagents rows", n)
	}
}
