package eventlog

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// AttemptGroup is one --attempts invocation in the log DB. Its attempts are
// the runs rows whose attempts_id is ID.
type AttemptGroup struct {
	ID        string
	Cwd       string
	Requested int
	TraceID   string
	Version   string
	Started   time.Time
}

// AttemptGroupResult is how a group ended.
type AttemptGroupResult struct {
	Run          int
	Winner       *int
	WinnerBranch string
	StopReason   string
	CostUSD      float64
	WallMS       int64
	Ended        time.Time
}

// StartAttemptGroup records g in the log DB at path, creating or migrating
// the database like Open. It does not add a runs row: each attempt's child
// run adds its own.
func StartAttemptGroup(path string, g AttemptGroup) error {
	return withDB(path, func(ctx context.Context, db *sql.DB) error {
		_, err := db.ExecContext(ctx, `INSERT OR REPLACE INTO attempt_groups (id, started_at, cwd, attempts_requested, trace_id, tinyagent_version)
			VALUES (?, ?, ?, ?, ?, ?)`, g.ID, g.Started.UnixMilli(), g.Cwd, g.Requested, nullable(g.TraceID), nullable(g.Version))
		return err
	})
}

// FinishAttemptGroup records how group id ended.
func FinishAttemptGroup(path, id string, r AttemptGroupResult) error {
	return withDB(path, func(ctx context.Context, db *sql.DB) error {
		var winner any
		if r.Winner != nil {
			winner = *r.Winner
		}
		_, err := db.ExecContext(ctx, `UPDATE attempt_groups SET ended_at = ?, attempts_run = ?, winner = ?, winner_branch = ?,
			stop_reason = ?, total_cost_usd = ?, total_wall_time_ms = ? WHERE id = ?`,
			r.Ended.UnixMilli(), r.Run, winner, nullable(r.WinnerBranch), r.StopReason, r.CostUSD, r.WallMS, id)
		return err
	})
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func withDB(path string, fn func(context.Context, *sql.DB) error) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	f.Close()
	db, err := sql.Open("sqlite", DSN(path))
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	run := func() error {
		if err := migrate(ctx, db, embeddedMigrations()); err != nil {
			return err
		}
		return fn(ctx, db)
	}
	err = run()
	for deadline := time.Now().Add(10 * time.Second); err != nil && strings.Contains(err.Error(), "SQLITE_BUSY") && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
		err = run()
	}
	if err != nil {
		return fmt.Errorf("log db %s: %w", path, err)
	}
	return nil
}

// attemptOps links this run's row to its attempts group from the
// run.started "attempt" field a child of --attempts carries.
func (s *SQLite) attemptOps(fields map[string]any) []op {
	m, ok := fields["attempt"].(map[string]any)
	if !ok {
		return nil
	}
	id := str(m["attempts_id"])
	if id == "" {
		return nil
	}
	return []op{{`UPDATE runs SET attempts_id = ?, attempt = ? WHERE id = ?`, []any{id, num(m["index"]), s.runID}}}
}
