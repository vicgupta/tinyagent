package eventlog

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func openTest(t *testing.T, opts Options) *SQLite {
	t.Helper()
	if opts.Path == "" {
		opts.Path = filepath.Join(t.TempDir(), ".tinyagent", "tinyagent.db")
	}
	s, err := Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func rawDB(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", DSN(path))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func count(t *testing.T, db *sql.DB, q string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

// dump returns every value stored in the database's tables as text.
func dump(t *testing.T, db *sql.DB) string {
	t.Helper()
	var b strings.Builder
	for _, table := range []string{"runs", "events", "tool_calls", "schema_migrations"} {
		rows, err := db.Query("SELECT * FROM " + table)
		if err != nil {
			t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			for _, v := range vals {
				fmt.Fprintf(&b, "%v|", v)
			}
			b.WriteString("\n")
		}
		rows.Close()
	}
	return b.String()
}

func emitRun(s *SQLite) {
	s.Emit("run.started", map[string]any{"schema_version": 1, "model": "fake"})
	s.Emit("assistant.message", map[string]any{"text": "BODY_ASSISTANT"})
	s.Emit("tool.call", map[string]any{"id": "c1", "name": "bash", "arguments": map[string]any{"command": "BODY_ARGS"}})
	s.Emit("tool.result", map[string]any{"id": "c1", "name": "bash", "output": "BODY_OUTPUT", "is_error": false})
	s.Emit("result", map[string]any{"status": "Submitted", "submission": "BODY_SUBMISSION", "steps": 2, "input_tokens": 10, "output_tokens": 5, "cost_usd": 0.5, "duration_ms": int64(1000)})
}

func TestFreshDatabaseHasSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "dir", "log.db")
	s := openTest(t, Options{Path: path, Run: RunInfo{Model: "openai/x", Task: "hi"}})
	emitRun(s)
	s.Close()
	db := rawDB(t, path)
	for _, table := range []string{"runs", "events", "tool_calls", "schema_migrations"} {
		if count(t, db, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?`, table) != 1 {
			t.Errorf("table %s missing", table)
		}
	}
	if v := count(t, db, `SELECT MAX(version) FROM schema_migrations`); v != SchemaVersion || SchemaVersion < 1 {
		t.Errorf("schema_migrations version = %d, want %d", v, SchemaVersion)
	}
	if v := count(t, db, `SELECT schema_version FROM runs`); v != SchemaVersion {
		t.Errorf("runs.schema_version = %d", v)
	}
	var mode string
	db.QueryRow(`PRAGMA journal_mode`).Scan(&mode)
	if mode != "wal" {
		t.Errorf("journal_mode = %q, want wal", mode)
	}
	var status string
	var steps, in int
	var cost float64
	if err := db.QueryRow(`SELECT status, steps, input_tokens, cost_usd FROM runs`).Scan(&status, &steps, &in, &cost); err != nil {
		t.Fatal(err)
	}
	if status != "Submitted" || steps != 2 || in != 10 || cost != 0.5 {
		t.Errorf("run = %s %d %d %v", status, steps, in, cost)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM tool_calls WHERE tool = 'bash' AND is_error = 0 AND duration_ms IS NOT NULL`); n != 1 {
		t.Errorf("tool_calls rows = %d", n)
	}
}

func TestOlderDatabaseMigratesForward(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.db")
	s := openTest(t, Options{Path: path})
	s.Close()
	db := rawDB(t, path)
	next := append(embeddedMigrations(), migration{SchemaVersion + 1, `ALTER TABLE runs ADD COLUMN extra TEXT;`})
	for range 2 { // the second pass is a no-op
		if err := migrate(context.Background(), db, next); err != nil {
			t.Fatal(err)
		}
	}
	if v := count(t, db, `SELECT MAX(version) FROM schema_migrations`); v != SchemaVersion+1 {
		t.Errorf("version = %d", v)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM pragma_table_info('runs') WHERE name = 'extra'`); n != 1 {
		t.Error("migration not applied")
	}
}

func TestMetadataLevelStoresNoBodies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.db")
	s := openTest(t, Options{Path: path, Run: RunInfo{Task: "BODY_TASK"}})
	emitRun(s)
	s.Close()
	db := rawDB(t, path)
	all := dump(t, db)
	if strings.Contains(all, "BODY_") {
		t.Fatalf("metadata level stored a body:\n%s", all)
	}
	var payload string
	db.QueryRow(`SELECT payload FROM events WHERE type = 'assistant.message'`).Scan(&payload)
	if payload != `{"text_len":14}` {
		t.Errorf("payload = %s", payload)
	}
	if n := count(t, db, `SELECT task_len FROM runs`); n != len("BODY_TASK") {
		t.Errorf("task_len = %d", n)
	}
}

func TestFullLevelStoresBodies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.db")
	s := openTest(t, Options{Path: path, Level: LevelFull, Run: RunInfo{Task: "BODY_TASK"}})
	emitRun(s)
	s.Close()
	all := dump(t, rawDB(t, path))
	for _, want := range []string{"BODY_TASK", "BODY_ASSISTANT", "BODY_ARGS", "BODY_OUTPUT", "BODY_SUBMISSION"} {
		if !strings.Contains(all, want) {
			t.Errorf("full level lost %s", want)
		}
	}
}

func TestSecretsAreRedacted(t *testing.T) {
	const key = "test-provider-key-0123456789"
	const cfgVal = "configured-mcp-value"
	r := NewRedactor([]string{"OPENAI_API_KEY=" + key, "GITHUB_TOKEN=gh-secret-abcdef", "HOME=/home/someone"}, cfgVal)
	path := filepath.Join(t.TempDir(), "log.db")
	s := openTest(t, Options{Path: path, Level: LevelFull, Redactor: r, Run: RunInfo{Task: "use " + key}})
	s.Emit("tool.call", map[string]any{"id": "1", "name": "bash", "arguments": map[string]any{"command": "curl -H 'Authorization: Bearer abc.def-ghi' " + cfgVal}})
	s.Emit("tool.result", map[string]any{"id": "1", "name": "bash", "output": "key=" + key + " tok=gh-secret-abcdef sk-ABCDEFGHIJKLMNOPQRSTUV", "is_error": false})
	s.Emit("warning", map[string]any{"message": "quoted \"" + key + "\""})
	s.Close()
	all := dump(t, rawDB(t, path))
	for _, secret := range []string{key, cfgVal, "gh-secret-abcdef", "abc.def-ghi", "sk-ABCDEFGHIJKLMNOPQRSTUV"} {
		if strings.Contains(all, secret) {
			t.Errorf("stored unredacted %q:\n%s", secret, all)
		}
	}
	if !strings.Contains(all, Redacted) {
		t.Errorf("redaction marker missing:\n%s", all)
	}
}

func TestRetentionMaxRuns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.db")
	base := time.Now()
	for i := range 4 {
		now := base.Add(time.Duration(i) * time.Second)
		s := openTest(t, Options{Path: path, MaxRuns: 2, Now: func() time.Time { return now }})
		emitRun(s)
		s.Close()
	}
	db := rawDB(t, path)
	if n := count(t, db, `SELECT COUNT(*) FROM runs`); n != 2 {
		t.Errorf("runs = %d, want 2", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM events WHERE run_id NOT IN (SELECT id FROM runs)`); n != 0 {
		t.Errorf("%d orphaned events", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM tool_calls WHERE run_id NOT IN (SELECT id FROM runs)`); n != 0 {
		t.Errorf("%d orphaned tool calls", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM runs WHERE started_at < ?`, base.Add(2*time.Second).UnixMilli()); n != 0 {
		t.Errorf("kept %d old runs", n)
	}
}

func TestRetentionMaxDays(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.db")
	old := time.Now().Add(-10 * 24 * time.Hour)
	s := openTest(t, Options{Path: path, Now: func() time.Time { return old }})
	emitRun(s)
	s.Close()
	s = openTest(t, Options{Path: path, MaxDays: 7})
	emitRun(s)
	s.Close()
	db := rawDB(t, path)
	if n := count(t, db, `SELECT COUNT(*) FROM runs`); n != 1 {
		t.Errorf("runs = %d, want 1", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM events`); n != 5 {
		t.Errorf("events = %d, want 5", n)
	}
}

func TestWriteFailureWarnsOnce(t *testing.T) {
	var warnings []string
	s := openTest(t, Options{Warn: func(m string) { warnings = append(warnings, m) }})
	s.db.Close() // the database becomes unusable mid-run
	for range 50 {
		emitRun(s)
	}
	s.Close()
	if len(warnings) != 1 {
		t.Fatalf("warnings = %q, want exactly one", warnings)
	}
}

func TestConcurrentRunsShareOneFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.db")
	const runs, events = 4, 200
	var wg sync.WaitGroup
	var mu sync.Mutex
	var warnings []string
	for range runs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s, err := Open(Options{Path: path, Warn: func(m string) { mu.Lock(); warnings = append(warnings, m); mu.Unlock() }})
			if err != nil {
				t.Error(err)
				return
			}
			for i := range events {
				s.Emit("warning", map[string]any{"message": fmt.Sprint(i)})
			}
			s.Emit("result", map[string]any{"status": "Submitted"})
			s.Close()
		}()
	}
	wg.Wait()
	if len(warnings) > 0 {
		t.Fatal(warnings)
	}
	db := rawDB(t, path)
	if n := count(t, db, `SELECT COUNT(*) FROM runs WHERE status = 'Submitted'`); n != runs {
		t.Errorf("runs = %d", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM events`); n != runs*(events+1) {
		t.Errorf("events = %d, want %d", n, runs*(events+1))
	}
}

func TestInjectionLikeInputIsStoredVerbatim(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.db")
	evil := `x'); DROP TABLE runs; -- "quoted" ''`
	s := openTest(t, Options{Path: path, Level: LevelFull, Run: RunInfo{Task: evil, Model: evil}})
	s.Emit(evil, map[string]any{"message": evil})
	s.Emit("tool.call", map[string]any{"id": evil, "name": evil, "arguments": evil})
	s.Emit("tool.result", map[string]any{"id": evil, "name": evil, "output": evil, "is_error": true})
	s.Close()
	db := rawDB(t, path)
	var task, model string
	if err := db.QueryRow(`SELECT task, model FROM runs`).Scan(&task, &model); err != nil {
		t.Fatal(err)
	}
	if task != evil || model != evil {
		t.Errorf("task = %q model = %q", task, model)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM events WHERE type = ?`, evil); n != 1 {
		t.Errorf("events of evil type = %d", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM tool_calls WHERE tool = ? AND call_id = ? AND is_error = 1`, evil, evil); n != 1 {
		t.Errorf("tool_calls = %d", n)
	}
}

func TestMaxPayloadBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "log.db")
	s := openTest(t, Options{Path: path, Level: LevelFull, MaxPayloadBytes: 4096})
	s.Emit("tool.result", map[string]any{"id": "1", "name": "read_file", "output": strings.Repeat("é", 100000), "is_error": false})
	s.Close()
	var payload string
	rawDB(t, path).QueryRow(`SELECT payload FROM events`).Scan(&payload)
	var m map[string]any
	if err := json.Unmarshal([]byte(payload), &m); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	if len(payload) > 4096 || m["_truncated"] != true || m["name"] != "read_file" {
		t.Errorf("payload (%d bytes) = %.200s", len(payload), payload)
	}
}

func TestOpenRejectsUnwritablePath(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if s, err := Open(Options{Path: file}); err != nil {
		t.Fatal(err)
	} else {
		s.Close()
	}
	if _, err := Open(Options{Path: filepath.Join(file, "x.db")}); err == nil {
		t.Error("opening under a regular file succeeded")
	}
}
