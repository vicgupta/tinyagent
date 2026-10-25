package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Change is one difference between the working directory and a snapshot.
type Change struct {
	// Op is "M" (modified: restored to the snapshot), "A" (added since:
	// removed) or "D" (deleted since: recreated).
	Op   string
	Path string
	want *Entry
}

// Plan lists what restoring target would change, sorted by path.
type Plan struct {
	Changes []Change
	// removeDirs are directories absent from target that the restore
	// removes once empty, deepest first.
	removeDirs []string
	makeDirs   []string
}

// Counts returns how many paths are modified, added and deleted.
func (p *Plan) Counts() (m, a, d int) {
	for _, c := range p.Changes {
		switch c.Op {
		case "M":
			m++
		case "A":
			a++
		case "D":
			d++
		}
	}
	return
}

// PlanRestore compares the working directory with target. Paths target left
// out (gitignored, oversized or excluded) are never touched.
func (s *Store) PlanRestore(target *Manifest) (*Plan, error) {
	cur, err := s.Current()
	if err != nil {
		return nil, err
	}
	want := map[string]*Entry{}
	for i := range target.Entries {
		want[target.Entries[i].Path] = &target.Entries[i]
	}
	have := map[string]*Entry{}
	for i := range cur.Entries {
		have[cur.Entries[i].Path] = &cur.Entries[i]
	}
	keep := append(append(append([]string(nil), target.Ignored...), target.Large...), target.Excluded...)
	outside := func(p string) bool {
		for _, k := range keep {
			if p == k || strings.HasPrefix(p, k+"/") {
				return true
			}
		}
		return false
	}
	plan := &Plan{}
	for _, p := range sortedPaths(want) {
		w := want[p]
		h, ok := have[p]
		switch {
		case ok && same(h, w):
		case ok:
			plan.Changes = append(plan.Changes, Change{Op: "M", Path: p, want: w})
		default:
			// Present but no longer captured (now oversized or ignored) is
			// still a modification; otherwise it was deleted.
			op := "D"
			if _, err := os.Lstat(filepath.Join(s.Root, filepath.FromSlash(p))); err == nil {
				op = "M"
			}
			plan.Changes = append(plan.Changes, Change{Op: op, Path: p, want: w})
		}
	}
	for _, p := range sortedPaths(have) {
		if _, ok := want[p]; !ok && !outside(p) {
			plan.Changes = append(plan.Changes, Change{Op: "A", Path: p})
		}
	}
	sort.Slice(plan.Changes, func(i, j int) bool { return plan.Changes[i].Path < plan.Changes[j].Path })
	wantDirs := map[string]bool{}
	for _, d := range target.Dirs {
		wantDirs[d] = true
	}
	for _, d := range cur.Dirs {
		if !wantDirs[d] && !outside(d) {
			plan.removeDirs = append(plan.removeDirs, d)
		}
	}
	sort.Slice(plan.removeDirs, func(i, j int) bool {
		return strings.Count(plan.removeDirs[i], "/") > strings.Count(plan.removeDirs[j], "/") ||
			(strings.Count(plan.removeDirs[i], "/") == strings.Count(plan.removeDirs[j], "/") && plan.removeDirs[i] > plan.removeDirs[j])
	})
	plan.makeDirs = append([]string(nil), target.Dirs...)
	sort.Strings(plan.makeDirs)
	return plan, nil
}

func same(a, b *Entry) bool {
	if a.Type != b.Type {
		return false
	}
	if a.Type == "symlink" {
		return a.Target == b.Target
	}
	return a.Hash == b.Hash && a.Mode == b.Mode
}

// Result reports a restore.
type Result struct {
	Restored []string
	// NotRestored lists the path that failed first and every planned path
	// after it; the restore stops at the first failure.
	NotRestored []string
	Err         error
}

// Apply carries out plan: removals first, then directories, then writes. It
// stops at the first failure.
func (s *Store) Apply(plan *Plan) Result {
	var res Result
	var removals, writes []Change
	for _, c := range plan.Changes {
		if c.Op == "A" {
			removals = append(removals, c)
		} else {
			writes = append(writes, c)
		}
	}
	// stop marks the failing path and everything not yet done as not
	// restored.
	stop := func(rest []Change, err error) Result {
		for _, c := range rest {
			res.NotRestored = append(res.NotRestored, c.Path)
		}
		res.Err = fmt.Errorf("%s: %w", rest[0].Path, err)
		sort.Strings(res.Restored)
		sort.Strings(res.NotRestored)
		return res
	}
	for i, c := range removals {
		if err := s.remove(c.Path); err != nil {
			return stop(append(append([]Change(nil), removals[i:]...), writes...), err)
		}
		res.Restored = append(res.Restored, c.Path)
	}
	for _, d := range plan.removeDirs {
		if s.safe(d) == nil {
			os.Remove(filepath.Join(s.Root, filepath.FromSlash(d))) // only if empty
		}
	}
	for _, d := range plan.makeDirs {
		if err := s.safe(d); err != nil {
			continue
		}
		p := filepath.Join(s.Root, filepath.FromSlash(d))
		if fi, err := os.Lstat(p); err == nil && fi.IsDir() {
			continue
		}
		os.Mkdir(p, 0o755)
	}
	for i, c := range writes {
		if err := s.restore(c.want); err != nil {
			return stop(writes[i:], err)
		}
		res.Restored = append(res.Restored, c.Path)
	}
	sort.Strings(res.Restored)
	return res
}

// safe refuses a path that is not local or whose parent directories, inside
// the root, include a symlink: writing through one could land outside it.
func (s *Store) safe(rel string) error {
	if !validRel(rel) {
		return fmt.Errorf("refusing unsafe path")
	}
	dir := path.Dir(rel)
	if dir == "." {
		return nil
	}
	cur := s.Root
	for _, part := range strings.Split(dir, "/") {
		cur = filepath.Join(cur, part)
		fi, err := os.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			return nil // created as a real directory
		}
		if err != nil {
			return err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("refusing to write through symlinked directory %s", filepath.ToSlash(strings.TrimPrefix(cur, s.Root+string(filepath.Separator))))
		}
		if !fi.IsDir() {
			return fmt.Errorf("parent %s is not a directory", part)
		}
	}
	return nil
}

func (s *Store) remove(rel string) error {
	if err := s.safe(rel); err != nil {
		return err
	}
	return os.Remove(filepath.Join(s.Root, filepath.FromSlash(rel)))
}

func (s *Store) restore(e *Entry) error {
	if err := s.safe(e.Path); err != nil {
		return err
	}
	dst := filepath.Join(s.Root, filepath.FromSlash(e.Path))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if err := s.safe(e.Path); err != nil { // re-check after creating parents
		return err
	}
	if fi, err := os.Lstat(dst); err == nil && fi.IsDir() {
		// A directory now stands where the snapshot had a file; it is
		// replaced only when empty.
		if err := os.Remove(dst); err != nil {
			return fmt.Errorf("a directory is in the way: %w", err)
		}
	}
	if e.Type == "symlink" {
		tmp := filepath.Join(filepath.Dir(dst), ".tinyagent-rewind-"+path.Base(e.Path))
		os.Remove(tmp)
		if err := os.Symlink(e.Target, tmp); err != nil {
			return err
		}
		if err := os.Rename(tmp, dst); err != nil {
			os.Remove(tmp)
			return err
		}
		return nil
	}
	src, err := os.Open(s.object(e.Hash))
	if err != nil {
		return fmt.Errorf("content missing from the snapshot store: %w", err)
	}
	defer src.Close()
	f, err := os.CreateTemp(filepath.Dir(dst), ".tinyagent-rewind-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), src); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != e.Hash {
		return errors.New("stored content is corrupt (hash mismatch)")
	}
	if err := os.Chmod(f.Name(), fs.FileMode(e.Mode).Perm()); err != nil {
		return err
	}
	return os.Rename(f.Name(), dst)
}
