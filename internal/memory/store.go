package memory

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// fileVersion is the store format version.
const fileVersion = 1

type storeFile struct {
	Version int     `json:"version"`
	Entries []Entry `json:"entries"`
}

// CorruptError reports a store file that exists but can't be parsed. A
// corrupt store is never overwritten.
type CorruptError struct {
	Path string
	Err  error
}

func (e *CorruptError) Error() string {
	return fmt.Sprintf("memory store %s is unreadable (%v); it was left unchanged", e.Path, e.Err)
}

func (e *CorruptError) Unwrap() error { return e.Err }

// Load reads the store at path. A missing file is an empty store. A file
// that can't be parsed returns a *CorruptError.
func Load(path string) ([]Entry, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f storeFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, &CorruptError{Path: path, Err: err}
	}
	if f.Version != fileVersion {
		return nil, &CorruptError{Path: path, Err: fmt.Errorf("unsupported version %d", f.Version)}
	}
	for _, e := range f.Entries {
		if e.ID == "" || (e.Status != StatusActive && e.Status != StatusPending) {
			return nil, &CorruptError{Path: path, Err: fmt.Errorf("invalid entry %q", e.ID)}
		}
	}
	return f.Entries, nil
}

// Update re-reads the store at path under an exclusive lock, applies fn and
// writes the result atomically, so concurrent writers never lose each
// other's changes. If fn returns an error nothing is written. A corrupt
// store is left byte-identical and its *CorruptError is returned.
func Update(path string, fn func([]Entry) ([]Entry, error)) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	unlock, err := lock(path)
	if err != nil {
		return err
	}
	defer unlock()
	entries, err := Load(path)
	if err != nil {
		return err
	}
	out, err := fn(entries)
	if err != nil {
		return err
	}
	return writeAtomic(path, out)
}

// writeAtomic writes entries to a temp file in path's directory with mode
// 0600, syncs it and renames it over path.
func writeAtomic(path string, entries []Entry) error {
	if entries == nil {
		entries = []Entry{}
	}
	data, err := json.MarshalIndent(storeFile{Version: fileVersion, Entries: entries}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".memory-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			tmp.Close()
			os.Remove(name)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	ok = true
	return nil
}

// Lock timing; variables so tests can shorten them.
var (
	lockWait  = 10 * time.Second
	lockStale = 60 * time.Second
	lockPoll  = 10 * time.Millisecond
)

// lock takes an exclusive lock on path by creating path+".lock". A lock
// file older than lockStale is treated as left behind by a crashed process
// and removed. It works the same on every platform and needs no cgo.
func lock(path string) (func(), error) {
	lp := path + ".lock"
	deadline := time.Now().Add(lockWait)
	for {
		f, err := os.OpenFile(lp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			fmt.Fprintf(f, "%d\n", os.Getpid())
			f.Close()
			return func() { os.Remove(lp) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if fi, serr := os.Stat(lp); serr == nil && time.Since(fi.ModTime()) > lockStale {
			os.Remove(lp)
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("memory store %s is locked by another process (remove %s if no tinyagent is running)", path, lp)
		}
		time.Sleep(lockPoll)
	}
}

// Approve activates the pending entry id, or every pending entry when all is
// true, and returns how many were approved. Approving a pending replacement
// removes the active entry it replaces.
func Approve(path, id string, all bool) (int, error) {
	n := 0
	err := Update(path, func(entries []Entry) ([]Entry, error) {
		now := time.Now().UTC()
		replaced := map[string]bool{}
		for i := range entries {
			e := &entries[i]
			if e.Status != StatusPending || (!all && e.ID != id) {
				continue
			}
			e.Status = StatusActive
			e.UpdatedAt = now
			if e.Replaces != "" {
				replaced[e.Replaces] = true
				e.Replaces = ""
			}
			n++
		}
		if n == 0 {
			if all {
				return nil, errNothing
			}
			if find(entries, id) < 0 {
				return nil, ErrNotFound
			}
			return nil, fmt.Errorf("memory entry %s is already active", id)
		}
		out := entries[:0:0]
		for _, e := range entries {
			if !replaced[e.ID] {
				out = append(out, e)
			}
		}
		return out, nil
	})
	if errors.Is(err, errNothing) {
		return 0, nil
	}
	return n, err
}

var errNothing = errors.New("nothing to change")

// Forget removes entry id (active or pending), along with any pending
// replacement of it.
func Forget(path, id string) error {
	return Update(path, func(entries []Entry) ([]Entry, error) {
		if find(entries, id) < 0 {
			return nil, ErrNotFound
		}
		out := entries[:0:0]
		for _, e := range entries {
			if e.ID != id && e.Replaces != id {
				out = append(out, e)
			}
		}
		return out, nil
	})
}

// ForgetAll removes every entry and returns how many there were. A missing
// store stays missing.
func ForgetAll(path string) (int, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	n := 0
	err := Update(path, func(entries []Entry) ([]Entry, error) {
		n = len(entries)
		return []Entry{}, nil
	})
	return n, err
}
