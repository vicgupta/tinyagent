package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/model"
)

func writeFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// snapshot returns every file under root and its content.
func snapshot(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			b, _ := os.ReadFile(p)
			out[rel(root, p)] = string(b)
		}
		return nil
	})
	return out
}

func sameFiles(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

func patchCall(input string) model.ToolCall {
	b, _ := json.Marshal(map[string]string{"input": input})
	return model.ToolCall{Function: model.FunctionCall{Name: ApplyPatchToolName, Arguments: string(b)}}
}

func TestApplyPatchAllOperations(t *testing.T) { // AC3
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"a.go":  "package a\n\nfunc One() int {\n\treturn 1\n}\n\nfunc Two() int {\n\treturn 2\n}\n",
		"b.go":  "package b\n\nconst X = 1\n",
		"e.txt": "bye\n",
	})
	patch := `*** Begin Patch
*** Update File: a.go
@@ func One() int {
-	return 1
+	return 10
 }
@@ func Two() int {
-	return 2
+	return 20
 }
*** Update File: b.go
*** Move to: c.go
@@
 package b

-const X = 1
+const X = 3
*** Add File: d.txt
+hello
+world
*** Delete File: e.txt
*** End Patch`
	e := &Executor{Root: root, ApplyPatch: true}
	r := e.RunResult(context.Background(), patchCall(patch))
	out, isErr := r.Text, r.IsError
	if isErr {
		t.Fatal(out)
	}
	if want := "ok\nM a.go (2 hunks)\nR b.go -> c.go\nA d.txt\nD e.txt"; out != want {
		t.Errorf("out = %q, want %q", out, want)
	}
	want := map[string]string{
		"a.go":  "package a\n\nfunc One() int {\n\treturn 10\n}\n\nfunc Two() int {\n\treturn 20\n}\n",
		"c.go":  "package b\n\nconst X = 3\n",
		"d.txt": "hello\nworld\n",
	}
	got := snapshot(t, root)
	for k := range got {
		if strings.HasPrefix(k, ".tinyagent/") {
			delete(got, k) // undo_file snapshots
		}
	}
	if !sameFiles(got, want) {
		t.Errorf("files = %q", got)
	}
}

func TestApplyPatchFailedCheckWritesNothing(t *testing.T) { // AC4
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"a.txt": "one\ntwo\n", "b.txt": "three\n"})
	before := snapshot(t, root)
	patch := "*** Begin Patch\n*** Update File: a.txt\n-one\n+ONE\n*** Update File: b.txt\n missing line\n-three\n+THREE\n*** Add File: new.txt\n+x\n*** End Patch"
	e := &Executor{Root: root, ApplyPatch: true}
	r := e.RunResult(context.Background(), patchCall(patch))
	out, isErr := r.Text, r.IsError
	if !isErr || !strings.Contains(out, "operation 2 (update b.txt)") || !strings.Contains(out, "context not found") ||
		!strings.Contains(out, "missing line\nthree") || !strings.Contains(out, "Nothing was written") {
		t.Errorf("out = %q", out)
	}
	if got := snapshot(t, root); !sameFiles(got, before) {
		t.Errorf("files changed: %q", got)
	}
}

func TestApplyPatchRejections(t *testing.T) { // AC5, AC8, AC10
	cases := map[string]struct{ patch, want string }{
		"add existing":         {"*** Begin Patch\n*** Add File: a.txt\n+x\n*** End Patch", "file already exists"},
		"update missing":       {"*** Begin Patch\n*** Update File: nope.txt\n-a\n+b\n*** End Patch", "file does not exist"},
		"delete missing":       {"*** Begin Patch\n*** Delete File: nope.txt\n*** End Patch", "file does not exist"},
		"move onto existing":   {"*** Begin Patch\n*** Update File: a.txt\n*** Move to: b.txt\n-a\n+b\n*** End Patch", "move destination b.txt already exists"},
		"same path twice":      {"*** Begin Patch\n*** Update File: a.txt\n-a\n+b\n*** Delete File: ./a.txt\n*** End Patch", "appears twice"},
		"no end":               {"*** Begin Patch\n*** Delete File: a.txt\n", "*** End Patch"},
		"no begin":             {"*** Delete File: a.txt\n*** End Patch", "*** Begin Patch"},
		"no operations":        {"*** Begin Patch\n*** End Patch", "no file operations"},
		"unknown prefix":       {"*** Begin Patch\n*** Update File: a.txt\n-a\n?b\n*** End Patch", "unknown line prefix"},
		"add without plus":     {"*** Begin Patch\n*** Add File: z.txt\nx\n*** End Patch", "must start with +"},
		"escape":               {"*** Begin Patch\n*** Add File: ../x\n+x\n*** End Patch", "outside the working directory"},
		"escape absolute":      {"*** Begin Patch\n*** Delete File: " + filepath.Join(os.TempDir(), "x") + "\n*** End Patch", "outside the working directory"},
		"escape move":          {"*** Begin Patch\n*** Update File: a.txt\n*** Move to: ../moved.txt\n-a\n+b\n*** End Patch", "outside the working directory"},
		"binary update":        {"*** Begin Patch\n*** Update File: bin.dat\n-a\n+b\n*** End Patch", "binary file"},
		"binary delete":        {"*** Begin Patch\n*** Delete File: bin.dat\n*** End Patch", "binary file"},
		"directory":            {"*** Begin Patch\n*** Delete File: dir\n*** End Patch", "not a regular file"},
		"non-whitespace drift": {"*** Begin Patch\n*** Update File: a.txt\n-A\n+b\n*** End Patch", "context not found"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeFiles(t, root, map[string]string{"a.txt": "a\n", "b.txt": "b\n", "bin.dat": "a\x00\n", "dir/f": "f\n"})
			before := snapshot(t, root)
			out, err := ApplyPatch(root, tc.patch)
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(strings.ToLower(err.Error()), "nothing was written") {
				t.Errorf("out %q, err %v, want %q", out, err, tc.want)
			}
			if got := snapshot(t, root); !sameFiles(got, before) {
				t.Errorf("files changed: %q", got)
			}
		})
	}
	if _, err := ApplyPatch(t.TempDir(), strings.Repeat("x", MaxPatchBytes+1)); err == nil || !strings.Contains(err.Error(), "byte limit") {
		t.Errorf("oversize: %v", err)
	}
}

func TestApplyPatchAnchors(t *testing.T) { // AC6
	src := "type A struct{}\n\nfunc (A) First() {\n\tx := 1\n}\n\ntype B struct{}\n\nfunc (B) First() {\n\tx := 1\n}\n\nfunc Second() {\n\tx := 1\n}\n"
	for name, tc := range map[string]struct{ anchors, want string }{
		"one anchor": {"@@ func Second() {\n", "func Second() {\n\tx := 2\n}\n"},
		"stacked":    {"@@ type B struct{}\n@@ func (B) First() {\n", "func (B) First() {\n\tx := 2\n}\n"},
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeFiles(t, root, map[string]string{"f.go": src})
			patch := "*** Begin Patch\n*** Update File: f.go\n" + tc.anchors + "-\tx := 1\n+\tx := 2\n*** End Patch"
			if out, err := ApplyPatch(root, patch); err != nil || out != "ok\nM f.go (1 hunk)" {
				t.Fatalf("out %q, err %v", out, err)
			}
			got := snapshot(t, root)["f.go"]
			if strings.Count(got, "x := 2") != 1 || !strings.Contains(got, tc.want) {
				t.Errorf("f.go = %q", got)
			}
		})
	}
}

func TestApplyPatchLooseMatchAndEndOfFile(t *testing.T) { // AC7
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"f.txt": "alpha  \nbeta\nlast\n"})
	patch := "*** Begin Patch\n*** Update File: f.txt\n alpha\n-beta\n+BETA\n@@\n-last\n+LAST\n+more\n*** End of File\n*** End Patch"
	out, err := ApplyPatch(root, patch)
	if err != nil || out != "ok\nM f.txt (2 hunks) [hunk 1 matched ignoring trailing whitespace]" {
		t.Fatalf("out %q, err %v", out, err)
	}
	if got := snapshot(t, root)["f.txt"]; got != "alpha  \nBETA\nLAST\nmore\n" {
		t.Errorf("f.txt = %q", got)
	}
}

func TestApplyPatchPreservesFileDetails(t *testing.T) { // AC12
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"crlf.txt":   "a\r\nb\r\nc\r\n",
		"run.sh":     "#!/bin/sh\necho a\necho b\n",
		"nonl.txt":   "one\ntwo\nthree",
		"heredoc.md": "x\n",
	})
	if err := os.Chmod(filepath.Join(root, "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	patch := `apply_patch <<"EOF"
*** Begin Patch
*** Update File: crlf.txt
 a
-b
+B
 c
*** Update File: run.sh
-echo a
+echo A
*** Update File: nonl.txt
 one
-two
+TWO
*** End Patch
EOF`
	if out, err := ApplyPatch(root, patch); err != nil {
		t.Fatalf("out %q, err %v", out, err)
	}
	got := snapshot(t, root)
	if got["crlf.txt"] != "a\r\nB\r\nc\r\n" || got["run.sh"] != "#!/bin/sh\necho A\necho b\n" || got["nonl.txt"] != "one\nTWO\nthree" {
		t.Errorf("files = %q", got)
	}
	if fi, _ := os.Stat(filepath.Join(root, "run.sh")); runtime.GOOS != "windows" && fi.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v", fi.Mode())
	}
}

func TestApplyPatchPartialWriteFailure(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("needs POSIX permissions as non-root")
	}
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"locked/f.txt": "a\n"})
	before := snapshot(t, root)
	locked := filepath.Join(root, "locked")
	if err := os.Chmod(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(locked, 0o755) })
	patch := "*** Begin Patch\n*** Add File: first.txt\n+1\n*** Delete File: locked/f.txt\n*** Add File: third.txt\n+3\n*** End Patch"
	_, err := ApplyPatch(root, patch)
	if err == nil || !strings.Contains(err.Error(), "operation 2 (delete locked/f.txt) failed") ||
		!strings.Contains(err.Error(), "rolled back: A first.txt") || !strings.Contains(err.Error(), "not applied: D locked/f.txt, A third.txt") {
		t.Fatalf("err = %v", err)
	}
	if got := snapshot(t, root); !sameFiles(got, before) {
		t.Errorf("files after rollback = %v, want %v", got, before)
	}
}

type fileState struct {
	content string
	mode    os.FileMode
}

func treeState(t *testing.T, root string) map[string]fileState {
	t.Helper()
	out := map[string]fileState{}
	err := filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil || fi.IsDir() {
			return nil
		}
		b, _ := os.ReadFile(p)
		out[rel(root, p)] = fileState{content: string(b), mode: fi.Mode().Perm()}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func sameTree(a, b map[string]fileState) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || w != v {
			return false
		}
	}
	return true
}

func TestApplyPatchWriteFailureRollsBackEveryOperation(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("needs POSIX permissions as non-root")
	}
	patch := `*** Begin Patch
*** Add File: 1-add/a.txt
+added
*** Update File: 2-upd/u.txt
-old
+updated
*** Update File: 3-mv/src.txt
*** Move to: 3-mv/dst.txt
-src
+moved
*** Delete File: 4-del/d.txt
*** Add File: 5-add/z.txt
+last
*** End Patch`
	dirs := []string{"1-add", "2-upd", "3-mv", "4-del", "5-add"}
	for failAt := 1; failAt <= len(dirs); failAt++ {
		t.Run(fmt.Sprintf("operation %d", failAt), func(t *testing.T) {
			root := t.TempDir()
			for _, d := range dirs {
				if err := os.MkdirAll(filepath.Join(root, d), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			writeFiles(t, root, map[string]string{
				"2-upd/u.txt":  "old\n",
				"3-mv/src.txt": "src\n",
				"4-del/d.txt":  "delete me\n",
			})
			modes := map[string]os.FileMode{
				"2-upd/u.txt":  0o640,
				"3-mv/src.txt": 0o750,
				"4-del/d.txt":  0o600,
			}
			// A read-only directory stops creates, deletes and renames; an
			// existing file needs its own read-only mode to stop an update.
			if failAt == 2 {
				modes["2-upd/u.txt"] = 0o444
			}
			for name, mode := range modes {
				if err := os.Chmod(filepath.Join(root, name), mode); err != nil {
					t.Fatal(err)
				}
			}
			before := treeState(t, root)
			blocked := filepath.Join(root, dirs[failAt-1])
			blockMode := os.FileMode(0o555)
			if failAt == 2 {
				blocked = filepath.Join(root, "2-upd/u.txt")
				blockMode = modes["2-upd/u.txt"]
			}
			if err := os.Chmod(blocked, blockMode); err != nil {
				t.Fatal(err)
			}
			defer os.Chmod(blocked, 0o755)

			_, err := ApplyPatch(root, patch)
			if err == nil {
				t.Fatal("patch succeeded, want a write failure")
			}
			if !strings.Contains(err.Error(), fmt.Sprintf("operation %d ", failAt)) ||
				!strings.Contains(err.Error(), "rolled back:") {
				t.Errorf("err = %v", err)
			}
			if got := treeState(t, root); !sameTree(got, before) {
				t.Errorf("files after rollback = %v, want %v", got, before)
			}
			if entries, _ := os.ReadDir(filepath.Join(root, "1-add")); len(entries) != 0 {
				t.Errorf("1-add not empty after rollback: %v", entries)
			}
		})
	}
}

func TestApplyPatchOptIn(t *testing.T) { // AC2, AC9
	names := func(e *Executor) string {
		var n []string
		for _, s := range e.Catalog() {
			n = append(n, s.Function.Name)
		}
		return strings.Join(n, ",")
	}
	off := &Executor{Root: t.TempDir()}
	if strings.Contains(names(off), ApplyPatchToolName) {
		t.Error("apply_patch offered while off")
	}
	on := &Executor{Root: t.TempDir(), ApplyPatch: true}
	if n := names(on); !strings.HasSuffix(n, ",apply_patch") || !strings.Contains(n, "edit_file") || !strings.Contains(n, "write_file") {
		t.Errorf("catalog = %s", n)
	}
	params := ApplyPatchSpec().Function.Parameters
	if req, _ := params["required"].([]string); len(req) != 1 || req[0] != "input" {
		t.Errorf("required = %v", params["required"])
	}
	if on.FormatError(model.ToolCall{Function: model.FunctionCall{Name: ApplyPatchToolName, Arguments: `{}`}}) == "" {
		t.Error("missing input is not a format error")
	}
	if r := off.RunResult(context.Background(), patchCall("*** Begin Patch\n*** Add File: x\n+x\n*** End Patch")); !r.IsError || !strings.Contains(r.Text, "unknown tool") {
		t.Errorf("off: %q", r.Text)
	}
	ro := &Executor{Root: t.TempDir(), ApplyPatch: true, ReadOnly: true}
	if strings.Contains(names(ro), ApplyPatchToolName) || ro.Permits(ApplyPatchToolName) {
		t.Error("apply_patch offered in read-only mode")
	}
	call := patchCall("*** Begin Patch\n*** Add File: x\n+x\n*** End Patch")
	if ro.FormatError(call) != "" {
		t.Error("read-only apply_patch call is a format error, want the read-only denial")
	}
	if r := ro.RunResult(context.Background(), call); !r.IsError || !strings.Contains(r.Text, "read-only mode") {
		t.Errorf("read-only: %q", r.Text)
	}
	if len(snapshot(t, ro.Root)) != 0 {
		t.Error("read-only call wrote files")
	}
}

func TestApplyPatchRollbackReportsUnknownPaths(t *testing.T) {
	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("needs POSIX permissions as non-root")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "locked")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(p, []byte("current\n"), 0o444); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(p, 0o644)
	b := fileBackup{path: p, data: []byte("prior\n"), mode: 0o644, exists: true}
	got := rollbackFiles([]fileBackup{b})
	if len(got) != 1 || got[0] != p {
		t.Errorf("unknown = %v, want [%s]", got, p)
	}
}
