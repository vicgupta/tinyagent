// Package cwdpath confines paths to the working directory (--cwd).
//
// TinyAgent reads and writes its project state only inside --cwd. Resolve
// turns a path from config or the environment into one inside cwd, or
// refuses it: a path that leaves cwd through "..", an absolute path
// elsewhere, or a symlink that resolves outside cwd. Only cwd itself and
// the path below it are looked at.
package cwdpath

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// ErrOutside marks a path that was refused because it would leave --cwd.
var ErrOutside = errors.New("path must stay inside --cwd")

// Resolve returns p (relative to cwd, or absolute) as an absolute path
// inside cwd. It refuses, wrapping ErrOutside, a path with a ".." element,
// an absolute path outside cwd, cwd itself, and one whose existing part
// resolves through a symlink to somewhere outside cwd. The path need not
// exist. The returned path keeps cwd as given (made absolute).
func Resolve(cwd, p string) (string, error) {
	if p == "" || strings.HasPrefix(p, "~") {
		return "", fmt.Errorf("%w: %q is not a path inside it", ErrOutside, p)
	}
	for _, el := range strings.Split(filepath.ToSlash(p), "/") {
		if el == ".." {
			return "", fmt.Errorf("%w: %q contains \"..\"", ErrOutside, p)
		}
	}
	if cwd == "" {
		cwd = "."
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return "", err
	}
	real := abs
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		real = r
	}
	var out, full string
	if filepath.IsAbs(p) || filepath.VolumeName(p) != "" {
		c := filepath.Clean(p)
		switch {
		case Within(c, abs):
			rel, _ := filepath.Rel(abs, c)
			out, full = c, filepath.Join(real, rel)
		case Within(c, real):
			rel, _ := filepath.Rel(real, c)
			out, full = filepath.Join(abs, rel), c
		default:
			return "", fmt.Errorf("%w: %s is outside %s", ErrOutside, p, abs)
		}
	} else {
		out = filepath.Join(abs, filepath.Clean(p))
		full = filepath.Join(real, filepath.Clean(p))
	}
	if full == real {
		return "", fmt.Errorf("%w: %q is --cwd itself", ErrOutside, p)
	}
	// Resolve the longest existing prefix: a symlink inside cwd that points
	// outside it is refused.
	existing := full
	for existing != real {
		if _, err := os.Lstat(existing); err == nil {
			break
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		existing = filepath.Dir(existing)
	}
	resolved, err := filepath.EvalSymlinks(existing)
	if errors.Is(err, fs.ErrNotExist) && existing == real {
		return out, nil // cwd itself doesn't exist yet
	}
	if err != nil {
		return "", err
	}
	if !Within(resolved, real) {
		return "", fmt.Errorf("%w: %q resolves through a symlink to %s", ErrOutside, p, resolved)
	}
	return out, nil
}

// Within reports whether path is dir or below it (lexically).
func Within(path, dir string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// Ref resolves ref, a path written relative to cwd (for example a playbook's
// workflow: file), like Resolve, but also refuses an absolute ref: such
// references must name a file inside cwd by a relative path.
func Ref(cwd, ref string) (string, error) {
	if filepath.IsAbs(ref) || strings.HasPrefix(ref, "/") || strings.HasPrefix(ref, `\`) || filepath.VolumeName(ref) != "" {
		return "", fmt.Errorf("%w: %q is an absolute path; use a path relative to --cwd", ErrOutside, ref)
	}
	return Resolve(cwd, ref)
}
