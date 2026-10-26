package eventlog

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	_ "modernc.org/sqlite" // pure-Go driver, no cgo
)

// Levels control how much of each event is stored.
const (
	// LevelMetadata stores no prompt, assistant, tool-argument, tool-output
	// or submission bodies: each is replaced by its length.
	LevelMetadata = "metadata"
	// LevelFull stores bodies, redacted and size-capped.
	LevelFull = "full"
)

// DefaultMaxPayloadBytes caps one stored event payload.
const DefaultMaxPayloadBytes = 64 << 10

// resultSummaryBytes caps tool_calls.result_summary at level full.
const resultSummaryBytes = 1024

// bodyKeys are the payload fields that carry bodies; at LevelMetadata they
// are replaced by <key>_len.
var bodyKeys = []string{"text", "arguments", "output", "submission", "task", "content", "prompt", "question"}

//go:embed migrations/*.sql
var migrationFS embed.FS

type migration struct {
	version int
	sql     string
}

func embeddedMigrations() []migration {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		panic(err)
	}
	var out []migration
	for _, e := range entries {
		n, err := strconv.Atoi(strings.SplitN(e.Name(), "_", 2)[0])
		if err != nil {
			panic("bad migration name " + e.Name())
		}
		b, err := migrationFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			panic(err)
		}
		out = append(out, migration{n, string(b)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out
}

// SchemaVersion is the newest schema this binary writes.
var SchemaVersion = func() int { m := embeddedMigrations(); return m[len(m)-1].version }()

// RunInfo describes the run being logged.
type RunInfo struct {
	Cwd, Model, Provider, Task, Version string
	Resumed                             bool
}

// Options configures an SQLite sink.
type Options struct {
	Path            string
	Level           string // LevelMetadata (default) or LevelFull
	MaxDays         int    // prune runs older than this at open; 0 keeps all
	MaxRuns         int    // keep at most this many runs, including the new one; 0 keeps all
	MaxPayloadBytes int    // 0 means DefaultMaxPayloadBytes
	Redactor        *Redactor
	Run             RunInfo
	// Step returns the current step for each event; nil records 0.
	Step func() int
	// Warn is called at most once, on the caller's goroutine, after the
	// first failed write. Later writes are dropped.
	Warn func(msg string)
	// Now overrides the clock in tests.
	Now func() time.Time
}

type op struct {
	query string
	args  []any
}

type pendingCall struct {
	start time.Time
	step  int
}

// SQLite is a Sink that writes events to an SQLite database from a
// background goroutine, batching inserts in transactions. Write failures
// never surface as errors: Warn is called once and later events are dropped.
type SQLite struct {
	opts  Options
	db    *sql.DB
	runID string
	seq   int
	calls map[string]pendingCall

	ch   chan []op
	done chan struct{}

	mu      sync.Mutex
	failErr error
	closed  bool
	warned  sync.Once
}

// DSN returns the driver connection string for path: WAL journaling, a busy
// timeout so concurrent runs wait instead of failing, and immediate write
// transactions so they take the write lock up front.
func DSN(path string) string {
	return path + "?_pragma=busy_timeout(10000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_txlock=immediate"
}

// Open creates or migrates the database at opts.Path, prunes old runs, and
// records a new run. The caller decides what an error means; tinyagent
// warns and runs without the log.
func Open(opts Options) (*SQLite, error) {
	if opts.Level == "" {
		opts.Level = LevelMetadata
	}
	if opts.Level != LevelMetadata && opts.Level != LevelFull {
		return nil, fmt.Errorf("log_db level %q: want %s or %s", opts.Level, LevelMetadata, LevelFull)
	}
	if opts.MaxPayloadBytes <= 0 {
		opts.MaxPayloadBytes = DefaultMaxPayloadBytes
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if dir := filepath.Dir(opts.Path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	// Create the file owner-only before SQLite opens it; the -wal and -shm
	// files inherit its permissions.
	f, err := os.OpenFile(opts.Path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	f.Close()
	db, err := sql.Open("sqlite", DSN(opts.Path))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	s := &SQLite{opts: opts, db: db, calls: map[string]pendingCall{}, ch: make(chan []op, 256), done: make(chan struct{})}
	// Switching a new file to WAL needs an exclusive lock and can report
	// SQLITE_BUSY without waiting on the busy timeout, so retry briefly.
	err = s.init()
	for deadline := time.Now().Add(10 * time.Second); err != nil && strings.Contains(err.Error(), "SQLITE_BUSY") && time.Now().Before(deadline); {
		time.Sleep(20 * time.Millisecond)
		err = s.init()
	}
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("log db %s: %w", opts.Path, err)
	}
	go s.writer()
	return s, nil
}

func (s *SQLite) init() error {
	ctx := context.Background()
	if err := migrate(ctx, s.db, embeddedMigrations()); err != nil {
		return err
	}
	if err := s.prune(ctx); err != nil {
		return err
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return err
	}
	s.runID = hex.EncodeToString(id[:])
	r := s.opts.Run
	sum := sha256.Sum256([]byte(r.Task))
	var task any
	if s.opts.Level == LevelFull {
		task = truncate(s.opts.Redactor.Redact(r.Task), s.opts.MaxPayloadBytes)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO runs (id, started_at, cwd, model, provider, task, task_sha256, task_len, level, resumed, tinyagent_version, schema_version)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.runID, s.opts.Now().UnixMilli(), r.Cwd, r.Model, r.Provider, task, hex.EncodeToString(sum[:]), len(r.Task), s.opts.Level, r.Resumed, r.Version, SchemaVersion)
	return err
}

// RunID identifies this run's row in runs.
func (s *SQLite) RunID() string { return s.runID }

// migrate applies the migrations newer than the database's recorded version,
// each in its own immediate transaction so concurrent openers serialize.
func migrate(ctx context.Context, db *sql.DB, migs []migration) error {
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
		return err
	}
	for _, m := range migs {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		var applied int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version = ?`, m.version).Scan(&applied); err != nil {
			tx.Rollback()
			return err
		}
		if applied == 0 {
			if _, err := tx.ExecContext(ctx, m.sql); err != nil {
				tx.Rollback()
				return fmt.Errorf("migration %d: %w", m.version, err)
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`, m.version, time.Now().UnixMilli()); err != nil {
				tx.Rollback()
				return err
			}
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// prune deletes runs (and their events and tool calls) older than MaxDays,
// and all but the newest MaxRuns-1, leaving room for the new run.
func (s *SQLite) prune(ctx context.Context) error {
	var conds []string
	var args []any
	if s.opts.MaxDays > 0 {
		conds = append(conds, `started_at < ?`)
		args = append(args, s.opts.Now().Add(-time.Duration(s.opts.MaxDays)*24*time.Hour).UnixMilli())
	}
	if s.opts.MaxRuns > 0 {
		conds = append(conds, `id IN (SELECT id FROM runs ORDER BY started_at DESC, rowid DESC LIMIT -1 OFFSET ?)`)
		args = append(args, s.opts.MaxRuns-1)
	}
	if len(conds) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `CREATE TEMP TABLE IF NOT EXISTS prune_ids (id TEXT PRIMARY KEY)`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM prune_ids`); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO prune_ids SELECT id FROM runs WHERE `+strings.Join(conds, " OR "), args...); err != nil {
		return err
	}
	for _, q := range []string{
		`DELETE FROM events WHERE run_id IN (SELECT id FROM prune_ids)`,
		`DELETE FROM tool_calls WHERE run_id IN (SELECT id FROM prune_ids)`,
		`DELETE FROM runs WHERE id IN (SELECT id FROM prune_ids)`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Emit records one event. It never blocks on a failed database.
func (s *SQLite) Emit(kind string, fields map[string]any) {
	s.mu.Lock()
	failed, closed := s.failErr != nil, s.closed
	s.mu.Unlock()
	if failed {
		s.warnOnce()
	}
	if failed || closed {
		return
	}
	now := s.opts.Now()
	step := 0
	if s.opts.Step != nil {
		step = s.opts.Step()
	}
	s.seq++
	ops := []op{{`INSERT INTO events (run_id, seq, step, ts, type, payload) VALUES (?, ?, ?, ?, ?, ?)`,
		[]any{s.runID, s.seq, step, now.UnixMilli(), kind, s.payload(fields)}}}
	switch kind {
	case "tool.call":
		id := str(fields["id"])
		s.calls[id] = pendingCall{now, step}
		var args any
		if s.opts.Level == LevelFull {
			b, _ := json.Marshal(fields["arguments"])
			args = truncate(s.opts.Redactor.Redact(string(b)), s.opts.MaxPayloadBytes)
		}
		ops = append(ops, op{`INSERT INTO tool_calls (run_id, step, call_id, tool, args, started_at) VALUES (?, ?, ?, ?, ?, ?)`,
			[]any{s.runID, step, id, str(fields["name"]), args, now.UnixMilli()}})
	case "tool.result":
		id := str(fields["id"])
		out := str(fields["output"])
		summary := fmt.Sprintf(`{"output_len":%d}`, len(out))
		if s.opts.Level == LevelFull {
			summary = truncate(s.opts.Redactor.Redact(out), resultSummaryBytes)
		}
		var dur any
		if c, ok := s.calls[id]; ok {
			dur = now.Sub(c.start).Milliseconds()
			delete(s.calls, id)
		}
		isErr, _ := fields["is_error"].(bool)
		ops = append(ops, op{`UPDATE tool_calls SET result_summary = ?, duration_ms = ?, is_error = ? WHERE id = (SELECT id FROM tool_calls WHERE run_id = ? AND call_id = ? AND is_error IS NULL ORDER BY id DESC LIMIT 1)`,
			[]any{summary, dur, isErr, s.runID, id}})
	case "result":
		ops = append(ops, op{`UPDATE runs SET ended_at = ?, status = ?, steps = ?, input_tokens = ?, output_tokens = ?, cache_read_tokens = ?, cache_write_tokens = ?, cost_usd = ?, duration_ms = ? WHERE id = ?`,
			[]any{now.UnixMilli(), str(fields["status"]), num(fields["steps"]), num(fields["input_tokens"]), num(fields["output_tokens"]),
				num(fields["cache_read_tokens"]), num(fields["cache_write_tokens"]), fields["cost_usd"], num(fields["duration_ms"]), s.runID}})
	}
	s.ch <- ops
}

// payload returns fields as stored: bodies replaced by lengths at
// LevelMetadata, secrets redacted, and capped at MaxPayloadBytes.
func (s *SQLite) payload(fields map[string]any) string {
	m := make(map[string]any, len(fields))
	for k, v := range fields {
		m[k] = v
	}
	if s.opts.Level != LevelFull {
		for _, k := range bodyKeys {
			v, ok := m[k]
			if !ok {
				continue
			}
			delete(m, k)
			if sv, ok := v.(string); ok {
				m[k+"_len"] = len(sv)
			} else {
				b, _ := json.Marshal(v)
				m[k+"_len"] = len(b)
			}
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		b, _ = json.Marshal(map[string]any{"_error": "unencodable payload"})
	}
	out := s.opts.Redactor.Redact(string(b))
	if len(out) <= s.opts.MaxPayloadBytes {
		return out
	}
	// Too big: keep the redacted fields, each string cut short.
	var red map[string]any
	if json.Unmarshal([]byte(out), &red) == nil {
		short := map[string]any{"_truncated": true, "_bytes": len(out)}
		per := max(64, s.opts.MaxPayloadBytes/(2*max(1, len(red))))
		for k, v := range red {
			switch v := v.(type) {
			case string:
				short[k] = truncate(v, per)
			case map[string]any, []any:
				b, _ := json.Marshal(v)
				short[k] = truncate(string(b), per)
			default:
				short[k] = v
			}
		}
		if b, err := json.Marshal(short); err == nil && len(b) <= s.opts.MaxPayloadBytes {
			return string(b)
		}
	}
	return fmt.Sprintf(`{"_truncated":true,"_bytes":%d}`, len(out))
}

func (s *SQLite) writer() {
	defer close(s.done)
	for ops := range s.ch {
		batch := append([]op(nil), ops...)
	drain:
		for len(batch) < 512 {
			select {
			case more, ok := <-s.ch:
				if !ok {
					break drain
				}
				batch = append(batch, more...)
			default:
				break drain
			}
		}
		if s.failed() {
			continue
		}
		if err := s.exec(batch); err != nil {
			s.mu.Lock()
			s.failErr = err
			s.mu.Unlock()
		}
	}
}

func (s *SQLite) exec(batch []op) error {
	ctx := context.Background()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	for _, o := range batch {
		if _, err := tx.ExecContext(ctx, o.query, o.args...); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (s *SQLite) failed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.failErr != nil
}

func (s *SQLite) warnOnce() {
	s.warned.Do(func() {
		s.mu.Lock()
		err := s.failErr
		s.mu.Unlock()
		if s.opts.Warn != nil {
			s.opts.Warn(fmt.Sprintf("log db %s: %v; further database writes are disabled", s.opts.Path, err))
		}
	})
}

// Close flushes buffered events, marks the run ended if no result was
// recorded, and closes the database. A write failure is reported through
// Warn (once), never returned.
func (s *SQLite) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	failed := s.failErr != nil
	s.mu.Unlock()
	if !failed {
		s.ch <- []op{{`UPDATE runs SET ended_at = ? WHERE id = ? AND ended_at IS NULL`, []any{s.opts.Now().UnixMilli(), s.runID}}}
	}
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	close(s.ch)
	<-s.done
	if s.failed() {
		s.warnOnce()
	}
	if err := s.db.Close(); err != nil && !s.failed() {
		s.mu.Lock()
		s.failErr = err
		s.mu.Unlock()
		s.warnOnce()
	}
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func num(v any) any {
	switch v := v.(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	}
	return nil
}

// truncate cuts s to at most n bytes on a UTF-8 boundary.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
