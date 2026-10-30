package memory

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver, no cgo
)

// schemaVersion is the SQLite store schema, kept in PRAGMA user_version.
const schemaVersion = 1

// migrations[i] moves a database from user_version i to i+1.
var migrations = []string{
	`CREATE TABLE memories (
		id                TEXT PRIMARY KEY,
		content           TEXT NOT NULL,
		created_at        TEXT NOT NULL,
		updated_at        TEXT NOT NULL,
		source_session    TEXT NOT NULL DEFAULT '',
		model             TEXT NOT NULL DEFAULT '',
		version           TEXT NOT NULL DEFAULT '',
		untrusted_context INTEGER NOT NULL DEFAULT 0,
		status            TEXT NOT NULL CHECK (status IN ('active', 'pending')),
		replaces          TEXT NOT NULL DEFAULT '',
		tags              TEXT NOT NULL DEFAULT '',
		seq               INTEGER NOT NULL DEFAULT 0
	);
	CREATE INDEX memories_created_at ON memories (created_at);
	CREATE INDEX memories_status ON memories (status);`,
}

func sqliteDSN(path string) string {
	return path + "?_pragma=busy_timeout(10000)&_txlock=immediate"
}

func openSQLite(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

func userVersion(q interface {
	QueryRow(string, ...any) *sql.Row
}) (int, error) {
	var v int
	err := q.QueryRow("PRAGMA user_version").Scan(&v)
	return v, err
}

const selectAll = `SELECT id, content, created_at, updated_at, source_session, model, version, untrusted_context, status, replaces, tags FROM memories ORDER BY seq, created_at, id`

func scanEntries(rows *sql.Rows, path string) ([]Entry, error) {
	defer rows.Close()
	var out []Entry
	for rows.Next() {
		var e Entry
		var created, updated, tags string
		var untrusted int
		if err := rows.Scan(&e.ID, &e.Text, &created, &updated, &e.Source.RunID, &e.Source.Model, &e.Source.Version, &untrusted, &e.Status, &e.Replaces, &tags); err != nil {
			return nil, &CorruptError{Path: path, Err: err}
		}
		e.Source.UntrustedContext = untrusted != 0
		var err error
		if e.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
			return nil, &CorruptError{Path: path, Err: fmt.Errorf("entry %s: bad created_at", e.ID)}
		}
		if e.UpdatedAt, err = time.Parse(time.RFC3339Nano, updated); err != nil {
			return nil, &CorruptError{Path: path, Err: fmt.Errorf("entry %s: bad updated_at", e.ID)}
		}
		if tags != "" {
			e.Tags = strings.Split(tags, ",")
		}
		if err := validEntry(e); err != nil {
			return nil, &CorruptError{Path: path, Err: err}
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, &CorruptError{Path: path, Err: err}
	}
	return out, nil
}

// loadSQLite reads the SQLite store at path without creating or migrating
// it. A missing file is an empty store.
func loadSQLite(path string) ([]Entry, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	db, err := openSQLite(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	v, err := userVersion(db)
	if err != nil {
		return nil, &CorruptError{Path: path, Err: err}
	}
	if v == 0 {
		return nil, nil // created but never migrated: empty
	}
	if v > schemaVersion {
		return nil, &CorruptError{Path: path, Err: fmt.Errorf("unsupported schema version %d (this tinyagent knows %d)", v, schemaVersion)}
	}
	rows, err := db.Query(selectAll)
	if err != nil {
		return nil, &CorruptError{Path: path, Err: err}
	}
	return scanEntries(rows, path)
}

// updateSQLite is Update for a SQLite store, in one IMMEDIATE transaction
// (which also serializes concurrent writers). It creates and migrates the
// database as needed.
func updateSQLite(path string, fn func([]Entry) ([]Entry, error)) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600); err != nil {
		return err
	} else {
		f.Close()
	}
	db, err := openSQLite(path)
	if err != nil {
		return err
	}
	defer db.Close()
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	v, err := userVersion(tx)
	if err != nil {
		return &CorruptError{Path: path, Err: err}
	}
	if v > schemaVersion {
		return &CorruptError{Path: path, Err: fmt.Errorf("unsupported schema version %d (this tinyagent knows %d)", v, schemaVersion)}
	}
	for ; v < schemaVersion; v++ {
		if _, err := tx.Exec(migrations[v]); err != nil {
			return fmt.Errorf("memory store %s: migrating to schema %d: %w", path, v+1, err)
		}
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", v+1)); err != nil {
			return err
		}
	}
	rows, err := tx.Query(selectAll)
	if err != nil {
		return &CorruptError{Path: path, Err: err}
	}
	entries, err := scanEntries(rows, path)
	if err != nil {
		return err
	}
	out, err := fn(entries)
	if err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM memories"); err != nil {
		return err
	}
	ins, err := tx.Prepare(`INSERT INTO memories (id, content, created_at, updated_at, source_session, model, version, untrusted_context, status, replaces, tags, seq) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer ins.Close()
	for i, e := range out {
		if err := validEntry(e); err != nil {
			return err
		}
		untrusted := 0
		if e.Source.UntrustedContext {
			untrusted = 1
		}
		if _, err := ins.Exec(e.ID, e.Text, e.CreatedAt.UTC().Format(time.RFC3339Nano), e.UpdatedAt.UTC().Format(time.RFC3339Nano),
			e.Source.RunID, e.Source.Model, e.Source.Version, untrusted, e.Status, e.Replaces, strings.Join(NormalizeTags(e.Tags), ","), i); err != nil {
			return err
		}
	}
	return tx.Commit()
}
