package tools

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// UndoToolName is the built-in tool that restores a file's session snapshot.
const UndoToolName = "undo_file"

// UndoDir is where per-session file snapshots live, relative to the working
// directory. Its contents are local state and must never be committed.
const UndoDir = ".tinyagent/undo"

// undoState records, per session, the contents each file had before the
// built-in file tools first changed it. Only that first snapshot is kept.
type undoState struct {
	mu      sync.Mutex
	session string
}

// undoSessionID returns e's undo session id, creating one on first use.
func (e *Executor) undoSessionID() string {
	e.undo.mu.Lock()
	defer e.undo.mu.Unlock()
	if e.undo.session == "" {
		e.undo.session = e.UndoSession
	}
	if e.undo.session == "" {
		var b [4]byte
		_, _ = rand.Read(b[:])
		e.undo.session = time.Now().UTC().Format("20060102T150405Z") + "-" + hex.EncodeToString(b[:])
	}
	return e.undo.session
}

// undoSnapDir is the snapshot directory for e's session.
func (e *Executor) undoSnapDir() string {
	return filepath.Join(e.Root, filepath.FromSlash(UndoDir), e.undoSessionID())
}

// undoKey names a file's snapshot by a hash of its absolute path.
func undoKey(full string) string {
	h := sha256.Sum256([]byte(filepath.Clean(full)))
	return hex.EncodeToString(h[:16])
}

// undoSnapshot saves full's current contents (or that it did not exist) into
// dir, unless the session already has a snapshot for it.
func undoSnapshot(dir, full string) error {
	if dir == "" {
		return nil
	}
	if within(filepath.Dir(dir), full) {
		return nil // never snapshot the snapshots
	}
	base := filepath.Join(dir, undoKey(full))
	if _, err := os.Lstat(base); err == nil {
		return nil
	}
	if _, err := os.Lstat(base + ".absent"); err == nil {
		return nil
	}
	fi, err := os.Stat(full)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := mkUndoDir(dir); err != nil {
			return err
		}
		return os.WriteFile(base+".absent", []byte(full), 0o600)
	case err != nil:
		return fmt.Errorf("undo snapshot: %w", err)
	case !fi.Mode().IsRegular():
		return nil
	}
	b, err := os.ReadFile(full)
	if err != nil {
		return fmt.Errorf("undo snapshot: %w", err)
	}
	if err := mkUndoDir(dir); err != nil {
		return err
	}
	tmp := base + ".tmp"
	if err := os.WriteFile(tmp, b, fi.Mode().Perm()); err != nil {
		return fmt.Errorf("undo snapshot: %w", err)
	}
	if err := os.Chmod(tmp, fi.Mode().Perm()); err != nil {
		return fmt.Errorf("undo snapshot: %w", err)
	}
	return os.Rename(tmp, base)
}

// undoFile restores path to its session snapshot: the original contents, or
// removal when the file did not exist before the session changed it. It goes
// through the same path and write checks as the other file tools.
func (e *Executor) undoFile(path string) (string, error) {
	if path == "" {
		return "", errors.New("path is required")
	}
	sc := e.scope()
	sc.snapDir = "" // restoring must not take a new snapshot
	full, err := sc.resolve(e.Root, path)
	if err != nil {
		return "", err
	}
	if err := sc.checkWrite(full, path); err != nil {
		return "", err
	}
	base := filepath.Join(e.undoSnapDir(), undoKey(full))
	if _, err := os.Lstat(base + ".absent"); err == nil {
		if err := os.Remove(full); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		return fmt.Sprintf("ok: removed %s (it did not exist before this session)", path), nil
	}
	fi, err := os.Lstat(base)
	if err != nil {
		return "", fmt.Errorf("no undo snapshot for %s in this session (only write_file, edit_file and apply_patch changes are recorded)", path)
	}
	b, err := os.ReadFile(base)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(full, b, fi.Mode().Perm()); err != nil {
		return "", err
	}
	if err := os.Chmod(full, fi.Mode().Perm()); err != nil {
		return "", err
	}
	return fmt.Sprintf("ok: restored %s to its contents before this session's first change", path), nil
}

// mkUndoDir creates the session snapshot directory and a .gitignore in the
// undo directory so snapshots are never committed (git add -A included).
func mkUndoDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("undo snapshot: %w", err)
	}
	ign := filepath.Join(filepath.Dir(dir), ".gitignore")
	if _, err := os.Lstat(ign); errors.Is(err, fs.ErrNotExist) {
		if err := os.WriteFile(ign, []byte("*\n"), 0o644); err != nil {
			return fmt.Errorf("undo snapshot: %w", err)
		}
	}
	return nil
}
