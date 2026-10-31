// Package cwdpath confines paths to the working directory (--cwd). Agent
// Skills and playbooks are read only from inside it: a reference that is
// absolute, contains "..", or resolves (after filepath.Clean and
// filepath.EvalSymlinks) outside root is refused.
package cwdpath

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Ref resolves ref, a path relative to root, and returns its resolved path.
// It refuses an absolute ref, a ref with a ".." element, and a ref whose
// resolved path (symlinks followed) is outside root. A ref that does not
// exist yet is checked lexically and returned joined to root.
func Ref(root, ref string) (string, error) {
	if ref == "" {
		return "", fmt.Errorf("empty path")
	}
	if filepath.IsAbs(ref) || strings.HasPrefix(ref, "/") || strings.HasPrefix(ref, `\`) || filepath.VolumeName(ref) != "" {
		return "", fmt.Errorf("%s: absolute paths are refused; paths must be inside the working directory", ref)
	}
	for _, el := range strings.FieldsFunc(filepath.ToSlash(ref), func(r rune) bool { return r == '/' }) {
		if el == ".." {
			return "", fmt.Errorf("%s: paths containing \"..\" are refused", ref)
		}
	}
	return Resolve(root, filepath.Join(root, filepath.Clean(ref)))
}

// Resolve checks that path (absolute, or relative to the process) stays
// inside root after filepath.Clean and filepath.EvalSymlinks, and returns
// the resolved path. A path that does not exist is checked lexically.
func Resolve(root, path string) (string, error) {
	rr, err := filepath.EvalSymlinks(root)
	if err != nil {
		rr = filepath.Clean(root)
	}
	clean := filepath.Clean(path)
	real, err := filepath.EvalSymlinks(clean)
	if err != nil {
		if !os.IsNotExist(err) {
			return "", err
		}
		// Not there yet: resolve the nearest existing parent.
		real = clean
		if parent, perr := filepath.EvalSymlinks(filepath.Dir(clean)); perr == nil {
			real = filepath.Join(parent, filepath.Base(clean))
		}
	}
	if !Within(rr, real) && !Within(filepath.Clean(root), real) {
		return "", fmt.Errorf("%s resolves outside the working directory %s; refused", path, root)
	}
	return real, nil
}

// Within reports whether path is root or below it (both already clean).
func Within(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
