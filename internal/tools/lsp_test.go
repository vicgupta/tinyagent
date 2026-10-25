package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/lsp"
	"github.com/vicgupta/tinyagent/internal/lsp/lsptest"
	"github.com/vicgupta/tinyagent/internal/model"
)

func lspRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

// lspExec returns an executor with the fake go server and post-edit
// diagnostics on.
func lspExec(t *testing.T, root string, servers ...config.LSPServer) *Executor {
	t.Helper()
	if len(servers) == 0 {
		servers = []config.LSPServer{lsptest.Server(t, "go", []string{".go"})}
	}
	m := lsp.New(lsp.Options{Root: root, Servers: servers})
	t.Cleanup(m.Close)
	return &Executor{Root: root, ApplyPatch: true, LSP: &LSP{Manager: m, AfterEdit: true, Wait: 3 * time.Second, Max: 20}}
}

func callResult(t *testing.T, e *Executor, name string, a map[string]any) Result {
	t.Helper()
	b, _ := json.Marshal(a)
	return e.RunResult(context.Background(), model.ToolCall{ID: "1", Function: model.FunctionCall{Name: name, Arguments: string(b)}})
}

func specNames(specs []model.ToolSpec) map[string]bool {
	out := map[string]bool{}
	for _, s := range specs {
		out[s.Function.Name] = true
	}
	return out
}

func TestLSPOffByDefault(t *testing.T) {
	root := lspRoot(t)
	e := &Executor{Root: root}
	if specNames(e.Catalog())[LSPToolName] {
		t.Fatal("lsp advertised without LSP")
	}
	r := callResult(t, e, LSPToolName, map[string]any{"operation": "diagnostics", "path": "a.go"})
	if !r.IsError || !strings.Contains(r.Text, `unknown tool "lsp"`) {
		t.Fatalf("lsp without LSP: %+v", r)
	}
	r = callResult(t, e, "write_file", map[string]any{"path": "a.go", "content": "ERR: x\n"})
	if r.Text != "ok" {
		t.Fatalf("write_file changed without LSP: %q", r.Text)
	}
}

func TestLSPEditAppendsErrors(t *testing.T) {
	root := lspRoot(t)
	e := lspExec(t, root)
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n\nfunc f() {}\n"), 0o644)
	r := callResult(t, e, "edit_file", map[string]any{"path": "a.go", "old_str": "func f() {}", "new_str": "func f() { ERR: undefined: x }"})
	if r.IsError {
		t.Fatalf("edit is_error: %q", r.Text)
	}
	want := "[lsp go: 1 error in a.go]\na.go:3:12: undefined: x }"
	if !strings.HasSuffix(r.Text, want) || !strings.HasPrefix(r.Text, "ok") {
		t.Fatalf("edit result:\n%s\nwant suffix:\n%s", r.Text, want)
	}
}

func TestLSPCleanEditUnchanged(t *testing.T) {
	root := lspRoot(t)
	e := lspExec(t, root)
	plain := &Executor{Root: root}
	for i, ex := range []*Executor{plain, e} {
		r := callResult(t, ex, "write_file", map[string]any{"path": fmt.Sprintf("c%d.go", i), "content": "package a\n"})
		if r.Text != "ok" || r.IsError {
			t.Fatalf("clean write %d: %+v", i, r)
		}
	}
	os.WriteFile(filepath.Join(root, "d.go"), []byte("package a\nvar x = 1\n"), 0o644)
	a := callResult(t, plain, "edit_file", map[string]any{"path": "d.go", "old_str": "1", "new_str": "2"})
	b := callResult(t, e, "edit_file", map[string]any{"path": "d.go", "old_str": "2", "new_str": "3"})
	if a.Text != b.Text {
		t.Fatalf("clean edit differs: %q vs %q", a.Text, b.Text)
	}
}

func TestLSPDiagnosticsTimeout(t *testing.T) {
	root := lspRoot(t)
	e := lspExec(t, root, lsptest.Server(t, "go", []string{".go"}, "nopublish"))
	e.LSP.Wait = 300 * time.Millisecond
	// Warm the server so startup is not part of the timing.
	callResult(t, e, "write_file", map[string]any{"path": "w.go", "content": "package a\n"})
	start := time.Now()
	r := callResult(t, e, "write_file", map[string]any{"path": "a.go", "content": "package a\n"})
	if d := time.Since(start); d > 300*time.Millisecond+500*time.Millisecond {
		t.Errorf("took %v", d)
	}
	if r.IsError || !strings.HasSuffix(r.Text, "[lsp go: diagnostics not ready after 300 ms for a.go]") {
		t.Fatalf("timeout result: %+v", r)
	}
}

func TestLSPDiagnosticsCapAndSeverity(t *testing.T) {
	root := lspRoot(t)
	e := lspExec(t, root)
	var sb strings.Builder
	sb.WriteString("package a\n")
	for i := 0; i < 50; i++ {
		fmt.Fprintf(&sb, "ERR: bad %d\n", i)
	}
	for i := 0; i < 10; i++ {
		fmt.Fprintf(&sb, "WARN: meh %d\n", i)
	}
	r := callResult(t, e, "write_file", map[string]any{"path": "a.go", "content": sb.String()})
	lines := strings.Split(r.Text, "\n")
	var diag int
	for _, l := range lines {
		if strings.HasPrefix(l, "a.go:") {
			diag++
		}
		if strings.Contains(l, "meh") {
			t.Errorf("warning reported at severity error: %s", l)
		}
	}
	if diag != 20 {
		t.Errorf("got %d diagnostic lines, want 20:\n%s", diag, r.Text)
	}
	if !strings.Contains(r.Text, "[lsp go: 50 errors in a.go]") || !strings.HasSuffix(r.Text, "[lsp go: omitted 30 more diagnostics in a.go]") {
		t.Errorf("header/omitted note missing:\n%s", r.Text)
	}
	if lines[2] != "a.go:2:1: bad 0" || lines[21] != "a.go:21:1: bad 19" {
		t.Errorf("lines not sorted: %q %q", lines[2], lines[21])
	}

	e.LSP.Warnings = true
	r = callResult(t, e, "write_file", map[string]any{"path": "b.go", "content": "package a\nWARN: meh\n"})
	if !strings.HasSuffix(r.Text, "[lsp go: 0 errors, 1 warning in b.go]\nb.go:2:1: warning: meh") {
		t.Errorf("warning severity:\n%s", r.Text)
	}
}

func TestLSPApplyPatchOnlyHandledFiles(t *testing.T) {
	root := lspRoot(t)
	log := filepath.Join(root, "..", "lsp.log")
	e := lspExec(t, root, lsptest.Server(t, "go", []string{".go"}, "log="+log))
	patch := "*** Begin Patch\n*** Add File: x.go\n+package a\n+ERR: in x\n*** Add File: y.go\n+package a\n+ERR: in y\n*** Add File: notes.md\n+ERR: not go\n*** End Patch\n"
	r := callResult(t, e, ApplyPatchToolName, map[string]any{"input": patch})
	if r.IsError {
		t.Fatal(r.Text)
	}
	if !strings.Contains(r.Text, "[lsp go: 1 error in x.go]\nx.go:2:1: in x") || !strings.Contains(r.Text, "[lsp go: 1 error in y.go]\ny.go:2:1: in y") {
		t.Errorf("missing .go diagnostics:\n%s", r.Text)
	}
	if strings.Contains(r.Text, "notes.md") && strings.Contains(r.Text, "not go") {
		t.Errorf(".md diagnosed:\n%s", r.Text)
	}
	b, _ := os.ReadFile(log)
	if strings.Contains(string(b), "notes.md") {
		t.Errorf("server was sent notes.md:\n%s", b)
	}
}

func TestLSPReferencesFiltered(t *testing.T) {
	parent := lspRoot(t)
	root := filepath.Join(parent, "ws")
	os.MkdirAll(filepath.Join(root, "secret"), 0o755)
	files := map[string]string{
		"a.go":          "package a\n\nfunc Save() {}\n",
		"b.go":          "package a\n\nfunc g() { Save() }\n",
		"secret/s.go":   "package secret\nvar _ = Save\n",
		"../outside.go": "package o\nvar _ = Save\n",
	}
	for name, c := range files {
		os.WriteFile(filepath.Join(root, name), []byte(c), 0o644)
	}
	refs := filepath.Join(parent, "refs.json")
	lsptest.WriteLocs(t, refs, []lsptest.Loc{
		{Path: filepath.Join(root, "b.go"), Line: 2, Character: 11},
		{Path: filepath.Join(root, "secret", "s.go"), Line: 1, Character: 8},
		{Path: filepath.Join(parent, "outside.go"), Line: 1, Character: 8},
	})
	e := lspExec(t, root, lsptest.Server(t, "go", []string{".go"}, "refs="+refs, "def="+refs))
	fa, err := NewFileAccess(root, []string{"secret/**"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	e.FileAccess = fa
	r := callResult(t, e, LSPToolName, map[string]any{"operation": "references", "path": "a.go", "line": 3, "column": 6})
	want := "b.go:3:12: func g() { Save() }\n[2 locations outside the workspace omitted]"
	if r.IsError || r.Text != want {
		t.Fatalf("references:\n%s\nwant:\n%s", r.Text, want)
	}
	if strings.Contains(r.Text, "s.go") || strings.Contains(r.Text, "outside.go") {
		t.Fatal("leaked a denied location")
	}

	r = callResult(t, e, LSPToolName, map[string]any{"operation": "references", "path": "secret/s.go", "line": 1, "column": 1})
	if !r.IsError {
		t.Errorf("lsp on a deny_read file: %+v", r)
	}
	r = callResult(t, e, LSPToolName, map[string]any{"operation": "definition", "path": "../outside.go", "line": 1, "column": 1})
	if !r.IsError {
		t.Errorf("lsp outside the root: %+v", r)
	}
	r = callResult(t, e, LSPToolName, map[string]any{"operation": "definition", "path": "x.rs", "line": 1, "column": 1})
	if !r.IsError || !strings.Contains(r.Text, "no lsp server configured for .rs files") {
		t.Errorf("unhandled extension: %+v", r)
	}
	r = callResult(t, e, LSPToolName, map[string]any{"operation": "hover", "path": "a.go"})
	if !r.IsError {
		t.Errorf("bad operation: %+v", r)
	}
	r = callResult(t, e, LSPToolName, map[string]any{"operation": "definition", "path": "a.go"})
	if !r.IsError {
		t.Errorf("definition without position: %+v", r)
	}
}

func TestLSPReferencesCap(t *testing.T) {
	parent := lspRoot(t)
	root := filepath.Join(parent, "ws")
	os.MkdirAll(root, 0o755)
	var sb strings.Builder
	var locs []lsptest.Loc
	for i := 0; i < 120; i++ {
		fmt.Fprintf(&sb, "x%d\n", i)
		locs = append(locs, lsptest.Loc{Path: filepath.Join(root, "a.go"), Line: i})
	}
	os.WriteFile(filepath.Join(root, "a.go"), []byte(sb.String()), 0o644)
	refs := filepath.Join(parent, "refs.json")
	lsptest.WriteLocs(t, refs, locs)
	e := lspExec(t, root, lsptest.Server(t, "go", []string{".go"}, "refs="+refs))
	r := callResult(t, e, LSPToolName, map[string]any{"operation": "references", "path": "a.go", "line": 1, "column": 1})
	lines := strings.Split(r.Text, "\n")
	if len(lines) != MaxLSPLocations+1 || lines[len(lines)-1] != "[20 more locations omitted]" {
		t.Fatalf("cap: %d lines, last %q", len(lines), lines[len(lines)-1])
	}
}

func TestLSPUTF16Columns(t *testing.T) {
	root := lspRoot(t)
	e := lspExec(t, root)
	// "é😀" is 2 runes but 3 UTF-16 units: the marker's rune column is 4.
	r := callResult(t, e, "write_file", map[string]any{"path": "u.go", "content": "é😀 ERR: wide\n"})
	if !strings.HasSuffix(r.Text, "u.go:1:4: wide") {
		t.Fatalf("utf-16 column:\n%s", r.Text)
	}
}

func TestLSPReadOnly(t *testing.T) {
	root := lspRoot(t)
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\nERR: broken\n"), 0o644)
	e := lspExec(t, root)
	e.ReadOnly = true
	if !specNames(e.Catalog())[LSPToolName] {
		t.Fatal("lsp not advertised in read-only mode")
	}
	r := callResult(t, e, LSPToolName, map[string]any{"operation": "diagnostics", "path": "a.go"})
	if r.IsError || r.Text != "[lsp go: 1 error in a.go]\na.go:2:1: broken" {
		t.Fatalf("read-only diagnostics: %+v", r)
	}
	r = callResult(t, e, "write_file", map[string]any{"path": "b.go", "content": "ERR: x\n"})
	if !r.IsError || strings.Contains(r.Text, "[lsp") {
		t.Fatalf("read-only write: %+v", r)
	}
	if _, err := os.Stat(filepath.Join(root, "b.go")); err == nil {
		t.Fatal("read-only write created a file")
	}
}

func TestLSPDiagnosticsClean(t *testing.T) {
	root := lspRoot(t)
	os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n"), 0o644)
	e := lspExec(t, root)
	r := callResult(t, e, LSPToolName, map[string]any{"operation": "diagnostics", "path": "a.go"})
	if r.IsError || r.Text != "[lsp go: no errors in a.go]" {
		t.Fatalf("clean diagnostics: %+v", r)
	}
}

// TestLSPSeesBashEdits: a file rewritten outside the edit tools is re-sent
// before the server answers.
func TestLSPSeesBashEdits(t *testing.T) {
	root := lspRoot(t)
	log := filepath.Join(root, "..", "bash-lsp.log")
	e := lspExec(t, root, lsptest.Server(t, "go", []string{".go"}, "log="+log))
	callResult(t, e, "write_file", map[string]any{"path": "a.go", "content": "package a\n"})
	r := callResult(t, e, "bash", map[string]any{"command": "printf 'package a\\nERR: from bash\\n' > a.go"})
	if r.IsError {
		t.Fatal(r.Text)
	}
	r = callResult(t, e, LSPToolName, map[string]any{"operation": "diagnostics", "path": "a.go"})
	if r.Text != "[lsp go: 1 error in a.go]\na.go:2:1: from bash" {
		t.Fatalf("after bash rewrite: %+v", r)
	}
	b, _ := os.ReadFile(log)
	if !strings.Contains(string(b), `didChange "package a\nERR: from bash\n"`) {
		t.Errorf("server log:\n%s", b)
	}
}

func TestLSPUnavailableNote(t *testing.T) {
	root := lspRoot(t)
	e := lspExec(t, root, config.LSPServer{Name: "go", Command: filepath.Join(root, "no-such-server"), Extensions: []string{".go"}, StartupTimeoutSeconds: 5})
	r := callResult(t, e, "write_file", map[string]any{"path": "a.go", "content": "package a\n"})
	if r.IsError || r.Text != "ok\n[lsp go: server unavailable; no diagnostics for a.go]" {
		t.Fatalf("unavailable write: %+v", r)
	}
	r = callResult(t, e, LSPToolName, map[string]any{"operation": "diagnostics", "path": "a.go"})
	if !r.IsError || !strings.Contains(r.Text, "lsp server go is unavailable") {
		t.Fatalf("unavailable lsp call: %+v", r)
	}
}
