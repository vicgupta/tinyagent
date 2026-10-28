package eventlog

import (
	"path/filepath"
	"testing"
	"time"
)

func TestAttemptGroupAndLinkedRuns(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".tinyagent", "tinyagent.db")
	start := time.UnixMilli(1000)
	if err := StartAttemptGroup(path, AttemptGroup{ID: "grp", Cwd: "/repo", Requested: 2, TraceID: "t1", Started: start}); err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 2; i++ {
		s := openTest(t, Options{Path: path})
		s.Emit("run.started", map[string]any{"attempt": map[string]any{"attempts_id": "grp", "index": i, "count": 2}})
		s.Emit("result", map[string]any{"status": "completed"})
		s.Close()
	}
	other := openTest(t, Options{Path: path})
	other.Emit("run.started", map[string]any{"model": "m"})
	other.Close()
	w := 2
	if err := FinishAttemptGroup(path, "grp", AttemptGroupResult{Run: 2, Winner: &w, WinnerBranch: "tinyagent/w-a2", StopReason: "passed", CostUSD: 0.5, WallMS: 9, Ended: start.Add(time.Second)}); err != nil {
		t.Fatal(err)
	}
	db := rawDB(t, path)
	if n := count(t, db, `SELECT COUNT(*) FROM runs WHERE attempts_id = 'grp'`); n != 2 {
		t.Fatalf("linked runs = %d", n)
	}
	if n := count(t, db, `SELECT SUM(attempt) FROM runs WHERE attempts_id = 'grp'`); n != 3 {
		t.Fatalf("attempt numbers sum = %d", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM runs WHERE attempts_id IS NULL`); n != 1 {
		t.Fatalf("unlinked runs = %d", n)
	}
	if n := count(t, db, `SELECT winner FROM attempt_groups WHERE id = 'grp' AND stop_reason = 'passed' AND attempts_run = 2 AND trace_id = 't1' AND ended_at = 2000`); n != 2 {
		t.Fatalf("winner = %d", n)
	}
}
