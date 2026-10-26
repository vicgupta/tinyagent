package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/vicgupta/tinyagent/embed"
	"github.com/vicgupta/tinyagent/internal/tools"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// reviewMaxDiffBytes caps the diff text in a review run's first user
// message. Tests lower it.
var reviewMaxDiffBytes = 200_000

// Review targets, as recorded in trajectory.ReviewInfo.Target.
const (
	reviewUncommitted = "uncommitted"
	reviewBase        = "base"
	reviewCommit      = "commit"
)

// reviewTarget is the change set selected by --review, --review-base or
// --review-commit.
type reviewTarget struct {
	kind, ref string
}

// flagName is the command-line flag that selected t, for error messages.
func (t *reviewTarget) flagName() string {
	switch t.kind {
	case reviewBase:
		return "--review-base"
	case reviewCommit:
		return "--review-commit"
	}
	return "--review"
}

// errNothingToReview reports an empty change set: no model request is made.
var errNothingToReview = errors.New("nothing to review")

// reviewSelection returns the review target selected on the command line
// (nil without a review flag), or a usage error (exit 2) for two targets,
// an unusable ref, or an option review mode can't be combined with.
func reviewSelection(fs *flag.FlagSet, o *options) (*reviewTarget, error) {
	var set []string
	var t *reviewTarget
	if flagWasSet(fs, "review") && o.review {
		set, t = append(set, "--review"), &reviewTarget{kind: reviewUncommitted}
	}
	if flagWasSet(fs, "review-base") {
		set, t = append(set, "--review-base"), &reviewTarget{kind: reviewBase, ref: o.reviewBase}
	}
	if flagWasSet(fs, "review-commit") {
		set, t = append(set, "--review-commit"), &reviewTarget{kind: reviewCommit, ref: o.reviewCommit}
	}
	if len(set) > 1 {
		return nil, fmt.Errorf("%s and %s can't be combined; choose one review target", set[0], set[1])
	}
	if t == nil {
		return nil, nil
	}
	if t.kind != reviewUncommitted {
		if t.ref == "" {
			return nil, fmt.Errorf("%s needs a Git ref", t.flagName())
		}
		// A ref is passed to git as an argument; one starting with "-"
		// would be read as an option.
		if strings.HasPrefix(t.ref, "-") {
			return nil, fmt.Errorf("%s %q: a ref can't start with -", t.flagName(), t.ref)
		}
	}
	conflicts := []struct {
		on   bool
		name string
	}{
		{flagWasSet(fs, "read-only") && !o.readOnly, "--read-only=false"},
		{o.resume != "", "--resume"},
		{o.rewind != "", "--rewind"},
		{o.worktree.on, "--worktree"},
		{flagWasSet(fs, "snapshots") && o.snapshots, "--snapshots"},
		{o.check, "--check"},
		{o.initConfig, "--init-config"},
		{o.doUpdate, "--update"},
	}
	for _, c := range conflicts {
		if c.on {
			return nil, fmt.Errorf("%s can't be combined with %s: a review run is read-only and starts fresh", t.flagName(), c.name)
		}
	}
	return t, nil
}

// reviewFile is one changed file in a review.
type reviewFile struct {
	path     string
	status   string // A, M or D
	add, del int
	binary   bool
	diff     string // the file's whole diff; "" when not shown
}

// reviewGit runs one of tinyagent's own read-only git commands for a
// review in dir. Repository config can't start programs: external diff
// and textconv drivers are off (the callers pass --no-ext-diff and
// --no-textconv), and overrides turns off core.fsmonitor and every
// configured filter driver. GIT_OPTIONAL_LOCKS=0 keeps git from
// refreshing the index on disk. Exit status 1 is accepted when ok1 is set
// (git diff --no-index reports differences that way).
func reviewGit(dir string, overrides []string, ok1 bool, args ...string) (string, error) {
	full := append([]string{"-C", dir}, overrides...)
	cmd := exec.Command("git", append(full, args...)...)
	cmd.Env = append(gitEnv(), "GIT_OPTIONAL_LOCKS=0")
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !ok1 || !errors.As(err, &exit) || exit.ExitCode() != 1 {
			msg := strings.TrimSpace(errOut.String())
			if msg == "" {
				msg = err.Error()
			}
			return "", errors.New(msg)
		}
	}
	return out.String(), nil
}

// reviewOverrides returns the -c options that keep repository config from
// starting programs while a review collects its diff.
func reviewOverrides(dir string) ([]string, error) {
	ov := []string{"-c", "core.fsmonitor=false"}
	names, err := reviewGit(dir, ov, true, "config", "-z", "--name-only", "--get-regexp", `^filter\..*\.(clean|smudge|process)$`)
	if err != nil {
		return nil, fmt.Errorf("reading the repository's filter config: %w", err)
	}
	for _, name := range strings.Split(names, "\x00") {
		if name == "" {
			continue
		}
		if strings.Contains(name, "=") {
			return nil, fmt.Errorf("the repository config sets a filter driver that can't be turned off (%s)", name)
		}
		ov = append(ov, "-c", name+"=")
	}
	return ov, nil
}

// collectReview resolves the review target in the repository containing
// root, collects its diff with read-only git commands, and returns the
// first user message (review instructions, target, change summary, diff,
// then focus as additional instructions) and its record. Files under root
// only are reviewed, with paths relative to root; files that
// file_access.deny_read hides are left out and only counted. An empty
// change set returns errNothingToReview; every other error is a setup
// problem (exit 2).
func collectReview(t *reviewTarget, root, focus string, fa *tools.FileAccess, stderr io.Writer, verbose bool) (string, *trajectory.ReviewInfo, error) {
	flagName := t.flagName()
	if _, err := exec.LookPath("git"); err != nil {
		return "", nil, fmt.Errorf("%s requires git on PATH", flagName)
	}
	top, err := git("-C", root, "rev-parse", "--show-toplevel")
	if err != nil || top == "" {
		return "", nil, fmt.Errorf("%s: --cwd %s is not inside a Git work tree", flagName, root)
	}
	ov, err := reviewOverrides(root)
	if err != nil {
		return "", nil, fmt.Errorf("%s: %w", flagName, err)
	}
	g := func(ok1 bool, args ...string) (string, error) { return reviewGit(root, ov, ok1, args...) }
	resolve := func(rev string) (string, error) {
		out, err := g(false, "rev-parse", "--verify", "--quiet", rev+"^{commit}")
		return strings.TrimSpace(out), err
	}
	// Diff options shared by every target: Git's built-in diff only, no
	// rename detection (one entry per path), paths relative to root.
	diffArgs := []string{"diff", "--no-ext-diff", "--no-textconv", "--no-color", "--no-renames", "--relative"}

	info := &trajectory.ReviewInfo{Target: t.kind, Ref: t.ref}
	var from, to, header string
	switch t.kind {
	case reviewUncommitted:
		head, err := resolve("HEAD")
		if err != nil || head == "" {
			return "", nil, fmt.Errorf("--review: the repository at %s has no commits", top)
		}
		info.HeadCommit, from = head, head
		header = fmt.Sprintf("Review target: uncommitted changes (staged, unstaged and untracked) against HEAD %s.", shortCommit(head))
	case reviewBase:
		base, err := resolve(t.ref)
		if err != nil || base == "" {
			return "", nil, fmt.Errorf("--review-base %q: not a commit in the repository at %s", t.ref, top)
		}
		head, err := resolve("HEAD")
		if err != nil || head == "" {
			return "", nil, fmt.Errorf("--review-base %q: the repository at %s has no commits", t.ref, top)
		}
		mb, err := g(false, "merge-base", base, head)
		if mb = strings.TrimSpace(mb); err != nil || mb == "" {
			return "", nil, fmt.Errorf("--review-base %q: no merge base with HEAD in the repository at %s", t.ref, top)
		}
		info.BaseCommit, info.HeadCommit, from, to = mb, head, mb, head
		header = fmt.Sprintf("Review target: the commits on HEAD %s since its merge base %s with %s (uncommitted changes are not included).", shortCommit(head), shortCommit(mb), t.ref)
	case reviewCommit:
		c, err := resolve(t.ref)
		if err != nil || c == "" {
			return "", nil, fmt.Errorf("--review-commit %q: not a commit in the repository at %s", t.ref, top)
		}
		parent, err := resolve(c + "^1")
		if err != nil || parent == "" {
			// A root commit is compared with the empty tree.
			out, err := g(false, "hash-object", "-t", "tree", "--stdin")
			if parent = strings.TrimSpace(out); err != nil || parent == "" {
				return "", nil, fmt.Errorf("--review-commit %q: %v", t.ref, err)
			}
		} else {
			info.BaseCommit = parent
		}
		meta, err := g(false, "show", "-s", "--no-color", "--format=%s%n%an <%ae>", c)
		if err != nil {
			return "", nil, fmt.Errorf("--review-commit %q: %w", t.ref, err)
		}
		subject, author, _ := strings.Cut(strings.TrimSpace(meta), "\n")
		info.Commit, from, to = c, parent, c
		header = fmt.Sprintf("Review target: the changes introduced by commit %s (against its first parent).\nCommit subject: %s\nAuthor: %s", c, subject, author)
	}
	if rel, err := filepath.Rel(top, root); err == nil && rel != "." {
		header += fmt.Sprintf("\nOnly changes under %s are included.", filepath.ToSlash(rel))
	}
	header += "\nPaths are relative to the working directory."

	revs := []string{from}
	if to != "" {
		revs = append(revs, to)
	}
	numstat, err := g(false, append(append(append([]string(nil), diffArgs...), "--numstat", "-z"), revs...)...)
	if err != nil {
		return "", nil, fmt.Errorf("%s: git diff: %w", flagName, err)
	}
	patch, err := g(false, append(append([]string(nil), diffArgs...), revs...)...)
	if err != nil {
		return "", nil, fmt.Errorf("%s: git diff: %w", flagName, err)
	}
	files, err := parseReviewDiff(numstat, patch)
	if err != nil {
		return "", nil, fmt.Errorf("%s: %w", flagName, err)
	}
	if t.kind == reviewUncommitted {
		out, err := g(false, "ls-files", "--others", "--exclude-standard", "-z")
		if err != nil {
			return "", nil, fmt.Errorf("--review: git ls-files: %w", err)
		}
		for _, p := range strings.Split(out, "\x00") {
			if p == "" {
				continue
			}
			f := reviewFile{path: p, status: "A"}
			// A nested repository is listed as a directory; it gets a
			// summary line only.
			if !strings.HasSuffix(p, "/") && !fa.Hidden(filepath.Join(root, p)) {
				d, err := g(true, "diff", "--no-index", "--no-ext-diff", "--no-textconv", "--no-color", "--", os.DevNull, p)
				if err != nil {
					return "", nil, fmt.Errorf("--review: git diff %s: %w", p, err)
				}
				f.diff = d
				f.add, f.binary = countAdded(d)
			}
			files = append(files, f)
		}
	}

	var shown []reviewFile
	for _, f := range files {
		if fa.Hidden(filepath.Join(root, filepath.FromSlash(strings.TrimSuffix(f.path, "/")))) {
			info.HiddenPaths++
			continue
		}
		shown = append(shown, f)
		info.DiffBytes += len(f.diff)
	}
	info.FilesChanged = len(shown)
	if len(files) == 0 {
		return "", nil, errNothingToReview
	}

	var sb, body strings.Builder
	var omitted []string
	for _, f := range shown {
		if f.diff == "" {
			continue
		}
		if body.Len()+len(f.diff) > reviewMaxDiffBytes {
			omitted = append(omitted, f.path)
			continue
		}
		body.WriteString(f.diff)
	}
	info.DiffBytesSent, info.Truncated = body.Len(), len(omitted) > 0

	sb.WriteString(strings.TrimSpace(embed.ReviewPrompt()))
	sb.WriteString("\n\n" + header + "\n\n")
	fmt.Fprintf(&sb, "Changed files (%d):\n", len(shown))
	for _, f := range shown {
		counts := fmt.Sprintf("+%d -%d", f.add, f.del)
		switch {
		case f.binary:
			counts = "binary file changed"
		case strings.HasSuffix(f.path, "/"):
			counts = "nested repository, not shown"
		}
		fmt.Fprintf(&sb, "%s %s (%s)\n", f.status, f.path, counts)
	}
	if info.HiddenPaths > 0 {
		fmt.Fprintf(&sb, "\n%d changed %s hidden by file_access.deny_read; %s not shown.\n", info.HiddenPaths, plural(info.HiddenPaths, "path is", "paths are"), plural(info.HiddenPaths, "its changes are", "their changes are"))
	}
	if info.Truncated {
		fmt.Fprintf(&sb, "\nThe diff is too large to include in full: %d of %d bytes are shown. The diffs of these files were left out; read them with read_file:\n", info.DiffBytesSent, info.DiffBytes)
		for _, p := range omitted {
			sb.WriteString("- " + p + "\n")
		}
	}
	sb.WriteString("\n--- BEGIN DIFF ---\n")
	sb.WriteString(body.String())
	if body.Len() > 0 && !strings.HasSuffix(body.String(), "\n") {
		sb.WriteString("\n")
	}
	sb.WriteString("--- END DIFF ---\n")
	if focus != "" {
		sb.WriteString("\nAdditional review instructions:\n" + focus + "\n")
	}

	if info.HiddenPaths > 0 {
		fmt.Fprintf(stderr, "review: %d changed %s hidden by file_access.deny_read\n", info.HiddenPaths, plural(info.HiddenPaths, "path", "paths"))
	}
	if info.Truncated {
		fmt.Fprintf(stderr, "review: diff truncated to %d of %d bytes; %d %s left out\n", info.DiffBytesSent, info.DiffBytes, len(omitted), plural(len(omitted), "file", "files"))
	}
	if t.kind == reviewBase {
		if st, err := g(false, "status", "--porcelain", "--untracked-files=normal", "--", "."); err == nil && strings.TrimSpace(st) != "" {
			fmt.Fprintln(stderr, "note: uncommitted changes are not part of --review-base; use --review to review them")
		}
	}
	if verbose {
		fmt.Fprintln(stderr, reviewLine(info))
	}
	return sb.String(), info, nil
}

// reviewLine is the -v summary of a review's change set.
func reviewLine(info *trajectory.ReviewInfo) string {
	s := "review: target=" + info.Target
	if info.Ref != "" {
		s += " ref=" + info.Ref
	}
	for _, c := range [][2]string{{"base", info.BaseCommit}, {"head", info.HeadCommit}, {"commit", info.Commit}} {
		if c[1] != "" {
			s += " " + c[0] + "=" + shortCommit(c[1])
		}
	}
	return s + fmt.Sprintf(" files=%d diff_bytes=%d sent=%d truncated=%t hidden=%d", info.FilesChanged, info.DiffBytes, info.DiffBytesSent, info.Truncated, info.HiddenPaths)
}

// parseReviewDiff pairs the records of git diff --numstat -z with the
// per-file sections of the same diff's patch: with rename detection off
// both list the same paths in the same order.
func parseReviewDiff(numstat, patch string) ([]reviewFile, error) {
	var files []reviewFile
	for _, rec := range strings.Split(numstat, "\x00") {
		if rec == "" {
			continue
		}
		parts := strings.SplitN(rec, "\t", 3)
		if len(parts) != 3 {
			return nil, fmt.Errorf("unexpected git diff --numstat output %q", rec)
		}
		f := reviewFile{path: parts[2], status: "M"}
		if parts[0] == "-" && parts[1] == "-" {
			f.binary = true
		} else {
			f.add, _ = strconv.Atoi(parts[0])
			f.del, _ = strconv.Atoi(parts[1])
		}
		files = append(files, f)
	}
	var sections []string
	for _, line := range strings.SplitAfter(patch, "\n") {
		if strings.HasPrefix(line, "diff --git ") || len(sections) == 0 {
			sections = append(sections, "")
		}
		sections[len(sections)-1] += line
	}
	if patch == "" {
		sections = nil
	}
	if len(sections) != len(files) {
		return nil, fmt.Errorf("git diff listed %d files but its patch has %d sections", len(files), len(sections))
	}
	for i, s := range sections {
		files[i].diff = s
		head, _, _ := strings.Cut(s, "\n@@")
		switch {
		case strings.Contains(head, "\nnew file mode "):
			files[i].status = "A"
		case strings.Contains(head, "\ndeleted file mode "):
			files[i].status = "D"
		}
	}
	return files, nil
}

// countAdded returns the added lines of a new-file diff, or binary when
// git reported a binary file.
func countAdded(diff string) (int, bool) {
	n := 0
	inHunk := false
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "@@"):
			inHunk = true
		case !inHunk && strings.HasPrefix(line, "Binary files "):
			return 0, true
		case inHunk && strings.HasPrefix(line, "+"):
			n++
		}
	}
	return n, false
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
