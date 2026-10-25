package tools

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Additional directory modes.
const (
	DirRead  = "read"
	DirWrite = "write"
)

// MaxExtraDirs caps the number of additional directories one run may use.
const MaxExtraDirs = 10

// ExtraDir is a directory outside the root that the built-in file tools may
// reach (additional_dirs config, --add-dir-read and --add-dir-write). Path is
// absolute, clean and symlink-resolved; Mode is DirRead or DirWrite.
type ExtraDir struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
}

// ValidDirMode reports whether m is a supported additional directory mode.
func ValidDirMode(m string) bool { return m == DirRead || m == DirWrite }

// ResolveExtraDirs validates dirs for a run rooted at root (canonical) and
// returns them with each path made absolute (relative paths are taken from
// root), cleaned and symlink-resolved. It rejects missing paths, files,
// filesystem roots, paths equal to, inside or containing root, entries that
// overlap one another, invalid modes and more than MaxExtraDirs entries.
// With readOnly, every directory is returned in read mode.
func ResolveExtraDirs(root string, dirs []ExtraDir, readOnly bool) ([]ExtraDir, error) {
	if len(dirs) == 0 {
		return nil, nil
	}
	if len(dirs) > MaxExtraDirs {
		return nil, fmt.Errorf("additional directories: %d entries; at most %d are allowed", len(dirs), MaxExtraDirs)
	}
	realRoot := filepath.Clean(root)
	if r, err := filepath.EvalSymlinks(realRoot); err == nil {
		realRoot = r
	}
	out := make([]ExtraDir, 0, len(dirs))
	for _, d := range dirs {
		name := d.Path
		if strings.TrimSpace(d.Path) == "" {
			return nil, errors.New("additional directory: empty path")
		}
		if !ValidDirMode(d.Mode) {
			if d.Mode == "" {
				return nil, fmt.Errorf("additional directory %q: mode is required (read or write)", name)
			}
			return nil, fmt.Errorf("additional directory %q: mode %q must be read or write", name, d.Mode)
		}
		p := d.Path
		if !filepath.IsAbs(p) {
			p = filepath.Join(root, p)
		}
		p = filepath.Clean(p)
		real, err := filepath.EvalSymlinks(p)
		if err != nil {
			return nil, fmt.Errorf("additional directory %q does not exist", name)
		}
		if fi, err := os.Stat(real); err != nil || !fi.IsDir() {
			return nil, fmt.Errorf("additional directory %q is not a directory", name)
		}
		if isFSRoot(real) {
			return nil, fmt.Errorf("additional directory %q is a filesystem root", name)
		}
		switch {
		case real == realRoot:
			return nil, fmt.Errorf("additional directory %q is the working directory (--cwd)", name)
		case within(realRoot, real):
			return nil, fmt.Errorf("additional directory %q is inside the working directory (--cwd) %s", name, root)
		case within(real, realRoot):
			return nil, fmt.Errorf("additional directory %q contains the working directory (--cwd) %s", name, root)
		}
		for _, prev := range out {
			switch {
			case prev.Path == real:
				return nil, fmt.Errorf("additional directory %q is listed twice", name)
			case within(prev.Path, real), within(real, prev.Path):
				return nil, fmt.Errorf("additional directory %q overlaps %s", name, prev.Path)
			}
		}
		mode := d.Mode
		if readOnly {
			mode = DirRead
		}
		out = append(out, ExtraDir{Path: real, Mode: mode})
	}
	return out, nil
}

// isFSRoot reports whether p is a filesystem root ("/" or a drive root).
func isFSRoot(p string) bool {
	return filepath.Dir(p) == p
}

// within reports whether p is strictly inside dir (lexically).
func within(dir, p string) bool {
	r, err := filepath.Rel(dir, p)
	return err == nil && r != "." && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator)) && !filepath.IsAbs(r)
}

// scope is the set of paths the built-in file tools may touch: the root,
// any additional directories and the file_access rules. A nil *scope (or
// one with no extras) reaches only the root, as before additional
// directories existed.
type scope struct {
	fa       *FileAccess
	extra    []ExtraDir
	readOnly bool
}

// scope returns e's path scope for the built-in file tools.
func (e *Executor) scope() *scope {
	return &scope{fa: e.FileAccess, extra: e.ExtraDirs, readOnly: e.ReadOnly}
}

func faScope(fa *FileAccess) *scope { return &scope{fa: fa} }

// resolve maps path to an absolute path under root or, for an absolute
// path, under one of the additional directories. Relative paths always
// resolve against root.
func (s *scope) resolve(root, path string) (string, error) {
	full, err := Resolve(root, path)
	if err == nil || s == nil || len(s.extra) == 0 || !filepath.IsAbs(path) {
		return full, err
	}
	full = filepath.Clean(path)
	if s.dirOf(full) != nil {
		return full, nil
	}
	return "", fmt.Errorf("path %q is outside the working directory and the additional directories", path)
}

// dirOf returns the additional directory containing full (or equal to it),
// or nil.
func (s *scope) dirOf(full string) *ExtraDir {
	if s == nil {
		return nil
	}
	for i := range s.extra {
		d := &s.extra[i]
		if full == d.Path || within(d.Path, full) {
			return d
		}
	}
	return nil
}

// checkRead applies the file_access deny_read rules.
func (s *scope) checkRead(full, name string) error {
	if s == nil {
		return nil
	}
	return s.fa.CheckRead(full, name)
}

// checkWrite refuses writes in read-mode additional directories (every
// additional directory in read-only runs), checked for full and its
// symlink-resolved target, then applies the file_access rules.
func (s *scope) checkWrite(full, name string) error {
	if s == nil {
		return nil
	}
	if len(s.extra) > 0 {
		targets := []string{filepath.Clean(full)}
		if t, ok := resolveExisting(full); ok && t != targets[0] {
			targets = append(targets, t)
		}
		for _, t := range targets {
			if d := s.dirOf(t); d != nil && (d.Mode != DirWrite || s.readOnly) {
				return &ReadDirError{Path: name, Dir: d.Path}
			}
		}
	}
	return s.fa.CheckWrite(full, name)
}

// hidden reports whether a walk should hide full.
func (s *scope) hidden(full string) bool {
	return s != nil && s.fa.Hidden(full)
}

// ReadDirError is returned for a write to a read-mode additional directory.
type ReadDirError struct {
	Path string // the path as the caller named it
	Dir  string // the additional directory
}

func (e *ReadDirError) Error() string {
	return fmt.Sprintf("%s is in additional directory %s, which has mode read; writes there are not allowed", e.Path, e.Dir)
}

// ExtraDirsNotice is the system prompt section that tells the model which
// additional directories it may use, or "" when there are none.
func ExtraDirsNotice(dirs []ExtraDir) string {
	if len(dirs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n" + extraDirsStart + "\n")
	b.WriteString("Besides the working directory, the file tools can reach these additional directories. Use absolute paths for them; relative paths resolve against the working directory. Directories with mode read can't be written.\n")
	for _, d := range dirs {
		fmt.Fprintf(&b, "- %s (%s)\n", d.Path, d.Mode)
	}
	b.WriteString(extraDirsEnd)
	return b.String()
}

const (
	extraDirsStart = "<additional_directories>"
	extraDirsEnd   = "</additional_directories>"
)

// StripExtraDirsNotice removes an ExtraDirsNotice section from a system
// prompt, so a prompt can be compared regardless of which directories it
// listed.
func StripExtraDirsNotice(system string) string {
	i := strings.Index(system, "\n\n"+extraDirsStart+"\n")
	if i < 0 {
		return system
	}
	j := strings.Index(system[i:], extraDirsEnd)
	if j < 0 {
		return system
	}
	return system[:i] + system[i+j+len(extraDirsEnd):]
}
