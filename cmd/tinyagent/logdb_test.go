package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vicgupta/tinyagent/internal/agent"
	"github.com/vicgupta/tinyagent/internal/eventlog"
	"github.com/vicgupta/tinyagent/internal/model"
)

// TestLogDBMirrorsJSONEvents runs one agent with both sinks: every JSONL
// event must have an events row of the same type and, at level full with
// nothing to redact, the same payload.
func TestLogDBMirrorsJSONEvents(t *testing.T) {
	m := &replies{
		{Role: "assistant", Content: "looking", ToolCalls: []model.ToolCall{{ID: "1", Type: "function", Function: model.FunctionCall{Name: "list_files", Arguments: `{}`}}}},
		{Role: "assistant", Content: "all good\n" + agent.SubmitMarker},
	}
	a := newTestAgent(t, m)
	a.Config.CostLimit = 1
	a.ModelName = "openai/unpriced-model" // adds the unpriced warning event
	path := filepath.Join(t.TempDir(), "log.db")
	db, err := eventlog.Open(eventlog.Options{Path: path, Level: eventlog.LevelFull, Step: a.Step, Redactor: eventlog.NewRedactor(nil)})
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	events := startEvents(&out, true, a, false, func() { t.Error("stop called") }, db, nil)
	if err := events.finish(a.Run(context.Background(), "check"), time.Second); err != nil {
		t.Fatal(err)
	}
	var jsonl []map[string]any
	for _, line := range strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n") {
		var e map[string]any
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatal(err)
		}
		jsonl = append(jsonl, e)
	}
	sdb, err := sql.Open("sqlite", eventlog.DSN(path))
	if err != nil {
		t.Fatal(err)
	}
	defer sdb.Close()
	rows, err := sdb.Query(`SELECT type, payload, step FROM events ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var stored []map[string]any
	var lastStep int
	for rows.Next() {
		var kind, payload string
		var step int
		if err := rows.Scan(&kind, &payload, &step); err != nil {
			t.Fatal(err)
		}
		var e map[string]any
		if err := json.Unmarshal([]byte(payload), &e); err != nil {
			t.Fatal(err)
		}
		e["type"] = kind
		stored = append(stored, e)
		lastStep = step
	}
	if len(stored) != len(jsonl) {
		t.Fatalf("db has %d events, JSONL %d:\n%v\n%v", len(stored), len(jsonl), stored, jsonl)
	}
	for i := range jsonl {
		a, _ := json.Marshal(jsonl[i])
		b, _ := json.Marshal(stored[i])
		if string(a) != string(b) {
			t.Errorf("event %d differs:\njsonl %s\ndb    %s", i, a, b)
		}
	}
	if lastStep != 2 {
		t.Errorf("last event step = %d, want 2", lastStep)
	}
}

func TestLogDBDisabledByDefault(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "none.yaml")
	os.WriteFile(cfg, nil, 0o644)
	var requests atomic.Int32
	srv := fakeOpenAI(t, &requests)
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	t.Setenv("OPENAI_API_KEY", "fake")
	for _, args := range [][]string{{}, {"--json"}, {"--log-db=false"}} {
		stderr := captureStderr(t, func() {
			if code, _, _ := runWith(t, append([]string{"-t", "x", "--no-stream", "--cwd", dir, "-c", cfg}, args...)...); code != 0 {
				t.Errorf("%v: exit %d", args, code)
			}
		})
		if strings.Contains(stderr, "log db") {
			t.Errorf("%v: stderr mentions the log db: %s", args, stderr)
		}
		if _, err := os.Stat(filepath.Join(dir, ".tinyagent")); !os.IsNotExist(err) {
			t.Fatalf("%v: .tinyagent created (err %v)", args, err)
		}
	}
}

func TestLogDBFlagCreatesDefaultDatabase(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "none.yaml")
	os.WriteFile(cfg, nil, 0o644)
	var requests atomic.Int32
	srv := fakeOpenAI(t, &requests)
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	t.Setenv("OPENAI_API_KEY", "fake")
	stderr := captureStderr(t, func() {
		if code, _, _ := runWith(t, "-t", "x", "--no-stream", "--cwd", dir, "-c", cfg, "--log-db"); code != 0 {
			t.Errorf("exit %d", code)
		}
	})
	if strings.Contains(stderr, "warning") {
		t.Errorf("stderr: %s", stderr)
	}
	path := filepath.Join(dir, ".tinyagent", "tinyagent.db")
	db, err := sql.Open("sqlite", eventlog.DSN(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var status, provider string
	var task sql.NullString
	if err := db.QueryRow(`SELECT status, provider, task FROM runs`).Scan(&status, &provider, &task); err != nil {
		t.Fatal(err)
	}
	if status != agent.StatusSubmitted || provider != "openai" || task.Valid {
		t.Errorf("run = %s %s %v", status, provider, task)
	}
}

func TestLogDBConfigPathIsRelativeToCwd(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "c.yaml")
	os.WriteFile(cfg, []byte("log_db:\n  enabled: true\n  path: logs/run.db\n  level: full\n"), 0o644)
	var requests atomic.Int32
	srv := fakeOpenAI(t, &requests)
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	t.Setenv("OPENAI_API_KEY", "fake")
	captureStderr(t, func() {
		if code, _, _ := runWith(t, "-t", "x", "--no-stream", "--cwd", dir, "-c", cfg); code != 0 {
			t.Errorf("exit %d", code)
		}
	})
	if _, err := os.Stat(filepath.Join(dir, "logs", "run.db")); err != nil {
		t.Fatal(err)
	}
}

func TestUnwritableLogDBWarnsOnceAndRunSucceeds(t *testing.T) {
	dir := t.TempDir()
	cfg := filepath.Join(dir, "none.yaml")
	os.WriteFile(cfg, nil, 0o644)
	var requests atomic.Int32
	srv := fakeOpenAI(t, &requests)
	t.Setenv("OPENAI_BASE_URL", srv.URL)
	t.Setenv("OPENAI_API_KEY", "fake")
	var code int
	stderr := captureStderr(t, func() {
		code, _, _ = runWith(t, "-t", "x", "--no-stream", "--cwd", dir, "-c", cfg, "--log-db="+filepath.Join(cfg, "x.db"))
	})
	if code != 0 || requests.Load() != 1 {
		t.Fatalf("exit %d, requests %d; stderr %s", code, requests.Load(), stderr)
	}
	if n := strings.Count(stderr, "warning:"); n != 1 {
		t.Errorf("got %d warnings:\n%s", n, stderr)
	}
}
