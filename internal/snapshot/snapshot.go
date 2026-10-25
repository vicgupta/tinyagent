// Package snapshot captures and restores the files under a working directory
// for opt-in workspace snapshots (--snapshots) and --rewind.
//
// A store lives at <root>/.tinyagent/snapshots. File contents are kept once
// each, by SHA-256, under objects/; each snapshot is a manifest under
// manifests/ listing every captured path with its type, permission bits and
// content hash. Nothing is ever deleted automatically.
package snapshot

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// DefaultMaxFileBytes is the default max_file_bytes (2 MiB).
const DefaultMaxFileBytes = 2 << 20

// StoreDir is the store's location relative to the working directory.
const StoreDir = ".tinyagent/snapshots"

// maxListedLarge bounds the oversized paths listed in a trajectory record
// and event. The manifest keeps them all.
const maxListedLarge = 20

const manifestVersion = 1

// Entry is one captured path.
type Entry struct {
	Path   string `json:"path"` // slash-separated, relative to the root
	Type   string `json:"type"` // "file" or "symlink"
	Mode   uint32 `json:"mode,omitempty"`
	Hash   string `json:"sha256,omitempty"`
	Size   int64  `json:"size,omitempty"`
	Target string `json:"target,omitempty"` // symlink target
}

// Manifest is one snapshot.
type Manifest struct {
	Version      int     `json:"version"`
	ID           string  `json:"id"`
	Root         string  `json:"root"`
	Kind         string  `json:"kind"`
	Step         int     `json:"step"`
	Time         string  `json:"time"`
	MaxFileBytes int64   `json:"max_file_bytes"`
	Entries      []Entry `json:"entries"`
	// Dirs lists every captured directory (without the root itself).
	Dirs []string `json:"dirs,omitempty"`
	// Ignored lists the gitignored paths left out (an ignored directory
	// once); Large lists the files over MaxFileBytes; Excluded lists the
	// configured exclusions (trajectory and event log files). A rewind never
	// removes any of them.
	Ignored  []string `json:"ignored,omitempty"`
	Large    []string `json:"large,omitempty"`
	Excluded []string `json:"excluded,omitempty"`
}

// Options configures a store.
type Options struct {
	// MaxFileBytes is the largest file captured; 0 means DefaultMaxFileBytes.
	MaxFileBytes int64
	// Exclude lists absolute paths never captured (a directory excludes
	// everything under it): the trajectory and the
	// event log database. A trajectory's .tinyagent-*.json save temps and
	// a database's -wal, -shm and -journal files are excluded with them.
	Exclude []string
}

// Store reads and writes one store.
type Store struct {
	Root string // canonical working directory
	Dir  string // the store directory
	opts Options
	// cache maps a path to its last hashed stat, so unchanged files are not
	// read again.
	cache map[string]cached
	now   func() time.Time
}

type cached struct {
	size  int64
	mtime time.Time
	mode  fs.FileMode
	hash  string
	at    time.Time
}

// Open creates (or reuses) the store under root.
func Open(root string, opts Options) (*Store, error) {
	if opts.MaxFileBytes <= 0 {
		opts.MaxFileBytes = DefaultMaxFileBytes
	}
	s := &Store{Root: root, Dir: filepath.Join(root, filepath.FromSlash(StoreDir)), opts: opts, cache: map[string]cached{}, now: time.Now}
	for _, d := range []string{filepath.Dir(s.Dir), s.Dir, s.objects(), s.manifests()} {
		if err := os.Mkdir(d, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("create snapshot store: %w", err)
		}
	}
	if fi, err := os.Stat(s.Dir); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("create snapshot store: %s is not a directory", s.Dir)
	}
	gi := filepath.Join(s.Dir, ".gitignore")
	if _, err := os.Stat(gi); errors.Is(err, fs.ErrNotExist) {
		if err := os.WriteFile(gi, []byte("*\n"), 0o600); err != nil {
			return nil, fmt.Errorf("create snapshot store: %w", err)
		}
	}
	// Prove the store is writable now rather than at the first step.
	f, err := os.CreateTemp(s.objects(), ".probe-*")
	if err != nil {
		return nil, fmt.Errorf("create snapshot store: %w", err)
	}
	f.Close()
	os.Remove(f.Name())
	return s, nil
}

// OpenExisting opens the store at dir for root without creating anything.
func OpenExisting(root, dir string, opts Options) (*Store, error) {
	if opts.MaxFileBytes <= 0 {
		opts.MaxFileBytes = DefaultMaxFileBytes
	}
	s := &Store{Root: root, Dir: dir, opts: opts, cache: map[string]cached{}, now: time.Now}
	for _, d := range []string{s.objects(), s.manifests()} {
		if fi, err := os.Stat(d); err != nil || !fi.IsDir() {
			return nil, fmt.Errorf("snapshot store %s is missing or incomplete", dir)
		}
	}
	return s, nil
}

func (s *Store) objects() string   { return filepath.Join(s.Dir, "objects") }
func (s *Store) manifests() string { return filepath.Join(s.Dir, "manifests") }

func (s *Store) object(hash string) string {
	return filepath.Join(s.objects(), hash[:2], hash)
}

// Take captures the working directory and returns its trajectory record.
func (s *Store) Take(kind string, step int) (trajectory.Snapshot, error) {
	start := s.now()
	m, err := s.scan(true)
	if err != nil {
		return trajectory.Snapshot{}, err
	}
	id, err := newID()
	if err != nil {
		return trajectory.Snapshot{}, err
	}
	m.ID, m.Kind, m.Step, m.Time = id, kind, step, start.UTC().Format(time.RFC3339Nano)
	data, err := json.Marshal(m)
	if err != nil {
		return trajectory.Snapshot{}, err
	}
	if err := writeAtomic(filepath.Join(s.manifests(), id+".json"), data); err != nil {
		return trajectory.Snapshot{}, fmt.Errorf("write snapshot manifest: %w", err)
	}
	rec := trajectory.Snapshot{ID: id, Step: step, Kind: kind, Time: m.Time, Files: len(m.Entries),
		SkippedIgnored: len(m.Ignored), SkippedLarge: len(m.Large), Store: s.Dir}
	for _, e := range m.Entries {
		rec.Bytes += e.Size
	}
	if len(m.Large) > 0 {
		rec.LargePaths = append([]string(nil), m.Large[:min(len(m.Large), maxListedLarge)]...)
	}
	return rec, nil
}

// Current scans the working directory without storing anything.
func (s *Store) Current() (*Manifest, error) { return s.scan(false) }

// Load reads a manifest, checking it belongs to this root and that every
// object it needs is present.
func (s *Store) Load(id string) (*Manifest, error) {
	if id == "" || strings.ContainsAny(id, `/\.`) {
		return nil, fmt.Errorf("invalid snapshot id %q", id)
	}
	data, err := os.ReadFile(filepath.Join(s.manifests(), id+".json"))
	if err != nil {
		return nil, fmt.Errorf("snapshot %s: %w", id, err)
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("snapshot %s: corrupt manifest: %w", id, err)
	}
	if m.Version != manifestVersion || m.ID != id {
		return nil, fmt.Errorf("snapshot %s: corrupt manifest", id)
	}
	if m.Root != s.Root {
		return nil, fmt.Errorf("snapshot %s was taken in %s, not %s", id, m.Root, s.Root)
	}
	for _, e := range m.Entries {
		if !validRel(e.Path) || (e.Type != "file" && e.Type != "symlink") {
			return nil, fmt.Errorf("snapshot %s: corrupt entry %q", id, e.Path)
		}
		if e.Type == "file" {
			if len(e.Hash) != 64 {
				return nil, fmt.Errorf("snapshot %s: corrupt entry %q", id, e.Path)
			}
			if _, err := os.Stat(s.object(e.Hash)); err != nil {
				return nil, fmt.Errorf("snapshot %s: content of %s is missing from the store", id, e.Path)
			}
		}
	}
	for _, d := range m.Dirs {
		if !validRel(d) {
			return nil, fmt.Errorf("snapshot %s: corrupt directory %q", id, d)
		}
	}
	return &m, nil
}

func validRel(p string) bool {
	return p != "" && filepath.IsLocal(filepath.FromSlash(p)) && path.Clean(p) == p
}

func newID() (string, error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "snap-" + hex.EncodeToString(b[:]), nil
}

// excluded reports whether the absolute path p is one of the configured
// exclusions or a companion file of one.
func (s *Store) excluded(p string) bool {
	for _, x := range s.opts.Exclude {
		if p == x || p == x+"-wal" || p == x+"-shm" || p == x+"-journal" {
			return true
		}
		if filepath.Dir(p) == filepath.Dir(x) && strings.HasSuffix(x, ".json") {
			if b := filepath.Base(p); strings.HasPrefix(b, ".tinyagent-") && strings.HasSuffix(b, ".json") {
				return true
			}
		}
	}
	return false
}

func (s *Store) relExcluded() []string {
	var out []string
	for _, x := range s.opts.Exclude {
		if rel, err := filepath.Rel(s.Root, x); err == nil && filepath.IsLocal(rel) {
			out = append(out, filepath.ToSlash(rel))
		}
	}
	return out
}

// scan walks the root. With store set it copies new content into objects/.
func (s *Store) scan(store bool) (*Manifest, error) {
	m := &Manifest{Version: manifestVersion, Root: s.Root, MaxFileBytes: s.opts.MaxFileBytes, Entries: []Entry{}, Excluded: s.relExcluded()}
	ig := newIgnorer(s.Root)
	repoPrefix := ""
	if ig != nil {
		if rel, err := filepath.Rel(ig.repo, s.Root); err == nil && rel != "." {
			repoPrefix = filepath.ToSlash(rel) + "/"
		}
	}
	scanStart := s.now()
	err := filepath.WalkDir(s.Root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == s.Root {
			return nil
		}
		rel, _ := filepath.Rel(s.Root, p)
		rel = filepath.ToSlash(rel)
		isDir := d.IsDir()
		if d.Name() == ".git" || rel == ".tinyagent" || p == s.Dir {
			if isDir {
				return filepath.SkipDir
			}
			return nil
		}
		if s.excluded(p) {
			if isDir {
				return filepath.SkipDir
			}
			return nil
		}
		if ig.ignored(repoPrefix+rel, isDir) {
			m.Ignored = append(m.Ignored, rel)
			if isDir {
				return filepath.SkipDir
			}
			return nil
		}
		if isDir {
			m.Dirs = append(m.Dirs, rel)
			if ig != nil {
				ig.load(repoPrefix + rel)
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			m.Entries = append(m.Entries, Entry{Path: rel, Type: "symlink", Target: target})
		case info.Mode().IsRegular():
			if info.Size() > s.opts.MaxFileBytes {
				m.Large = append(m.Large, rel)
				return nil
			}
			hash, err := s.hashFile(p, rel, info, store, scanStart)
			if err != nil {
				return err
			}
			m.Entries = append(m.Entries, Entry{Path: rel, Type: "file", Mode: uint32(info.Mode().Perm()), Hash: hash, Size: info.Size()})
		}
		// Sockets, devices and named pipes are not captured.
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scan workspace: %w", err)
	}
	return m, nil
}

// hashFile returns p's SHA-256, storing the content when store is set. A
// file whose size, mtime and mode match the last scan is not read again,
// unless its mtime is too close to that scan to be trusted.
func (s *Store) hashFile(p, rel string, info fs.FileInfo, store bool, scanStart time.Time) (string, error) {
	if c, ok := s.cache[rel]; ok && c.size == info.Size() && c.mtime.Equal(info.ModTime()) && c.mode == info.Mode() &&
		info.ModTime().Before(c.at.Add(-2*time.Second)) {
		if !store {
			return c.hash, nil
		}
		if _, err := os.Stat(s.object(c.hash)); err == nil {
			return c.hash, nil
		}
	}
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var tmp *os.File
	h := sha256.New()
	w := io.Writer(h)
	if store {
		tmp, err = os.CreateTemp(s.objects(), ".tmp-*")
		if err != nil {
			return "", err
		}
		defer os.Remove(tmp.Name())
		defer tmp.Close()
		w = io.MultiWriter(w, tmp)
	}
	if _, err := io.Copy(w, f); err != nil {
		return "", err
	}
	hash := hex.EncodeToString(h.Sum(nil))
	if store {
		dst := s.object(hash)
		if _, err := os.Stat(dst); errors.Is(err, fs.ErrNotExist) {
			if err := tmp.Close(); err != nil {
				return "", err
			}
			if err := os.Mkdir(filepath.Dir(dst), 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
				return "", err
			}
			if err := os.Chmod(tmp.Name(), 0o600); err != nil {
				return "", err
			}
			if err := os.Rename(tmp.Name(), dst); err != nil {
				return "", err
			}
		}
	}
	s.cache[rel] = cached{size: info.Size(), mtime: info.ModTime(), mode: info.Mode(), hash: hash, at: scanStart}
	return hash, nil
}

// writeAtomic writes data to a new file at p (0600) through a temp file.
func writeAtomic(p string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(p), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(f.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(f.Name(), p)
}

// sortedPaths returns the keys of m, sorted.
func sortedPaths[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
