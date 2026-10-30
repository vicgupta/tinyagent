package plugins

// ModuleStore is an append-only, content-addressed store of plugin module
// versions (WebAssembly code modules and data modules):
//
//	<dir>/<module>/<sha256>.wasm|.md|.json   the version, never overwritten
//	<dir>/<module>/<sha256>.meta.json        manifest, permissions, load time
//	<dir>/<module>/pinned.json               permissions of the first load
//	<dir>/<module>/current                   the active hash (atomic rename)
//	<dir>/<module>/source                    hash last ingested from the plugin
//
// Versions are only removed by Prune, which never removes current. Rollback
// moves current to an older hash, but only to one whose permissions are a
// subset of the first load's, and never after taint. Loading verifies the
// file's hash, so a tampered version is refused.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// DefaultKeep is how many versions besides current Prune keeps.
const DefaultKeep = 5

// ErrTampered is returned when a stored version doesn't match its hash.
var ErrTampered = errors.New("stored module does not match its hash")

// ModuleStore is rooted at Dir.
type ModuleStore struct {
	Dir string
	Now func() time.Time // for tests; time.Now when nil
}

// VersionMeta is a version's sidecar.
type VersionMeta struct {
	Hash        string          `json:"hash"`
	Ext         string          `json:"ext"`
	Manifest    json.RawMessage `json:"manifest,omitempty"`
	Permissions Permissions     `json:"permissions"`
	LoadedAt    time.Time       `json:"loaded_at"`
}

var (
	moduleNameRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
	hashRE       = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// StoreName is the store's directory name for a plugin's module.
func StoreName(plugin, module string) string {
	return strings.ReplaceAll(plugin, "/", "_") + "__" + module
}

func (s *ModuleStore) dir(module string) (string, error) {
	if !moduleNameRE.MatchString(module) || strings.Contains(module, "..") {
		return "", fmt.Errorf("invalid module name %q", module)
	}
	return filepath.Join(s.Dir, module), nil
}

func (s *ModuleStore) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func validExt(ext string) bool { return ext == "wasm" || ext == "md" || ext == "json" || ext == "toml" }

// Put adds a version (a no-op when the hash is already stored) and returns
// its hash. It does not move current. The first Put of a module pins its
// permissions.
func (s *ModuleStore) Put(module, ext string, data []byte, perms Permissions, manifest json.RawMessage) (string, error) {
	if !validExt(ext) {
		return "", fmt.Errorf("invalid module extension %q", ext)
	}
	d, err := s.dir(module)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(d, 0o700); err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	h := hex.EncodeToString(sum[:])
	if err := writeOnce(filepath.Join(d, h+"."+ext), data); err != nil {
		return "", err
	}
	meta, _ := json.MarshalIndent(VersionMeta{Hash: h, Ext: ext, Manifest: manifest, Permissions: perms, LoadedAt: s.now().UTC()}, "", "  ")
	if err := writeOnce(filepath.Join(d, h+".meta.json"), meta); err != nil {
		return "", err
	}
	pin, _ := json.Marshal(perms)
	if err := writeOnce(filepath.Join(d, "pinned.json"), pin); err != nil {
		return "", err
	}
	return h, nil
}

// writeOnce creates path with data unless it already exists; an existing
// file is never overwritten.
func writeOnce(path string, data []byte) error {
	if _, err := os.Lstat(path); err == nil {
		return nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Link fails when path exists, so a racing writer can't overwrite it.
	if err := os.Link(tmp.Name(), path); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return nil
}

// writeAtomic replaces path via a temp file and rename.
func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (s *ModuleStore) readHashFile(module, name string) (string, error) {
	d, err := s.dir(module)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(filepath.Join(d, name))
	if err != nil {
		return "", err
	}
	h := strings.TrimSpace(string(b))
	if !hashRE.MatchString(h) {
		return "", fmt.Errorf("module %s: invalid %s file", module, name)
	}
	return h, nil
}

// Current returns the active hash ("" with no error before any version).
func (s *ModuleStore) Current(module string) (string, error) {
	h, err := s.readHashFile(module, "current")
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return h, err
}

// SetCurrent atomically points current at a stored hash.
func (s *ModuleStore) SetCurrent(module, hash string) error {
	if _, err := s.Meta(module, hash); err != nil {
		return err
	}
	d, _ := s.dir(module)
	return writeAtomic(filepath.Join(d, "current"), []byte(hash+"\n"))
}

// Source returns the hash last ingested from the plugin's own file.
func (s *ModuleStore) Source(module string) string {
	h, _ := s.readHashFile(module, "source")
	return h
}

// SetSource records the hash last ingested from the plugin's own file.
func (s *ModuleStore) SetSource(module, hash string) error {
	d, err := s.dir(module)
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(d, "source"), []byte(hash+"\n"))
}

// Meta reads a version's sidecar.
func (s *ModuleStore) Meta(module, hash string) (*VersionMeta, error) {
	d, err := s.dir(module)
	if err != nil {
		return nil, err
	}
	if !hashRE.MatchString(hash) {
		return nil, fmt.Errorf("invalid hash %q (want 64 hex digits)", hash)
	}
	b, err := os.ReadFile(filepath.Join(d, hash+".meta.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("module %s has no version %s", module, hash)
	}
	if err != nil {
		return nil, err
	}
	var m VersionMeta
	if err := json.Unmarshal(b, &m); err != nil || m.Hash != hash || !validExt(m.Ext) {
		return nil, fmt.Errorf("module %s version %s: invalid metadata", module, hash)
	}
	return &m, nil
}

// Read returns a version's content after verifying its hash.
func (s *ModuleStore) Read(module, hash string) ([]byte, *VersionMeta, error) {
	m, err := s.Meta(module, hash)
	if err != nil {
		return nil, nil, err
	}
	d, _ := s.dir(module)
	b, err := os.ReadFile(filepath.Join(d, hash+"."+m.Ext))
	if err != nil {
		return nil, nil, err
	}
	sum := sha256.Sum256(b)
	if hex.EncodeToString(sum[:]) != hash {
		return nil, nil, fmt.Errorf("module %s version %s: %w; refusing to load it", module, hash[:12], ErrTampered)
	}
	return b, m, nil
}

// ReadCurrent reads and verifies the active version.
func (s *ModuleStore) ReadCurrent(module string) ([]byte, *VersionMeta, error) {
	h, err := s.Current(module)
	if err != nil {
		return nil, nil, err
	}
	if h == "" {
		return nil, nil, fmt.Errorf("module %s has no current version", module)
	}
	return s.Read(module, h)
}

// Pinned returns the first load's permissions.
func (s *ModuleStore) Pinned(module string) (Permissions, error) {
	var p Permissions
	d, err := s.dir(module)
	if err != nil {
		return p, err
	}
	b, err := os.ReadFile(filepath.Join(d, "pinned.json"))
	if err != nil {
		return p, err
	}
	return p, json.Unmarshal(b, &p)
}

// Rollback points current at an older stored version. It is refused after
// taint, when the version's permissions are wider than the first load's,
// or when the version fails its hash check.
func (s *ModuleStore) Rollback(module, hash string, taint Tainter) error {
	if taint != nil && taint.Tainted() {
		return ErrTainted
	}
	_, m, err := s.Read(module, hash)
	if err != nil {
		return err
	}
	pin, err := s.Pinned(module)
	if err != nil {
		return err
	}
	if err := m.Permissions.subsetOf(pin); err != nil {
		return fmt.Errorf("rollback of %s to %s: %w", module, hash[:12], err)
	}
	return s.SetCurrent(module, hash)
}

// Versions lists a module's versions, newest first.
func (s *ModuleStore) Versions(module string) ([]VersionMeta, error) {
	d, err := s.dir(module)
	if err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(d)
	if err != nil {
		return nil, err
	}
	var out []VersionMeta
	for _, e := range ents {
		h, ok := strings.CutSuffix(e.Name(), ".meta.json")
		if !ok || !hashRE.MatchString(h) {
			continue
		}
		if m, err := s.Meta(module, h); err == nil {
			out = append(out, *m)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].LoadedAt.After(out[j].LoadedAt) })
	return out, nil
}

// Modules lists the stored modules.
func (s *ModuleStore) Modules() ([]string, error) {
	ents, err := os.ReadDir(s.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() && moduleNameRE.MatchString(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// Prune removes all but current and the keep newest other versions of a
// module, returning the removed hashes. Current is never removed.
func (s *ModuleStore) Prune(module string, keep int) ([]string, error) {
	if keep < 0 {
		return nil, errors.New("keep must be >= 0")
	}
	cur, err := s.Current(module)
	if err != nil {
		return nil, err
	}
	vs, err := s.Versions(module)
	if err != nil {
		return nil, err
	}
	d, _ := s.dir(module)
	var removed []string
	n := 0
	for _, v := range vs {
		if v.Hash == cur {
			continue
		}
		if n < keep {
			n++
			continue
		}
		if err := os.Remove(filepath.Join(d, v.Hash+"."+v.Ext)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return removed, err
		}
		if err := os.Remove(filepath.Join(d, v.Hash+".meta.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
			return removed, err
		}
		removed = append(removed, v.Hash)
	}
	return removed, nil
}
