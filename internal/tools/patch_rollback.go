package tools

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// patchFS is the file system apply_patch changes files through. Tests
// replace it with a fake that fails on a chosen call; production uses
// osPatchFS. Reads always go to the os package directly.
type patchFS interface {
	WriteFile(name string, data []byte, perm os.FileMode) error
	// CreateFile creates name, failing if anything already exists there.
	CreateFile(name string, data []byte, perm os.FileMode) error
	Remove(name string) error
	MkdirAll(name string, perm os.FileMode) error
	Chmod(name string, mode os.FileMode) error
	Symlink(target, name string) error
}

type osPatchFS struct{}

func (osPatchFS) WriteFile(name string, data []byte, perm os.FileMode) error {
	return os.WriteFile(name, data, perm)
}

func (osPatchFS) CreateFile(name string, data []byte, perm os.FileMode) error {
	f, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func (osPatchFS) Remove(name string) error                     { return os.Remove(name) }
func (osPatchFS) MkdirAll(name string, perm os.FileMode) error { return os.MkdirAll(name, perm) }
func (osPatchFS) Chmod(name string, mode os.FileMode) error    { return os.Chmod(name, mode) }
func (osPatchFS) Symlink(target, name string) error            { return os.Symlink(target, name) }

// pathBackup is one path's state just before an apply_patch call changes
// it, kept in memory. The session's undo snapshots are not used for
// rollback: they hold a file's state before the session's first change,
// which may be older than this call.
type pathBackup struct {
	path   string
	exists bool
	link   string // the symlink target, when path was a symlink
	data   []byte // contents of the file (through the link, for a symlink)
	mode   os.FileMode
	dirs   []string // missing parent directories, deepest first (absent paths)
}

// backupChange records every path c may change: its file and any move
// destination.
func backupChange(c fileChange) ([]pathBackup, error) {
	paths := []string{c.full}
	if c.dest != "" {
		paths = append(paths, c.dest)
	}
	var out []pathBackup
	for _, p := range paths {
		b, err := backupPath(p)
		if err != nil {
			return nil, fmt.Errorf("cannot record %s for rollback: %w", p, err)
		}
		out = append(out, b)
	}
	return out, nil
}

func backupPath(full string) (pathBackup, error) {
	b := pathBackup{path: full}
	fi, err := os.Lstat(full)
	if errors.Is(err, fs.ErrNotExist) {
		b.dirs = missingDirs(filepath.Dir(full))
		return b, nil
	}
	if err != nil {
		return b, err
	}
	b.exists = true
	if fi.Mode()&os.ModeSymlink != 0 {
		if b.link, err = os.Readlink(full); err != nil {
			return b, err
		}
		if fi, err = os.Stat(full); err != nil {
			return b, err
		}
	}
	if !fi.Mode().IsRegular() {
		return b, errors.New("not a regular file")
	}
	if b.data, err = os.ReadFile(full); err != nil {
		return b, err
	}
	b.mode = fi.Mode().Perm()
	return b, nil
}

// missingDirs lists dir and its missing ancestors, deepest first.
func missingDirs(dir string) []string {
	var out []string
	for {
		if _, err := os.Lstat(dir); !errors.Is(err, fs.ErrNotExist) {
			return out
		}
		out = append(out, dir)
		parent := filepath.Dir(dir)
		if parent == dir {
			return out
		}
		dir = parent
	}
}

// restoreFailure is a path rollback could not put back.
type restoreFailure struct {
	path string
	err  error
}

// rollback restores backups in reverse order, through fsys, and returns
// the paths it could not restore. It takes no undo snapshot and changes
// only the backed-up paths (through a symlink, for a symlinked file) and
// the directories this call created for them.
func rollback(fsys patchFS, backups []pathBackup) []restoreFailure {
	var failed []restoreFailure
	for i := len(backups) - 1; i >= 0; i-- {
		b := backups[i]
		if err := b.restore(fsys); err != nil {
			failed = append(failed, restoreFailure{b.path, err})
		}
		for _, d := range b.dirs {
			if err := fsys.Remove(d); err != nil && !errors.Is(err, fs.ErrNotExist) {
				failed = append(failed, restoreFailure{d, fmt.Errorf("created directory not removed: %w", err)})
				break
			}
		}
	}
	return failed
}

func (b pathBackup) restore(fsys patchFS) error {
	fi, err := os.Lstat(b.path)
	present := err == nil
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	switch {
	case !b.exists:
		if !present {
			return nil
		}
		if err := fsys.Remove(b.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	case b.link != "":
		if !present {
			if err := fsys.Symlink(b.link, b.path); err != nil {
				return err
			}
		} else if target, err := os.Readlink(b.path); err != nil || target != b.link {
			return errors.New("expected the original symlink but found something else; left as is")
		}
	case !present:
		if err := fsys.CreateFile(b.path, b.data, b.mode); err != nil {
			return err
		}
		return fsys.Chmod(b.path, b.mode)
	case !fi.Mode().IsRegular():
		return errors.New("is no longer a regular file; left as is")
	}
	if b.matches() {
		return nil
	}
	if err := fsys.WriteFile(b.path, b.data, b.mode); err != nil {
		return err
	}
	return fsys.Chmod(b.path, b.mode)
}

// matches reports whether the file at b.path (through a symlink) already
// has b's contents and mode, so restoring it would change nothing.
func (b pathBackup) matches() bool {
	fi, err := os.Stat(b.path)
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != b.mode {
		return false
	}
	data, err := os.ReadFile(b.path)
	return err == nil && bytes.Equal(data, b.data)
}

// rollbackError reports a write failure at operation i of changes and
// the outcome of rolling back.
func rollbackError(root string, changes []fileChange, i int, err error, failed []restoreFailure) error {
	var applied, pending []string
	for _, d := range changes[:i] {
		applied = append(applied, d.summary)
	}
	for _, d := range changes[i:] {
		pending = append(pending, d.summary)
	}
	label := changes[i].op.label(i)
	if len(failed) == 0 {
		return fmt.Errorf("%s failed: %w\nrolled back: %s; every file this patch touched is as it was before the call\nnot applied: %s",
			label, err, listOrNone(applied), strings.Join(pending, ", "))
	}
	var names, why []string
	for _, f := range failed {
		names = append(names, rel(root, f.path))
		why = append(why, fmt.Sprintf("  %s: %v", rel(root, f.path), f.err))
	}
	return fmt.Errorf("%s failed: %w\nrollback incomplete; unknown state: %s\n%s\nevery other path this patch touched was restored\nnot applied: %s",
		label, err, strings.Join(names, ", "), strings.Join(why, "\n"), strings.Join(pending, ", "))
}
