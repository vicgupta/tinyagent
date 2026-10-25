package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vicgupta/tinyagent/internal/model"
	"github.com/vicgupta/tinyagent/internal/tools"
	"github.com/vicgupta/tinyagent/internal/trajectory"
)

// maxScopedAgentsMDTotal caps the distinct scoped AGENTS.md content bytes
// one run delivers. A file re-delivered after compaction is not counted
// again. It is fixed, not configurable.
const maxScopedAgentsMDTotal = 64 << 10

// scopedSkip reasons for instructions.skipped events.
const (
	scopedSkipCap        = "cap"
	scopedSkipFileAccess = "file_access"
	scopedSkipReadError  = "read_error"
	scopedSkipOutside    = "outside_root"
	scopedSkipModified   = "modified"
)

// deliverScoped appends the scoped AGENTS.md files for the paths a
// successful built-in file tool call touched to res (see --scoped-agents-md).
// The blocks also go to res.Instructions, so feedback re-truncation keeps
// them whole, and to the saved output file when there is one.
func (a *Agent) deliverScoped(t *trajectory.Trajectory, call model.ToolCall, res *tools.Result) {
	if !a.scopedOn() || res.IsError || len(res.Paths) == 0 || t == nil {
		return
	}
	if writeTools[call.Function.Name] {
		for _, p := range res.Paths {
			if filepath.Base(p) == "AGENTS.md" {
				if a.scopedWritten == nil {
					a.scopedWritten = map[string]bool{}
				}
				a.scopedWritten[filepath.Clean(p)] = true
			}
		}
	}
	root := filepath.Clean(a.Tools.Root)
	realRoot := root
	if r, err := filepath.EvalSymlinks(root); err == nil {
		realRoot = r
	}
	since := scopedSince(t)
	var blocks, notices []string
	seen := map[string]bool{}
	for _, dir := range scopedDirs(root, res.Paths) {
		file := filepath.Join(dir, "AGENTS.md")
		if seen[file] {
			continue
		}
		seen[file] = true
		rel := filepath.ToSlash(relTo(root, file))
		scope := filepath.ToSlash(relTo(root, dir)) + "/"
		fi, err := os.Lstat(file)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			a.scopedSkipped(rel, scope, scopedSkipReadError, call, t, err)
			continue
		}
		if a.scopedInContext(t, rel) {
			continue
		}
		real, err := filepath.EvalSymlinks(file)
		if err != nil {
			a.scopedSkipped(rel, scope, scopedSkipReadError, call, t, err)
			continue
		}
		if !inside(realRoot, real) {
			a.scopedSkipped(rel, scope, scopedSkipOutside, call, t, nil)
			continue
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			if fi, err = os.Stat(file); err != nil {
				a.scopedSkipped(rel, scope, scopedSkipReadError, call, t, err)
				continue
			}
		}
		if !fi.Mode().IsRegular() {
			continue
		}
		if err := a.Tools.FileAccess.CheckRead(file, rel); err != nil {
			a.scopedSkipped(rel, scope, scopedSkipFileAccess, call, t, nil)
			continue
		}
		if a.scopedWritten[file] || (!since.IsZero() && fi.ModTime().After(since)) {
			a.scopedSkipped(rel, scope, scopedSkipModified, call, t, nil)
			continue
		}
		b, err := os.ReadFile(file)
		switch {
		case err != nil:
		case len(b) == 0:
			err = errors.New("file is empty")
		case !utf8.Valid(b):
			err = errors.New("file is not valid UTF-8")
		}
		if err != nil {
			a.scopedSkipped(rel, scope, scopedSkipReadError, call, t, err)
			continue
		}
		content, truncated := capAgentsMD(string(b))
		if !scopedCounted(t, rel) && scopedTotal(t)+len(content) > maxScopedAgentsMDTotal {
			if a.scopedSkipped(rel, scope, scopedSkipCap, call, t, nil) {
				notices = append(notices, fmt.Sprintf("[scoped AGENTS.md %s not delivered: the per-run limit of %s bytes of scoped instructions is reached]", rel, formatInt(maxScopedAgentsMDTotal)))
			}
			continue
		}
		blocks = append(blocks, fmt.Sprintf("[AGENTS.md for %s (%s bytes): instructions for files under %s; they take precedence over broader project instructions there]\n%s\n[end AGENTS.md for %s]",
			scope, formatInt(len(b)), scope, strings.TrimRight(content, "\n"), scope))
		load := trajectory.ScopedAgentsMDLoad{
			Path: rel, ScopeDir: scope, Bytes: len(b), Truncated: truncated, Delivered: len(content),
			Tool: call.Function.Name, ToolCallID: call.ID, Step: t.Steps, Message: len(t.Messages),
		}
		t.ScopedAgentsMDLoads = append(t.ScopedAgentsMDLoads, load)
		if a.Verbose && a.Out != nil {
			fmt.Fprintf(a.Out, "loaded scoped AGENTS.md from %s (%d bytes) via %s\n", rel, len(b), call.Function.Name)
		}
		a.event("instructions.loaded", map[string]any{
			"path": rel, "scope_dir": scope, "bytes": len(b), "truncated": truncated,
			"tool": call.Function.Name, "tool_call_id": call.ID, "step": t.Steps,
		})
	}
	if len(blocks)+len(notices) == 0 {
		return
	}
	suffix := "\n\n" + strings.Join(append(blocks, notices...), "\n\n")
	res.Text += suffix
	res.Instructions += suffix
	if res.OutputFile != "" {
		path := filepath.Join(root, filepath.FromSlash(res.OutputFile))
		if f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0); err == nil {
			if n, err := f.WriteString(suffix); err == nil {
				res.OutputBytes += n
			}
			f.Close()
		}
	}
}

// scopedOn reports whether scoped AGENTS.md delivery is on for this run.
func (a *Agent) scopedOn() bool { return a.ScopedAgentsMD && !a.SkipAgentsMD }

// writeTools are the built-in tools that write the files they touch.
var writeTools = map[string]bool{"write_file": true, "edit_file": true, tools.ApplyPatchToolName: true}

// scopedSkipped reports a scoped AGENTS.md that was not delivered, once per
// file and reason per invocation, and reports whether this was the first
// report. Read errors warn; cap skips warn; the rest are -v lines.
func (a *Agent) scopedSkipped(rel, scope, reason string, call model.ToolCall, t *trajectory.Trajectory, err error) bool {
	key := reason + "\x00" + rel
	if a.scopedReported[key] {
		return false
	}
	if a.scopedReported == nil {
		a.scopedReported = map[string]bool{}
	}
	a.scopedReported[key] = true
	var msg string
	switch reason {
	case scopedSkipReadError:
		msg = fmt.Sprintf("reading AGENTS.md: %s: %v", rel, err)
	case scopedSkipCap:
		msg = fmt.Sprintf("scoped AGENTS.md %s not delivered: the per-run limit of %d bytes of scoped instructions is reached", rel, maxScopedAgentsMDTotal)
	}
	if msg != "" {
		if a.Err != nil {
			fmt.Fprintln(a.Err, "warning:", msg)
		}
		a.event("warning", map[string]any{"message": msg})
	} else if a.Verbose && a.Out != nil {
		fmt.Fprintf(a.Out, "skipped scoped AGENTS.md %s (%s)\n", rel, reason)
	}
	fields := map[string]any{"path": rel, "scope_dir": scope, "reason": reason, "tool": call.Function.Name, "tool_call_id": call.ID, "step": t.Steps}
	if err != nil {
		fields["error"] = err.Error()
	}
	a.event("instructions.skipped", fields)
	return true
}

// scopedDirs returns, in order and without repeats, the directories strictly
// below root from root down to each path's directory (a directory path
// counts as its own directory). Paths outside root are ignored.
func scopedDirs(root string, paths []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range paths {
		p = filepath.Clean(p)
		if !inside(root, p) {
			continue
		}
		dir := p
		if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
			dir = filepath.Dir(p)
		}
		rel := relTo(root, dir)
		if rel == "." || rel == "" {
			continue
		}
		cur := root
		for _, part := range strings.Split(rel, string(filepath.Separator)) {
			cur = filepath.Join(cur, part)
			if !seen[cur] {
				seen[cur] = true
				out = append(out, cur)
			}
		}
	}
	return out
}

// inside reports whether p is root or below it (lexically).
func inside(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func relTo(root, p string) string {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return p
	}
	return rel
}

// capAgentsMD cuts s to maxAgentsMD bytes on a rune boundary.
func capAgentsMD(s string) (string, bool) {
	if len(s) <= maxAgentsMD {
		return s, false
	}
	n := maxAgentsMD
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "\n[AGENTS.md truncated]", true
}

func scopedSince(t *trajectory.Trajectory) time.Time {
	since, _ := time.Parse(time.RFC3339Nano, t.ScopedAgentsMDSince)
	return since
}

// scopedCounted reports whether rel was delivered earlier in the run, so a
// re-delivery does not count toward the per-run cap again.
func scopedCounted(t *trajectory.Trajectory, rel string) bool {
	for _, l := range t.ScopedAgentsMDLoads {
		if l.Path == rel {
			return true
		}
	}
	return false
}

// scopedTotal is the distinct scoped content bytes the run delivered.
func scopedTotal(t *trajectory.Trajectory) int {
	n := 0
	seen := map[string]bool{}
	for _, l := range t.ScopedAgentsMDLoads {
		if !seen[l.Path] {
			seen[l.Path] = true
			n += l.Delivered
		}
	}
	return n
}

// scopedInContext reports whether rel's latest delivery is still in the
// model's context: its tool result is in the view and not elided. A
// delivery whose result is not recorded yet counts as in context.
func (a *Agent) scopedInContext(t *trajectory.Trajectory, rel string) bool {
	for _, p := range a.scopedDelivered(t, a.view) {
		if p == rel {
			return true
		}
	}
	return false
}

// scopedDelivered returns the scoped AGENTS.md paths whose delivering tool
// result is in v and not elided, in delivery order.
func (a *Agent) scopedDelivered(t *trajectory.Trajectory, v *view) []string {
	return scopedDeliveredIn(t, v)
}

func scopedDeliveredIn(t *trajectory.Trajectory, v *view) []string {
	if len(t.ScopedAgentsMDLoads) == 0 {
		return nil
	}
	live := map[int]bool{}
	if v != nil {
		for i, ix := range v.idx {
			if ix >= 0 && !strings.HasPrefix(v.msgs[i].Content, elidedPrefix) {
				live[ix] = true
			}
		}
	}
	var out []string
	seen := map[string]bool{}
	for _, l := range t.ScopedAgentsMDLoads {
		if seen[l.Path] {
			continue
		}
		if live[l.Message] || l.Message >= len(t.Messages) {
			seen[l.Path] = true
			out = append(out, l.Path)
		}
	}
	return out
}

// ScopedAgentsMDDelivered returns the scoped AGENTS.md paths of checkpoint
// t whose delivering results are still in context, for run.started; nil
// when there are none.
func ScopedAgentsMDDelivered(t *trajectory.Trajectory) []string {
	if t == nil {
		return nil
	}
	return scopedDeliveredIn(t, viewFromTrajectory(t))
}
