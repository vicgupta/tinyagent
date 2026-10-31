package tools

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/skills"
)

// DefaultToolOutputDir is where saved tool output goes, relative to --cwd.
const DefaultToolOutputDir = ".tinyagent/tool-output"

// runIDPattern matches the run directories OutputFiles creates. Pruning
// removes only directories with names like this.
var runIDPattern = regexp.MustCompile(`^\d{8}T\d{6}\.\d{3}Z-[0-9a-f]{6}$`)

// OutputFiles saves the full text of tool results that exceed the
// output limit, one file per tool call, in a per-run directory under Dir.
// The model sees a bounded preview plus a notice naming the file, which it
// can page with read_file (offset/limit) or search with grep.
type OutputFiles struct {
	Root  string // absolute working root (--cwd)
	Dir   string // absolute output directory, inside Root
	RunID string
	// Warn, when set, reports the first save failure of the run.
	Warn func(string)

	mu     sync.Mutex
	warned bool
	used   map[string]bool
}

// ResolveOutputDir checks that dir (relative to root, or absolute) is a
// directory strictly inside root and returns its absolute path. An empty
// dir means DefaultToolOutputDir.
func ResolveOutputDir(root, dir string) (string, error) {
	if dir == "" {
		dir = DefaultToolOutputDir
	}
	full, err := Resolve(root, dir)
	if err != nil {
		return "", fmt.Errorf("tool_output_files.dir %q must be inside --cwd %s", dir, root)
	}
	if full == filepath.Clean(root) {
		return "", fmt.Errorf("tool_output_files.dir %q must be a subdirectory of --cwd, not --cwd itself", dir)
	}
	return full, nil
}

// NewOutputFiles validates dir and returns a store with a fresh run id. No
// directory is created until the first result is saved.
func NewOutputFiles(root, dir string, now time.Time) (*OutputFiles, error) {
	full, err := ResolveOutputDir(root, dir)
	if err != nil {
		return nil, err
	}
	var b [3]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, err
	}
	id := now.UTC().Format("20060102T150405.000Z") + "-" + hex.EncodeToString(b[:])
	return &OutputFiles{Root: root, Dir: full, RunID: id, used: map[string]bool{}}, nil
}

// Prune deletes the oldest earlier run directories so at most maxRuns
// remain (maxRuns 0 keeps all). It runs at startup, before the current run
// saves anything, and only touches directories named like run ids; other
// files are left alone.
func (o *OutputFiles) Prune(maxRuns int) error {
	if maxRuns <= 0 {
		return nil
	}
	// No directory yet (or something else in its place) means nothing to
	// prune; a save that then fails reports it.
	if fi, err := os.Stat(o.Dir); err != nil || !fi.IsDir() {
		return nil
	}
	entries, err := os.ReadDir(o.Dir)
	if err != nil {
		return err
	}
	var runs []string
	for _, e := range entries {
		if e.IsDir() && runIDPattern.MatchString(e.Name()) && e.Name() != o.RunID {
			runs = append(runs, e.Name())
		}
	}
	sort.Strings(runs)
	var errs []error
	for len(runs) > maxRuns {
		if err := os.RemoveAll(filepath.Join(o.Dir, runs[0])); err != nil {
			errs = append(errs, err)
		}
		runs = runs[1:]
	}
	return errors.Join(errs...)
}

// RunDir is the absolute directory holding this run's files.
func (o *OutputFiles) RunDir() string { return filepath.Join(o.Dir, o.RunID) }

// Contains reports whether the absolute path p is the output directory or
// inside it.
func (o *OutputFiles) Contains(p string) bool {
	r, err := filepath.Rel(o.Dir, p)
	return err == nil && r != ".." && !strings.HasPrefix(r, ".."+string(filepath.Separator))
}

// save writes text atomically to a new file named after the tool call id
// and returns its path relative to Root (slash-separated).
func (o *OutputFiles) save(callID, text string) (string, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	dir := o.RunDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	base := sanitizeCallID(callID)
	name := base + ".txt"
	for i := 2; o.used[name] || exists(filepath.Join(dir, name)); i++ {
		name = base + "-" + strconv.Itoa(i) + ".txt"
	}
	f, err := os.CreateTemp(dir, "."+base+".*.tmp")
	if err != nil {
		return "", err
	}
	_, werr := f.WriteString(text)
	cerr := f.Close()
	if err := errors.Join(werr, cerr); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	final := filepath.Join(dir, name)
	if err := os.Rename(f.Name(), final); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	o.used[name] = true
	return rel(o.Root, final), nil
}

// warnOnce reports a save failure the first time one happens in the run.
func (o *OutputFiles) warnOnce(err error) {
	o.mu.Lock()
	first := !o.warned
	o.warned = true
	o.mu.Unlock()
	if first && o.Warn != nil {
		o.Warn(fmt.Sprintf("tool_output_files: could not save full tool output (%v); over-limit results are truncated without a saved copy (later failures in this run are not reported)", err))
	}
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func sanitizeCallID(id string) string {
	var b strings.Builder
	for _, r := range id {
		if b.Len() >= 64 {
			break
		}
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "call"
	}
	return b.String()
}

// groupDigits formats n with comma thousands separators.
func groupDigits(n int) string {
	s := strconv.Itoa(n)
	if n < 0 {
		return "-" + groupDigits(-n)
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

// savedNotice is the line appended to a preview whose full text was saved.
func savedNotice(path string, text string) string {
	return fmt.Sprintf("[full output: %s bytes, %s lines, saved to %s; use read_file with offset/limit or grep on that path]",
		groupDigits(len(text)), groupDigits(countLines(text)), path)
}

// readFileNotice is the line appended to a truncated read_file result.
func readFileNotice(path string, lines int) string {
	return fmt.Sprintf("[file has %s lines; output was truncated, use read_file on %s with offset/limit to read the rest]", groupDigits(lines), path)
}

// TruncateWithNotice cuts body so body, a newline, and notice together fit
// in max bytes, keeping notice whole at the end. It returns ok false when
// notice itself does not fit; callers then fall back to Truncate.
func TruncateWithNotice(body, notice string, max int) (string, bool) {
	room := max - len(notice) - 1
	if room < 0 {
		return "", false
	}
	if room == 0 {
		return notice, len(notice) <= max
	}
	b := truncateRunes(body, room)
	if b == "" {
		return notice, true
	}
	return b + "\n" + notice, true
}

// unredactedTools are the built-in file tools redact_secrets leaves alone:
// their output feeds edits, and a placeholder written back would corrupt
// files (file_access is the control for secret files).
var unredactedTools = map[string]bool{
	"read_file": true, "write_file": true, "edit_file": true, UndoToolName: true, "list_files": true, "grep": true,
	ApplyPatchToolName: true, ViewImageToolName: true, LSPToolName: true, skills.ToolName: true,
}

// Redacts reports whether redact_secrets masks name's results: every tool
// (bash, the process tools, web_fetch, MCP tools) except the built-in file
// tools.
func Redacts(name string) bool { return !unredactedTools[name] }

// limit bounds out for the model. Without OutputFiles (or within the limit)
// it is exactly truncate. Over the limit it saves the full text and returns
// a preview plus a notice; read_file results and searches of the output
// directory itself are never saved.
func (e *Executor) limit(call model.ToolCall, out string, isErr bool) Result {
	var redactions int
	if e.Redactor != nil && Redacts(call.Function.Name) {
		out, redactions = e.Redactor.RedactCount(out)
	}
	res := Result{Text: out, IsError: isErr, Redactions: redactions}
	max := e.MaxOutputChars
	o := e.OutputFiles
	if o == nil || max <= 0 || len(out) <= max {
		res.Text = truncate(out, max)
		return res
	}
	var a args
	if strings.TrimSpace(call.Function.Arguments) != "" {
		json.Unmarshal([]byte(call.Function.Arguments), &a)
	}
	switch call.Function.Name {
	case "read_file":
		res.Text = truncate(out, max)
		if isErr {
			return res
		}
		full, err := e.scope().resolve(e.Root, a.Path)
		if err != nil {
			return res
		}
		data, err := os.ReadFile(full)
		if err != nil {
			return res
		}
		notice := readFileNotice(rel(e.Root, full), countLines(string(data)))
		if text, ok := TruncateWithNotice(out, notice, max); ok {
			res.Text, res.Notice = text, notice
		}
		return res
	case "grep", "list_files":
		if full, err := e.scope().resolve(e.Root, a.Path); err == nil && o.Contains(full) {
			res.Text = truncate(out, max)
			return res
		}
	}
	path, err := o.save(call.ID, out)
	if err != nil {
		o.warnOnce(err)
		res.Text = truncate(out, max)
		return res
	}
	notice := savedNotice(path, out)
	text, ok := TruncateWithNotice(out, notice, max)
	if !ok {
		os.Remove(filepath.Join(e.Root, filepath.FromSlash(path)))
		res.Text = truncate(out, max)
		return res
	}
	res.Text, res.Notice, res.OutputFile, res.OutputBytes = text, notice, path, len(out)
	return res
}
