package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/vicgupta/tinyagent/internal/gitwt"
)

// ProjectRoot returns the path that identifies cwd's project: the Git
// common directory when cwd is inside a Git work tree, so the main checkout
// and every linked worktree share one store, else the absolute cwd. Symlinks
// are resolved either way.
func ProjectRoot(cwd string) (string, error) {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		abs = r
	}
	if _, err := exec.LookPath("git"); err == nil {
		cmd := exec.Command("git", "-C", abs, "rev-parse", "--git-common-dir")
		cmd.Env = gitwt.Env()
		if out, err := cmd.Output(); err == nil {
			dir := strings.TrimSpace(string(out))
			if dir != "" {
				if !filepath.IsAbs(dir) {
					dir = filepath.Join(abs, dir)
				}
				if r, err := filepath.EvalSymlinks(dir); err == nil {
					dir = r
				}
				return filepath.Clean(dir), nil
			}
		}
	}
	return filepath.Clean(abs), nil
}

var unsafeKeyChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// ProjectKey returns the store file's base name for a project root: a
// readable name plus a hash of the full path, so two projects with the same
// directory name never share a store.
func ProjectKey(root string) string {
	name := filepath.Base(root)
	if name == ".git" {
		name = filepath.Base(filepath.Dir(root))
	}
	name = strings.Trim(unsafeKeyChars.ReplaceAllString(name, "-"), "-.")
	if len(name) > 40 {
		name = name[:40]
	}
	if name == "" {
		name = "project"
	}
	sum := sha256.Sum256([]byte(root))
	return name + "-" + hex.EncodeToString(sum[:8])
}

// StorePath returns the legacy JSON store file for cwd's project under dir.
// There is no default directory: since issue #335 tinyagent never looks for
// memory outside --cwd, so dir must be given.
func StorePath(dir, cwd string) (string, error) {
	if dir == "" {
		return "", errors.New("memory store directory is required")
	}
	if !filepath.IsAbs(dir) {
		return "", errors.New("memory store directory must be an absolute path")
	}
	root, err := ProjectRoot(cwd)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, ProjectKey(root)+".json"), nil
}
