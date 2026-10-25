package tools

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/vicgupta/tinyagent/internal/model"
)

// ApplyPatchToolName is the opt-in multi-file patch tool.
const ApplyPatchToolName = "apply_patch"

// MaxPatchBytes caps one apply_patch input.
const MaxPatchBytes = 1 << 20

// ApplyPatchSpec returns the apply_patch definition.
func ApplyPatchSpec() model.ToolSpec {
	return spec(ApplyPatchToolName, "Apply one patch that can add, update (several hunks), rename, and delete several files. "+
		"The whole patch is checked before anything is written; if any part does not apply, nothing changes. Format:\n"+
		"*** Begin Patch\n"+
		"*** Add File: <path>   (then every line prefixed with +)\n"+
		"*** Delete File: <path>\n"+
		"*** Update File: <path>   (optionally followed by *** Move to: <new path>)\n"+
		"@@ <optional line to search for first, e.g. a function signature; several @@ lines may be stacked>\n"+
		" context line (prefix space)\n-removed line\n+added line\n"+
		"*** End of File   (optional: the hunk is at the end of the file)\n"+
		"*** End Patch\n"+
		"Use relative paths and no line numbers; give about 3 lines of context around each change. Example:\n"+
		"*** Begin Patch\n*** Update File: src/app.go\n@@ func main() {\n \tfmt.Println(\"hi\")\n-\treturn\n+\tos.Exit(0)\n }\n*** End Patch",
		props{"input": "The entire patch, from *** Begin Patch to *** End Patch"}, "input")
}

type patchOp struct {
	kind   string // "add", "delete", or "update"
	path   string
	moveTo string
	lines  []string // add: the new file's lines
	hunks  []hunk
}

type hunk struct {
	anchors  []string
	old, new []string
	// ctx maps each new line that is a context line to its index in old,
	// and every added line to -1, so context keeps the file's own text.
	ctx []int
	eof bool
}

func (o patchOp) label(i int) string {
	return fmt.Sprintf("operation %d (%s %s)", i+1, o.kind, o.path)
}

// parsePatch parses a patch in the "*** Begin Patch" (V4A) format.
func parsePatch(input string) ([]patchOp, error) {
	if len(input) > MaxPatchBytes {
		return nil, fmt.Errorf("patch is %d bytes, over the %d byte limit", len(input), MaxPatchBytes)
	}
	lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(input, "\r\n", "\n")), "\n")
	// Strip the shell heredoc wrapper some models copy from the docs:
	// apply_patch <<"EOF" ... EOF
	if len(lines) > 2 && strings.HasPrefix(strings.TrimSpace(lines[0]), "apply_patch") && strings.Contains(lines[0], "<<") {
		lines = lines[1 : len(lines)-1]
	}
	if strings.TrimSpace(lines[0]) != "*** Begin Patch" {
		return nil, errors.New("the first line must be *** Begin Patch")
	}
	if len(lines) < 2 || strings.TrimSpace(lines[len(lines)-1]) != "*** End Patch" {
		return nil, errors.New("the last line must be *** End Patch")
	}
	lines = lines[1 : len(lines)-1]
	var ops []patchOp
	for i := 0; i < len(lines); {
		line := strings.TrimSpace(lines[i])
		lineErr := func(format string, a ...any) error {
			return fmt.Errorf("line %d: %s", i+2, fmt.Sprintf(format, a...))
		}
		switch {
		case strings.HasPrefix(line, "*** Add File: "):
			op := patchOp{kind: "add", path: strings.TrimSpace(strings.TrimPrefix(line, "*** Add File: "))}
			i++
			for ; i < len(lines) && !strings.HasPrefix(lines[i], "*** "); i++ {
				if !strings.HasPrefix(lines[i], "+") {
					return nil, lineErr("every line of an added file must start with +, got %q", lines[i])
				}
				op.lines = append(op.lines, lines[i][1:])
			}
			ops = append(ops, op)
		case strings.HasPrefix(line, "*** Delete File: "):
			ops = append(ops, patchOp{kind: "delete", path: strings.TrimSpace(strings.TrimPrefix(line, "*** Delete File: "))})
			i++
		case strings.HasPrefix(line, "*** Update File: "):
			op := patchOp{kind: "update", path: strings.TrimSpace(strings.TrimPrefix(line, "*** Update File: "))}
			i++
			if i < len(lines) && strings.HasPrefix(strings.TrimSpace(lines[i]), "*** Move to: ") {
				op.moveTo = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[i]), "*** Move to: "))
				i++
			}
			var h *hunk
			for ; i < len(lines); i++ {
				l := lines[i]
				if strings.TrimSpace(l) == "*** End of File" {
					if h == nil {
						return nil, lineErr("*** End of File outside a hunk")
					}
					h.eof = true
					continue
				}
				if strings.HasPrefix(l, "*** ") {
					break
				}
				if strings.HasPrefix(l, "@@") {
					// A new hunk starts unless the current one has no
					// body yet, in which case the anchors stack.
					if h == nil || len(h.old) > 0 || len(h.new) > 0 {
						op.hunks = append(op.hunks, hunk{})
						h = &op.hunks[len(op.hunks)-1]
					}
					if a := strings.TrimSpace(strings.TrimPrefix(l, "@@")); a != "" {
						h.anchors = append(h.anchors, a)
					}
					continue
				}
				if h == nil {
					// The first hunk may omit its @@ line.
					op.hunks = append(op.hunks, hunk{})
					h = &op.hunks[len(op.hunks)-1]
				}
				if h.eof {
					return nil, lineErr("lines after *** End of File; start a new hunk with @@")
				}
				switch {
				case l == "":
					h.ctx = append(h.ctx, len(h.old))
					h.old, h.new = append(h.old, ""), append(h.new, "")
				case l[0] == ' ':
					h.ctx = append(h.ctx, len(h.old))
					h.old, h.new = append(h.old, l[1:]), append(h.new, l[1:])
				case l[0] == '-':
					h.old = append(h.old, l[1:])
				case l[0] == '+':
					h.ctx = append(h.ctx, -1)
					h.new = append(h.new, l[1:])
				default:
					return nil, lineErr("unknown line prefix in %q; hunk lines start with space, -, or +", l)
				}
			}
			for _, h := range op.hunks {
				if len(h.old) == 0 && len(h.new) == 0 {
					return nil, fmt.Errorf("operation %d (update %s): a hunk has no lines", len(ops)+1, op.path)
				}
			}
			if len(op.hunks) == 0 && op.moveTo == "" {
				return nil, fmt.Errorf("operation %d (update %s): no hunks", len(ops)+1, op.path)
			}
			ops = append(ops, op)
		default:
			return nil, lineErr("expected *** Add File:, *** Delete File:, or *** Update File:, got %q", lines[i])
		}
		if p := ops[len(ops)-1].path; p == "" {
			return nil, fmt.Errorf("operation %d has an empty path", len(ops))
		}
	}
	if len(ops) == 0 {
		return nil, errors.New("the patch has no file operations")
	}
	return ops, nil
}

// fileChange is one checked operation, ready to write.
type fileChange struct {
	op      patchOp
	full    string // resolved path
	dest    string // resolved move destination, or ""
	content []byte // new content for add and update
	mode    os.FileMode
	summary string
}

// matchPasses are the comparisons tried in order, as in OpenAI's reference
// implementation: exact, ignoring trailing whitespace, ignoring surrounding
// whitespace.
var matchPasses = []struct {
	name string
	norm func(string) string
}{
	{"", func(s string) string { return s }},
	{"ignoring trailing whitespace", func(s string) string { return strings.TrimRight(s, " \t\r") }},
	{"ignoring surrounding whitespace", strings.TrimSpace},
}

// seek returns the first index >= start where want matches lines, and the
// loosest pass needed, or -1. With eof it tries the end of the file first.
func seek(lines, want []string, start int, eof bool) (int, int) {
	for p, pass := range matchPasses {
		at := func(i int) bool {
			for j, w := range want {
				if pass.norm(lines[i+j]) != pass.norm(w) {
					return false
				}
			}
			return true
		}
		if eof {
			if i := len(lines) - len(want); i >= start && at(i) {
				return i, p
			}
		}
		for i := start; i+len(want) <= len(lines); i++ {
			if at(i) {
				return i, p
			}
		}
	}
	return -1, 0
}

// applyHunks returns content with every hunk applied in order, and notes for
// hunks that needed a looser match.
func applyHunks(content string, hunks []hunk) (string, []string, error) {
	crlf := strings.Contains(content, "\r\n")
	if crlf {
		content = strings.ReplaceAll(content, "\r\n", "\n")
	}
	trailingNL := strings.HasSuffix(content, "\n")
	var lines []string
	if content != "" {
		lines = strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	}
	if len(lines) == 0 {
		trailingNL = true
	}
	type repl struct {
		at, n int
		new   []string
	}
	var repls []repl
	var notes []string
	pos := 0
	for hi, h := range hunks {
		loosest := 0
		for _, a := range h.anchors {
			i, p := seek(lines, []string{a}, pos, false)
			if i < 0 {
				return "", nil, fmt.Errorf("hunk %d: context not found; could not find the @@ line:\n%s", hi+1, a)
			}
			pos, loosest = i+1, max(loosest, p)
		}
		at := len(lines)
		if len(h.old) > 0 {
			i, p := seek(lines, h.old, pos, h.eof)
			if i < 0 {
				return "", nil, fmt.Errorf("hunk %d: context not found; could not find these lines in order:\n%s", hi+1, strings.Join(h.old, "\n"))
			}
			at, loosest = i, max(loosest, p)
		} else if len(h.anchors) > 0 && !h.eof {
			at = pos
		}
		if h.eof && at+len(h.old) == len(lines) {
			trailingNL = true
		}
		if loosest > 0 {
			notes = append(notes, fmt.Sprintf("hunk %d matched %s", hi+1, matchPasses[loosest].name))
		}
		r := repl{at, len(h.old), append([]string(nil), h.new...)}
		for j, k := range h.ctx {
			if k >= 0 {
				r.new[j] = lines[at+k]
			}
		}
		repls = append(repls, r)
		pos = at + len(h.old)
	}
	for i := len(repls) - 1; i >= 0; i-- {
		r := repls[i]
		lines = append(lines[:r.at], append(r.new, lines[r.at+r.n:]...)...)
	}
	out := strings.Join(lines, "\n")
	if trailingNL && len(lines) > 0 {
		out += "\n"
	}
	if crlf {
		out = strings.ReplaceAll(out, "\n", "\r\n")
	}
	return out, notes, nil
}

// checkPatch validates every operation against the files under root and
// computes all new contents in memory. It writes nothing.
func checkPatch(root string, ops []patchOp, sc *scope) ([]fileChange, error) {
	seen := map[string]int{}
	claim := func(i int, op patchOp, p string) (string, error) {
		full, err := sc.resolve(root, p)
		if err != nil {
			return "", fmt.Errorf("%s: %w", op.label(i), err)
		}
		if err := sc.checkWrite(full, p); err != nil {
			return "", fmt.Errorf("%s: %w", op.label(i), err)
		}
		if j, ok := seen[full]; ok {
			return "", fmt.Errorf("%s: path %s appears twice (also in operation %d)", op.label(i), p, j+1)
		}
		seen[full] = i
		return full, nil
	}
	changes := make([]fileChange, 0, len(ops))
	for i, op := range ops {
		c := fileChange{op: op, mode: 0o644}
		var err error
		if c.full, err = claim(i, op, op.path); err != nil {
			return nil, err
		}
		if op.moveTo != "" {
			if c.dest, err = claim(i, op, op.moveTo); err != nil {
				return nil, err
			}
			if _, err := os.Lstat(c.dest); err == nil {
				return nil, fmt.Errorf("%s: move destination %s already exists", op.label(i), op.moveTo)
			}
		}
		name := rel(root, c.full)
		if op.kind == "add" {
			if _, err := os.Lstat(c.full); err == nil {
				return nil, fmt.Errorf("%s: file already exists", op.label(i))
			}
			if len(op.lines) > 0 {
				c.content = []byte(strings.Join(op.lines, "\n") + "\n")
			}
			c.summary = "A " + name
			changes = append(changes, c)
			continue
		}
		fi, err := os.Stat(c.full)
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%s: file does not exist", op.label(i))
		} else if err != nil {
			return nil, fmt.Errorf("%s: %w", op.label(i), err)
		}
		if !fi.Mode().IsRegular() {
			return nil, fmt.Errorf("%s: not a regular file", op.label(i))
		}
		data, err := os.ReadFile(c.full)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", op.label(i), err)
		}
		if isBinary(data) {
			return nil, fmt.Errorf("%s: binary file; apply_patch edits text files only", op.label(i))
		}
		c.mode = fi.Mode().Perm()
		if op.kind == "delete" {
			c.summary = "D " + name
			changes = append(changes, c)
			continue
		}
		out, notes, err := applyHunks(string(data), op.hunks)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", op.label(i), err)
		}
		c.content = []byte(out)
		if c.dest != "" {
			c.summary = "R " + name + " -> " + rel(root, c.dest)
		} else {
			c.summary = fmt.Sprintf("M %s (%d hunk%s)", name, len(op.hunks), plural(len(op.hunks)))
		}
		if len(notes) > 0 {
			c.summary += " [" + strings.Join(notes, "; ") + "]"
		}
		changes = append(changes, c)
	}
	return changes, nil
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func (c fileChange) write() error {
	switch c.op.kind {
	case "delete":
		return os.Remove(c.full)
	case "add":
		if err := os.MkdirAll(filepath.Dir(c.full), 0o755); err != nil {
			return err
		}
		return os.WriteFile(c.full, c.content, c.mode)
	}
	target := c.full
	if c.dest != "" {
		target = c.dest
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
	}
	if err := os.WriteFile(target, c.content, c.mode); err != nil {
		return err
	}
	if err := os.Chmod(target, c.mode); err != nil {
		return err
	}
	if c.dest != "" {
		return os.Remove(c.full)
	}
	return nil
}

// ApplyPatch parses input, checks every operation before writing anything,
// then applies the operations in order. A write failure after the check stops
// the run and reports which operations were applied; those are not rolled back.
func ApplyPatch(root, input string) (string, error) {
	return applyPatch(root, input, nil)
}

// applyPatch is ApplyPatch that rejects the whole patch when any operation
// touches a path the scope denies for writing.
func applyPatch(root, input string, sc *scope) (string, error) {
	ops, err := parsePatch(input)
	if err != nil {
		return "", fmt.Errorf("invalid patch: %w; nothing was written", err)
	}
	changes, err := checkPatch(root, ops, sc)
	if err != nil {
		return "", fmt.Errorf("patch does not apply: %w\nNothing was written", err)
	}
	var sb strings.Builder
	sb.WriteString("ok")
	for i, c := range changes {
		if err := c.write(); err != nil {
			var applied, pending []string
			for _, d := range changes[:i] {
				applied = append(applied, d.summary)
			}
			for _, d := range changes[i:] {
				pending = append(pending, d.summary)
			}
			return "", fmt.Errorf("%s failed: %w\napplied (not rolled back): %s\nnot applied: %s",
				c.op.label(i), err, listOrNone(applied), strings.Join(pending, ", "))
		}
		sb.WriteString("\n" + c.summary)
	}
	return sb.String(), nil
}

func listOrNone(s []string) string {
	if len(s) == 0 {
		return "none"
	}
	return strings.Join(s, ", ")
}
