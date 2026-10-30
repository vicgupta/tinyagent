package tools

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/model"
)

func undoCall(e *Executor, name, args string) (string, bool) {
	return e.RunWithError(context.Background(), model.ToolCall{ID: "c", Type: "function", Function: model.FunctionCall{Name: name, Arguments: args}})
}

func snapFile(t *testing.T, e *Executor, path string) string {
	t.Helper()
	return filepath.Join(e.Root, filepath.FromSlash(UndoDir), e.UndoSession, undoKey(filepath.Join(e.Root, path)))
}

func TestUndoSnapshotBeforeFirstChange(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := &Executor{Root: root, UndoSession: "s1"}
	if out, isErr := undoCall(e, "edit_file", `{"path":"a.txt","old_str":"original","new_str":"first"}`); isErr {
		t.Fatal(out)
	}
	snap := snapFile(t, e, "a.txt")
	if b, err := os.ReadFile(snap); err != nil || string(b) != "original\n" {
		t.Fatalf("snapshot = %q, %v", b, err)
	}
	if out, isErr := undoCall(e, "write_file", `{"path":"a.txt","content":"second\n"}`); isErr {
		t.Fatal(out)
	}
	if b, _ := os.ReadFile(snap); string(b) != "original\n" {
		t.Errorf("second change overwrote snapshot: %q", b)
	}
	if b, _ := os.ReadFile(filepath.Join(root, UndoDir, ".gitignore")); string(b) != "*\n" {
		t.Errorf("undo .gitignore = %q", b)
	}
	out, isErr := undoCall(e, UndoToolName, `{"path":"a.txt"}`)
	if isErr || !strings.HasPrefix(out, "ok") {
		t.Fatal(out)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(b) != "original\n" {
		t.Errorf("after undo = %q", b)
	}
}

func TestUndoWriteFileSnapshotsOriginal(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "w.txt"), []byte("v1"), 0o600)
	e := &Executor{Root: root, UndoSession: "s"}
	if out, isErr := undoCall(e, "write_file", `{"path":"w.txt","content":"v2"}`); isErr {
		t.Fatal(out)
	}
	if b, _ := os.ReadFile(snapFile(t, e, "w.txt")); string(b) != "v1" {
		t.Errorf("snapshot = %q", b)
	}
	if out, isErr := undoCall(e, UndoToolName, `{"path":"w.txt"}`); isErr {
		t.Fatal(out)
	}
	fi, _ := os.Stat(filepath.Join(root, "w.txt"))
	if b, _ := os.ReadFile(filepath.Join(root, "w.txt")); string(b) != "v1" || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600) {
		t.Errorf("after undo = %q %v", b, fi.Mode())
	}
}

func TestUndoNewFileRemovesIt(t *testing.T) {
	root := t.TempDir()
	e := &Executor{Root: root, UndoSession: "s"}
	if out, isErr := undoCall(e, "write_file", `{"path":"dir/new.txt","content":"x"}`); isErr {
		t.Fatal(out)
	}
	undoCall(e, "write_file", `{"path":"dir/new.txt","content":"y"}`)
	if out, isErr := undoCall(e, UndoToolName, `{"path":"dir/new.txt"}`); isErr || !strings.Contains(out, "removed") {
		t.Fatal(out)
	}
	if _, err := os.Stat(filepath.Join(root, "dir", "new.txt")); !os.IsNotExist(err) {
		t.Errorf("new file still exists: %v", err)
	}
}

func TestUndoApplyPatch(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "p.txt"), []byte("one\n"), 0o644)
	e := &Executor{Root: root, ApplyPatch: true, UndoSession: "s"}
	if out, isErr := undoCall(e, ApplyPatchToolName, `{"input":"*** Begin Patch\n*** Update File: p.txt\n-one\n+ONE\n*** End Patch"}`); isErr {
		t.Fatal(out)
	}
	if out, isErr := undoCall(e, UndoToolName, `{"path":"p.txt"}`); isErr {
		t.Fatal(out)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "p.txt")); string(b) != "one\n" {
		t.Errorf("after undo = %q", b)
	}
}

func TestUndoNoSnapshot(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.txt"), []byte("x"), 0o644)
	e := &Executor{Root: root, UndoSession: "s"}
	out, isErr := undoCall(e, UndoToolName, `{"path":"a.txt"}`)
	if !isErr || !strings.Contains(out, "no undo snapshot for a.txt") {
		t.Errorf("out = %q", out)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(b) != "x" {
		t.Errorf("file changed: %q", b)
	}
	if _, isErr := undoCall(e, UndoToolName, `{}`); !isErr {
		t.Error("missing path accepted")
	}
}

func TestUndoPathChecks(t *testing.T) {
	root := t.TempDir()
	e := &Executor{Root: root, UndoSession: "s"}
	if out, isErr := undoCall(e, UndoToolName, `{"path":"../x"}`); !isErr {
		t.Errorf("escape accepted: %q", out)
	}
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "t.txt"), []byte("o"), 0o644)
	if err := os.Symlink(filepath.Join(outside, "t.txt"), filepath.Join(root, "link")); err == nil {
		if out, isErr := undoCall(e, UndoToolName, `{"path":"link"}`); !isErr {
			t.Errorf("symlink escape accepted: %q", out)
		}
	}
	fa, err := NewFileAccess(root, nil, []string{"secret.txt"})
	if err != nil {
		t.Fatal(err)
	}
	e.FileAccess = fa
	if out, isErr := undoCall(e, UndoToolName, `{"path":"secret.txt"}`); !isErr || !strings.Contains(out, "secret.txt") {
		t.Errorf("deny_write not applied: %q", out)
	}
	ro := &Executor{Root: root, ReadOnly: true}
	if out, isErr := undoCall(ro, UndoToolName, `{"path":"a.txt"}`); !isErr {
		t.Errorf("read-only accepted undo: %q", out)
	}
	if names := specNames(ro.Catalog()); names[UndoToolName] {
		t.Error("undo_file offered in read-only mode")
	}
	if names := specNames(e.Catalog()); !names[UndoToolName] {
		t.Error("undo_file missing from catalog")
	}
}
