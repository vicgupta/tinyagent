package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/vicgupta/tinyagent/internal/lsp"
	"github.com/vicgupta/tinyagent/internal/model"
)

// LSPToolName is the opt-in read-only language-server tool.
const LSPToolName = "lsp"

// MaxLSPLocations caps the locations one lsp call lists.
const MaxLSPLocations = 100

// maxLSPMessage caps one diagnostic message, in runes.
const maxLSPMessage = 300

// maxLSPSourceLine caps the source line shown with a location, in runes.
const maxLSPSourceLine = 200

// LSP connects the executor to the configured language servers.
type LSP struct {
	Manager *lsp.Manager
	// AfterEdit appends diagnostics to edit results.
	AfterEdit bool
	// Wait is the longest wait for diagnostics.
	Wait time.Duration
	// Max caps the diagnostics listed per file.
	Max int
	// Warnings also reports warnings (severity: warning).
	Warnings bool
}

// LSPSpec returns the lsp definition.
func LSPSpec() model.ToolSpec {
	return model.ToolSpec{
		Type: "function",
		Function: model.FunctionSpec{
			Name: LSPToolName,
			Description: "Ask the configured language server about a file (read-only). operation diagnostics lists the file's current " +
				"compiler errors; definition and references take a 1-based line and column of a symbol and return path:line:column " +
				"locations with their source line. Files changed on disk are re-sent to the server first.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"operation": map[string]any{"type": "string", "enum": []string{"diagnostics", "definition", "references"}, "description": "What to ask"},
					"path":      map[string]any{"type": "string", "description": "File path relative to the working directory"},
					"line":      map[string]any{"type": "integer", "description": "1-based line of the symbol (definition and references)"},
					"column":    map[string]any{"type": "integer", "description": "1-based column (character) of the symbol (definition and references)"},
				},
				"required": []string{"operation", "path"},
			},
		},
	}
}

func (l *LSP) wait() time.Duration {
	if l.Wait <= 0 {
		return 3 * time.Second
	}
	return l.Wait
}

func (l *LSP) max() int {
	if l.Max <= 0 {
		return 20
	}
	return l.Max
}

// noteEdited resolves path (already checked by the edit) into edited.
func (e *Executor) noteEdited(edited *[]string, path string) {
	if edited == nil {
		return
	}
	if full, err := e.scope().resolve(e.Root, path); err == nil {
		*edited = append(*edited, full)
	}
}

// lspAfterEdit returns the diagnostics notes for the edited files a server
// handles, or "" when every one of them is clean.
func (e *Executor) lspAfterEdit(ctx context.Context, edited []string) string {
	var paths []string
	seen := map[string]bool{}
	for _, p := range edited {
		if _, ok := e.LSP.Manager.ServerFor(p); ok && !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		return ""
	}
	var notes []string
	for _, fd := range e.LSP.Manager.Diagnostics(ctx, paths, e.LSP.wait()) {
		name := rel(e.Root, fd.Path)
		var ue *lsp.UnavailableError
		switch {
		case errors.As(fd.Err, &ue):
			notes = append(notes, fmt.Sprintf("[lsp %s: server unavailable; no diagnostics for %s]", fd.Server, name))
		case fd.Err != nil:
			notes = append(notes, fmt.Sprintf("[lsp %s: diagnostics failed for %s: %v]", fd.Server, name, fd.Err))
		case !fd.Ready:
			notes = append(notes, e.lspNotReady(fd.Server, name))
		default:
			if block := e.lspDiagnosticsBlock(fd); block != "" {
				notes = append(notes, block)
			}
		}
	}
	return strings.Join(notes, "\n")
}

func (e *Executor) lspNotReady(server, name string) string {
	return fmt.Sprintf("[lsp %s: diagnostics not ready after %d ms for %s]", server, e.LSP.wait().Milliseconds(), name)
}

// lspDiagnosticsBlock formats fd's diagnostics at the configured severity:
// a header, up to Max sorted path:line:column: message lines and an
// omitted count. "" when there are none.
func (e *Executor) lspDiagnosticsBlock(fd lsp.FileDiagnostics) string {
	var keep []lsp.Diagnostic
	errs, warns := 0, 0
	for _, d := range fd.Items {
		switch {
		case d.IsError():
			errs++
		case d.Severity == lsp.SeverityWarning && e.LSP.Warnings:
			warns++
		default:
			continue
		}
		keep = append(keep, d)
	}
	if len(keep) == 0 {
		return ""
	}
	sort.SliceStable(keep, func(i, j int) bool {
		a, b := keep[i].Range.Start, keep[j].Range.Start
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Character < b.Character
	})
	name := rel(e.Root, fd.Path)
	counts := countNoun(errs, "error")
	if warns > 0 {
		counts += ", " + countNoun(warns, "warning")
	}
	lines := fileLines(fd.Path)
	var sb strings.Builder
	fmt.Fprintf(&sb, "[lsp %s: %s in %s]", fd.Server, counts, name)
	shown := min(len(keep), e.LSP.max())
	for _, d := range keep[:shown] {
		line, col := d.Range.Start.Line, d.Range.Start.Character
		if line >= 0 && line < len(lines) {
			col = lsp.UTF16ToRune(lines[line], col)
		}
		msg := oneLine(d.Message, maxLSPMessage)
		if !d.IsError() {
			msg = "warning: " + msg
		}
		fmt.Fprintf(&sb, "\n%s:%d:%d: %s", name, line+1, col+1, msg)
	}
	if n := len(keep) - shown; n > 0 {
		fmt.Fprintf(&sb, "\n[lsp %s: omitted %d more diagnostics in %s]", fd.Server, n, name)
	}
	return sb.String()
}

func countNoun(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// oneLine collapses whitespace runs (newlines included) and caps s.
func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > max {
		s = string(r[:max]) + "…"
	}
	return s
}

// fileLines returns path's lines ("" lines on error).
func fileLines(path string) []string {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
}

// lspTool runs one lsp call. It reaches the same files read_file does.
func (e *Executor) lspTool(ctx context.Context, a args) (string, error) {
	op := a.Operation
	if op != "diagnostics" && op != "definition" && op != "references" {
		return "", fmt.Errorf("operation must be diagnostics, definition or references, got %q", op)
	}
	if strings.TrimSpace(a.Path) == "" {
		return "", errors.New("path is required")
	}
	sc := e.scope()
	full, err := sc.resolve(e.Root, a.Path)
	if err != nil {
		return "", err
	}
	if err := sc.checkRead(full, a.Path); err != nil {
		return "", err
	}
	if isDir(full) {
		return "", fmt.Errorf("%s is a directory", a.Path)
	}
	if _, ok := e.LSP.Manager.ServerFor(full); !ok {
		if ext := filepath.Ext(full); ext != "" {
			return "", fmt.Errorf("no lsp server configured for %s files", ext)
		}
		return "", fmt.Errorf("no lsp server configured for files without an extension (%s)", a.Path)
	}
	if _, err := os.Stat(full); err != nil {
		return "", err
	}
	name := rel(e.Root, full)
	if op == "diagnostics" {
		fd := e.LSP.Manager.Diagnostics(ctx, []string{full}, e.LSP.wait())[0]
		switch {
		case fd.Err != nil:
			return "", fd.Err
		case !fd.Ready:
			return e.lspNotReady(fd.Server, name), nil
		}
		if block := e.lspDiagnosticsBlock(fd); block != "" {
			return block, nil
		}
		if e.LSP.Warnings {
			return fmt.Sprintf("[lsp %s: no errors or warnings in %s]", fd.Server, name), nil
		}
		return fmt.Sprintf("[lsp %s: no errors in %s]", fd.Server, name), nil
	}
	line, err := intArg("line", a.Line)
	if err != nil {
		return "", err
	}
	col, err := intArg("column", a.Column)
	if err != nil {
		return "", err
	}
	if line < 1 || col < 1 {
		return "", fmt.Errorf("%s needs a 1-based line and column, got line %d column %d", op, line, col)
	}
	lines := fileLines(full)
	if line > len(lines) {
		return "", fmt.Errorf("line %d is past the end of %s (%d lines)", line, a.Path, len(lines))
	}
	pos := lsp.Position{Line: line - 1, Character: lsp.RuneToUTF16(lines[line-1], col-1)}
	var locs []lsp.Location
	if op == "definition" {
		locs, err = e.LSP.Manager.Definition(ctx, full, pos)
	} else {
		locs, err = e.LSP.Manager.References(ctx, full, pos)
	}
	if err != nil {
		if errors.Is(err, lsp.ErrNoServer) {
			return "", fmt.Errorf("no lsp server configured for %s files", filepath.Ext(full))
		}
		return "", err
	}
	return e.formatLocations(op, locs), nil
}

// formatLocations lists the locations the model may read, dropping (and
// counting) those outside the workspace or denied by file_access.
func (e *Executor) formatLocations(op string, locs []lsp.Location) string {
	sc := e.scope()
	type shown struct {
		path, name string
		line, col  int
	}
	var keep []shown
	seen := map[string]bool{}
	omitted := 0
	cache := map[string][]string{}
	for _, l := range locs {
		full, err := sc.resolve(e.Root, l.Path)
		if err != nil || !filepath.IsAbs(l.Path) || sc.checkRead(full, l.Path) != nil || sc.hidden(full) {
			omitted++
			continue
		}
		lines, ok := cache[full]
		if !ok {
			lines = fileLines(full)
			cache[full] = lines
		}
		line, col := l.Range.Start.Line, l.Range.Start.Character
		if line >= 0 && line < len(lines) {
			col = lsp.UTF16ToRune(lines[line], col)
		}
		key := fmt.Sprintf("%s:%d:%d", full, line, col)
		if seen[key] {
			continue
		}
		seen[key] = true
		keep = append(keep, shown{full, rel(e.Root, full), line, col})
	}
	sort.SliceStable(keep, func(i, j int) bool {
		if keep[i].name != keep[j].name {
			return keep[i].name < keep[j].name
		}
		if keep[i].line != keep[j].line {
			return keep[i].line < keep[j].line
		}
		return keep[i].col < keep[j].col
	})
	var sb strings.Builder
	if len(keep) == 0 {
		if op == "definition" {
			sb.WriteString("no definition found")
		} else {
			sb.WriteString("no references found")
		}
	}
	n := min(len(keep), MaxLSPLocations)
	for i, k := range keep[:n] {
		if i > 0 {
			sb.WriteString("\n")
		}
		src := ""
		if lines := cache[k.path]; k.line >= 0 && k.line < len(lines) {
			src = oneLine(lines[k.line], maxLSPSourceLine)
		}
		fmt.Fprintf(&sb, "%s:%d:%d: %s", k.name, k.line+1, k.col+1, src)
	}
	if more := len(keep) - n; more > 0 {
		fmt.Fprintf(&sb, "\n[%d more locations omitted]", more)
	}
	if omitted > 0 {
		fmt.Fprintf(&sb, "\n[%d locations outside the workspace omitted]", omitted)
	}
	return sb.String()
}
