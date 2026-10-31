package eventlog

import (
	"path/filepath"
	"testing"
)

func TestResultStoresUsageByRole(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.db")
	s := openTest(t, Options{Path: path, Run: RunInfo{Model: "openai/x", Task: "hi"}})
	s.Emit("result", map[string]any{"status": "Submitted", "usage_by_role": []map[string]any{
		{"role": "main", "task": "main", "model": "openai/x", "requests": 2, "input_tokens": 10, "output_tokens": 4, "cost_usd": 0.2},
		{"role": "delegated", "task": "worker", "model": "openai/y", "requests": 1, "input_tokens": 3, "output_tokens": 1, "cost_usd": 0.01},
	}})
	s.Close()
	db := rawDB(t, path)
	if n := count(t, db, `SELECT COUNT(*) FROM run_usage`); n != 2 {
		t.Fatalf("run_usage rows = %d", n)
	}
	if n := count(t, db, `SELECT requests FROM run_usage WHERE role = 'delegated' AND task = 'worker' AND model = 'openai/y'`); n != 1 {
		t.Errorf("worker requests = %d", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM run_usage u JOIN runs r ON r.id = u.run_id`); n != 2 {
		t.Errorf("rows not linked to the run: %d", n)
	}
}
