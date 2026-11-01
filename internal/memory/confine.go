package memory

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ErrOutsideCwd marks a store path that was refused because it would leave
// the working directory (issue #335).
var ErrOutsideCwd = errors.New("memory store must stay inside --cwd")

// confine resolves the store path p against cwd and refuses one that would
// leave cwd: an absolute path, a path with a ".." element, or one whose
// existing part resolves through a symlink to somewhere outside cwd. Nothing
// outside cwd is read: only cwd itself and the path below it are resolved.
func confine(p, cwd string) (string, error) {
	if p == "" || filepath.IsAbs(p) || filepath.VolumeName(p) != "" || strings.HasPrefix(p, "~") {
		return "", fmt.Errorf("%w: %q is not a path relative to it", ErrOutsideCwd, p)
	}
	for _, el := range strings.Split(filepath.ToSlash(p), "/") {
		if el == ".." {
			return "", fmt.Errorf("%w: %q contains \"..\"", ErrOutsideCwd, p)
		}
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return "", err
	}
	// The returned path keeps cwd as given (so callers see the same prefix
	// they passed); the containment check uses cwd with symlinks resolved.
	out := filepath.Join(abs, filepath.Clean(p))
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		abs = r
	}
	full := filepath.Join(abs, filepath.Clean(p))
	if full == abs {
		return "", fmt.Errorf("%w: %q is --cwd itself", ErrOutsideCwd, p)
	}
	// Resolve the longest existing prefix: a symlink inside cwd that points
	// outside it is refused.
	existing, rest := full, ""
	for existing != abs {
		if _, err := os.Lstat(existing); err == nil {
			break
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		rest = filepath.Join(filepath.Base(existing), rest)
		existing = filepath.Dir(existing)
	}
	resolved, err := filepath.EvalSymlinks(existing)
	if errors.Is(err, fs.ErrNotExist) && existing == abs {
		// cwd itself doesn't exist yet: there is nothing to follow.
		return out, nil
	}
	if err != nil {
		return "", err
	}
	if !within(resolved, abs) {
		return "", fmt.Errorf("%w: %q resolves through a symlink to %s", ErrOutsideCwd, p, resolved)
	}
	return out, nil
}

// within reports whether path is dir or below it.
func within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// OldDefaultStore returns the store the previous default location (./memory
// in cwd, before issue #335) holds for backend, or "" when there is none.
// Only cwd is looked at. Callers print a one-line hint; nothing is moved.
func OldDefaultStore(backend, cwd string) string {
	var p string
	switch backend {
	case "", BackendMarkdown:
		p = filepath.Join(cwd, "memory")
		ents, err := os.ReadDir(p)
		if err != nil {
			return ""
		}
		for _, e := range ents {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
				return p
			}
		}
		return ""
	case BackendSQLite:
		p = filepath.Join(cwd, "memory", "memory.sqlite")
	default:
		return ""
	}
	if fi, err := os.Lstat(p); err == nil && fi.Mode().IsRegular() {
		return p
	}
	return ""
}
