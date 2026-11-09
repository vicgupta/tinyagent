package gitwt

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// emptyTree is git's well-known empty tree. Root commits are compared to it.
const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// RepoState is the branch tips and index of the repository containing dir,
// taken before TinyAgent runs a tool command. Enforce puts back any ref the
// command moved onto a new commit that records a tinyagent.yaml, and restores
// the index bytes from this snapshot. It does not edit the work tree and does
// not rewrite commit objects. A repository that is not a work tree, or a
// command that does not create such a commit, is left alone.
type RepoState struct {
	dir     string
	ok      bool
	headSym string
	headOID string
	refs    map[string]string
	index   string
	idxData []byte
	hadIdx  bool
}

// Capture records refs and the index for the work tree containing dir.
// ok is false when dir is not inside a work tree; Enforce is then a no-op.
func Capture(dir string) *RepoState {
	s := &RepoState{dir: dir, refs: map[string]string{}}
	top, err := gitOut(dir, "rev-parse", "--show-toplevel")
	if err != nil || strings.TrimSpace(top) == "" {
		return s
	}
	s.dir = strings.TrimSpace(top)
	s.ok = true
	if sym, err := gitOut(s.dir, "symbolic-ref", "-q", "HEAD"); err == nil {
		s.headSym = strings.TrimSpace(sym)
	}
	if oid, err := gitOut(s.dir, "rev-parse", "--verify", "--quiet", "HEAD^{commit}"); err == nil {
		s.headOID = strings.TrimSpace(oid)
	}
	if refs, err := gitOut(s.dir, "for-each-ref", "--format=%(refname) %(objectname)"); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(refs), "\n") {
			name, oid, ok := strings.Cut(line, " ")
			if ok && name != "" && oid != "" {
				s.refs[name] = oid
			}
		}
	}
	if p, err := gitOut(s.dir, "rev-parse", "--git-path", "index"); err == nil {
		p = strings.TrimSpace(p)
		if p != "" && !filepath.IsAbs(p) {
			p = filepath.Join(s.dir, filepath.FromSlash(p))
		}
		s.index = p
		if b, err := os.ReadFile(p); err == nil {
			s.idxData = b
			s.hadIdx = true
		}
	}
	return s
}

// Enforce rolls back refs that gained a commit recording tinyagent.yaml and,
// when it does, writes the captured index back. The work tree is not modified.
// The returned note is empty when nothing was rolled back.
func (s *RepoState) Enforce() (string, error) {
	if s == nil || !s.ok {
		return "", nil
	}
	after := Capture(s.dir)
	if !after.ok {
		return "", nil
	}
	var bad []string
	for name, newOID := range after.refs {
		oldOID := s.refs[name]
		if oldOID == newOID {
			continue
		}
		hit, err := recordsConfig(s.dir, s.headOID, newOID, s.refs)
		if err != nil {
			return "", err
		}
		if hit {
			bad = append(bad, name)
		}
	}
	if len(bad) == 0 {
		return "", nil
	}
	if after.headSym != s.headSym && s.headSym != "" {
		for _, name := range bad {
			if after.headSym == name {
				if _, err := gitOut(s.dir, "symbolic-ref", "HEAD", s.headSym); err != nil {
					return "", fmt.Errorf("could not point HEAD back at %s: %w", s.headSym, err)
				}
				break
			}
		}
	}
	for _, name := range bad {
		oldOID := s.refs[name]
		newOID := after.refs[name]
		var err error
		if oldOID == "" {
			_, err = gitOut(s.dir, "update-ref", "-d", name, newOID)
		} else {
			_, err = gitOut(s.dir, "update-ref", name, oldOID, newOID)
		}
		if err != nil {
			return "", fmt.Errorf("could not restore %s: %w", name, err)
		}
	}
	if err := s.restoreIndex(); err != nil {
		return "", err
	}
	return fmt.Sprintf("tinyagent: undid commits that recorded tinyagent.yaml (%s). The branch tip and the index were put back to their state before this command; the working copy was not changed. A push already sent by this command is not undone.", strings.Join(bad, ", ")), nil
}

func (s *RepoState) restoreIndex() error {
	if s.index == "" {
		return nil
	}
	if !s.hadIdx {
		if err := os.Remove(s.index); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("could not remove the index created by the command: %w", err)
		}
		return nil
	}
	if err := os.WriteFile(s.index, s.idxData, 0o644); err != nil {
		return fmt.Errorf("could not restore the index: %w", err)
	}
	return nil
}

// recordsConfig reports whether any commit reachable from newOID, and not
// from the tips captured before the command, changes a tinyagent.yaml blob
// relative to the pre-command HEAD. An amend that keeps that blob is not a
// new recording. A ref that only moves to commits that already existed is not.
func recordsConfig(dir, baseline, newOID string, oldRefs map[string]string) (bool, error) {
	if newOID == "" {
		return false, nil
	}
	args := []string{"rev-list", newOID}
	seen := map[string]bool{}
	for _, oid := range oldRefs {
		if oid != "" && !seen[oid] {
			seen[oid] = true
			args = append(args, "--not", oid)
		}
	}
	if baseline != "" && !seen[baseline] {
		args = append(args, "--not", baseline)
	}
	out, err := gitOut(dir, args...)
	if err != nil {
		return false, err
	}
	for _, commit := range strings.Fields(out) {
		hit, err := commitChangesConfig(dir, baseline, commit)
		if err != nil {
			return false, err
		}
		if hit {
			return true, nil
		}
	}
	return false, nil
}

func commitChangesConfig(dir, baseline, commit string) (bool, error) {
	parent, err := gitOut(dir, "rev-parse", "--verify", "--quiet", commit+"^")
	parent = strings.TrimSpace(parent)
	var paths []string
	if err != nil || parent == "" {
		paths, err = diffNames(dir, emptyTree, commit)
	} else {
		paths, err = diffNames(dir, parent, commit)
	}
	if err != nil {
		return false, err
	}
	for _, p := range paths {
		if !isLocalConfigPath(p) {
			continue
		}
		have, hok := blobAt(dir, commit, p)
		base, bok := "", false
		if baseline != "" {
			base, bok = blobAt(dir, baseline, p)
		}
		if hok != bok || have != base {
			return true, nil
		}
	}
	return false, nil
}

func diffNames(dir, from, to string) ([]string, error) {
	out, err := gitOut(dir, "diff", "--name-only", "-z", from, to)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, p := range strings.Split(out, "\x00") {
		if p = strings.TrimSpace(p); p != "" {
			names = append(names, p)
		}
	}
	return names, nil
}

func blobAt(dir, treeish, path string) (string, bool) {
	out, err := gitOut(dir, "rev-parse", "--verify", "--quiet", treeish+":"+path)
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(out), true
}

func gitOut(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = Env()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return stdout.String(), errors.New(msg)
	}
	return stdout.String(), nil
}
