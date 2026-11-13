package tools

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// maxSymlinks caps the symlinks evalPath follows for one path, like the
// kernel's ELOOP limit.
const maxSymlinks = 255

// evalPath resolves every symlink in full the way the OS would when opening
// it, even when a link's target or a later component does not exist, so a
// write through a dangling symlink evaluates to the path it would create.
// Existing paths use filepath.EvalSymlinks. Otherwise the components are
// walked one at a time: a symlink's target is spliced in front of the
// components still to resolve, and ".." steps up from the real directory
// reached so far (never lexically across a symlink). Past the first
// missing component (or one under a regular file) nothing can be a
// symlink, so the rest is joined as is.
func evalPath(full string) (string, error) {
	full = filepath.Clean(full)
	if r, err := filepath.EvalSymlinks(full); err == nil {
		return r, nil
	}
	sep := string(filepath.Separator)
	vol := filepath.VolumeName(full)
	cur := vol + sep
	pending := pathParts(strings.TrimPrefix(full, vol))
	links, missing := 0, false
	for len(pending) > 0 {
		part := pending[0]
		pending = pending[1:]
		switch part {
		case "", ".":
			continue
		case "..":
			cur = filepath.Dir(cur)
			continue
		}
		next := filepath.Join(cur, part)
		if missing {
			cur = next
			continue
		}
		fi, err := os.Lstat(next)
		if errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
			missing = true
			cur = next
			continue
		}
		if err != nil {
			return "", err
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			cur = next
			continue
		}
		if links++; links > maxSymlinks {
			return "", fmt.Errorf("too many levels of symbolic links: %s", full)
		}
		target, err := os.Readlink(next)
		if err != nil {
			return "", err
		}
		if filepath.IsAbs(target) {
			tv := filepath.VolumeName(target)
			cur = tv + sep
			target = strings.TrimPrefix(target, tv)
		}
		pending = append(pathParts(target), pending...)
	}
	return cur, nil
}

// pathParts splits p on the separator (and "/" on Windows), keeping ".."
// so evalPath can apply it to the resolved directory.
func pathParts(p string) []string {
	return strings.FieldsFunc(p, func(r rune) bool { return r == filepath.Separator || r == '/' })
}
