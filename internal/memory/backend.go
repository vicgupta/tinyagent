package memory

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Store backends. A store is named by one path, and its kind follows from
// that path: a .sqlite, .sqlite3 or .db file is a SQLite database, a .json
// file is a legacy JSON store (read for import, still writable), and
// anything else is a markdown folder holding one <id>.md file per entry.
const (
	BackendMarkdown = "markdown"
	BackendSQLite   = "sqlite"
	backendJSON     = "json"
)

// Default store locations, relative to the working directory.
const (
	DefaultMarkdownPath = "memory"
	DefaultSQLitePath   = "memory/memory.sqlite"
)

// ValidBackend reports whether s is a selectable backend.
func ValidBackend(s string) bool { return s == BackendMarkdown || s == BackendSQLite }

// isSQLitePath reports whether path names a SQLite store.
func isSQLitePath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".sqlite", ".sqlite3", ".db":
		return true
	}
	return false
}

// BackendOf returns the backend a store path selects.
func BackendOf(path string) string {
	switch {
	case isSQLitePath(path):
		return BackendSQLite
	case strings.EqualFold(filepath.Ext(path), ".json"):
		return backendJSON
	}
	return BackendMarkdown
}

// ResolveStore returns the absolute store path for backend: dir (markdown)
// or sqlitePath (sqlite), each defaulting to its Default*Path, with
// relative paths resolved against cwd.
func ResolveStore(backend, dir, sqlitePath, cwd string) (string, error) {
	var p string
	switch backend {
	case "", BackendMarkdown:
		p = dir
		if p == "" {
			p = DefaultMarkdownPath
		}
		if BackendOf(p) != BackendMarkdown {
			return "", fmt.Errorf("memory path %q looks like a file; the markdown backend needs a folder (use sqlite_path for a database)", p)
		}
	case BackendSQLite:
		p = sqlitePath
		if p == "" {
			p = DefaultSQLitePath
		}
		if !isSQLitePath(p) {
			return "", fmt.Errorf("memory sqlite_path %q must end in .sqlite, .sqlite3 or .db", p)
		}
	default:
		return "", fmt.Errorf("memory backend %q must be markdown or sqlite", backend)
	}
	if !filepath.IsAbs(p) {
		abs, err := filepath.Abs(cwd)
		if err != nil {
			return "", err
		}
		p = filepath.Join(abs, p)
	}
	return filepath.Clean(p), nil
}

// Exists reports whether the store at path exists (a folder for markdown,
// a file otherwise).
func Exists(path string) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return false
	}
	if BackendOf(path) == BackendMarkdown {
		return fi.IsDir()
	}
	return !fi.IsDir()
}

// Load reads the store at path, oldest first. A missing store is empty and
// is never created. A store that can't be parsed returns a *CorruptError.
func Load(path string) ([]Entry, error) {
	if path == "" {
		return nil, errors.New("no memory store path")
	}
	switch BackendOf(path) {
	case BackendSQLite:
		return loadSQLite(path)
	case backendJSON:
		return loadJSON(path)
	}
	return loadDir(path)
}

// Update re-reads the store at path under an exclusive lock, applies fn and
// writes the result atomically, so concurrent writers never lose each
// other's changes. If fn returns an error nothing is written. A corrupt
// store is left unchanged and its *CorruptError is returned.
func Update(path string, fn func([]Entry) ([]Entry, error)) error {
	if path == "" {
		return errors.New("no memory store path")
	}
	switch BackendOf(path) {
	case BackendSQLite:
		return updateSQLite(path, fn)
	case backendJSON:
		return updateJSON(path, fn)
	}
	return updateDir(path, fn)
}

func validEntry(e Entry) error {
	if e.ID == "" || !validID(e.ID) {
		return fmt.Errorf("invalid entry id %q", e.ID)
	}
	if e.Status != StatusActive && e.Status != StatusPending {
		return fmt.Errorf("entry %s has invalid status %q", e.ID, e.Status)
	}
	return nil
}

// validID reports whether id is safe as a file name.
func validID(id string) bool {
	if id == "" || len(id) > 64 || id[0] == '.' {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}
