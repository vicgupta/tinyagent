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

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
}

func symlinkEscapeSetup(t *testing.T) (root, outside string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root, outside = filepath.Join(base, "root"), filepath.Join(base, "outside")
	for _, d := range []string{root, outside, filepath.Join(outside, "dir")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("SECRET\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	symlink(t, filepath.Join(outside, "secret.txt"), filepath.Join(root, "escape"))
	symlink(t, filepath.Join(outside, "dir"), filepath.Join(root, "escape_dir"))
	symlink(t, filepath.Join(outside, "missing.png"), filepath.Join(root, "escape.png"))
	return root, outside
}

func symlinkTool(e *Executor, name, args string) Result {
	return e.RunResult(context.Background(), model.ToolCall{ID: "c", Function: model.FunctionCall{Name: name, Arguments: args}})
}

func TestSymlinkEnforcementEscapes(t *testing.T) {
	root, outside := symlinkEscapeSetup(t)
	e := &Executor{Root: root, Images: true, ApplyPatch: true, UndoSession: "s", EnforceSymlinks: true}
	patch := "*** Begin Patch\n*** Update File: escape\n@@\n-SECRET\n+CHANGED\n*** End Patch"
	cases := []struct{ name, args string }{
		{"read_file", `{"path":"escape"}`},
		{"write_file", `{"path":"escape","content":"CHANGED\n"}`},
		{"edit_file", `{"path":"escape","old_str":"SECRET","new_str":"CHANGED"}`},
		{"apply_patch", `{"input":` + jsonString(patch) + `}`},
		{"grep", `{"pattern":"SECRET","path":"escape"}`},
		{"list_files", `{"path":"escape_dir"}`},
		{"undo_file", `{"path":"escape"}`},
		{"view_image", `{"path":"escape.png"}`},
	}
	for _, c := range cases {
		r := symlinkTool(e, c.name, c.args)
		if !r.IsError || !strings.Contains(r.Text, "escape") ||
			!strings.Contains(r.Text, "resolves to") ||
			!strings.Contains(r.Text, "use a path that resolves under the working directory or an additional directory") {
			t.Errorf("%s: %q", c.name, r.Text)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(outside, "secret.txt")); string(b) != "SECRET\n" {
		t.Errorf("outside file changed through a symlink: %q", b)
	}
}

func TestSymlinkEnforcementWalks(t *testing.T) {
	root, _ := symlinkEscapeSetup(t)
	e := &Executor{Root: root, EnforceSymlinks: true}
	if r := symlinkTool(e, "grep", `{"pattern":"SECRET"}`); r.IsError || strings.Contains(r.Text, "SECRET") {
		t.Errorf("grep read an escaping symlink: %q", r.Text)
	}
	if r := symlinkTool(e, "list_files", `{"pattern":"*"}`); r.IsError || strings.Contains(r.Text, "escape") {
		t.Errorf("list_files showed an escaping symlink: %q", r.Text)
	}
}

func TestSymlinkEnforcementInsideRoot(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "real", "data.txt"), []byte("original\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	symlink(t, filepath.Join(root, "real"), filepath.Join(root, "inside"))
	symlink(t, filepath.Join(root, "real", "data.txt"), filepath.Join(root, "alias.txt"))
	e := &Executor{Root: root, EnforceSymlinks: true}
	if r := symlinkTool(e, "read_file", `{"path":"inside/data.txt"}`); r.IsError || !strings.Contains(r.Text, "original") {
		t.Fatalf("read through an in-tree symlink: %q", r.Text)
	}
	if r := symlinkTool(e, "write_file", `{"path":"inside/new.txt","content":"new\n"}`); r.IsError {
		t.Fatalf("write through an in-tree symlink: %q", r.Text)
	}
	if r := symlinkTool(e, "write_file", `{"path":"alias.txt","content":"changed\n"}`); r.IsError {
		t.Fatalf("write through an in-tree file symlink: %q", r.Text)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "real", "data.txt")); string(b) != "changed\n" {
		t.Errorf("write through symlink = %q", b)
	}
}

func TestSymlinkEnforcementDanglingWrite(t *testing.T) {
	root, outside := symlinkEscapeSetup(t)
	symlink(t, filepath.Join(outside, "new.txt"), filepath.Join(root, "dangling"))
	e := &Executor{Root: root, EnforceSymlinks: true}
	if r := symlinkTool(e, "write_file", `{"path":"dangling","content":"x"}`); !r.IsError || !strings.Contains(r.Text, "resolves to") {
		t.Fatalf("dangling symlink write: %q", r.Text)
	}
	if _, err := os.Stat(filepath.Join(outside, "new.txt")); !os.IsNotExist(err) {
		t.Fatal("write created the dangling symlink's target outside the root")
	}
	legacy := &Executor{Root: root}
	if r := symlinkTool(legacy, "write_file", `{"path":"dangling","content":"x"}`); r.IsError {
		t.Fatalf("default behavior changed: %q", r.Text)
	}
	if b, _ := os.ReadFile(filepath.Join(outside, "new.txt")); string(b) != "x" {
		t.Errorf("default write through dangling symlink = %q", b)
	}
}

func TestSymlinkEnforcementAdditionalDirs(t *testing.T) {
	root, outside := symlinkEscapeSetup(t)
	readDir, writeDir := filepath.Join(outside, "read"), filepath.Join(outside, "write")
	for _, d := range []string{readDir, writeDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(readDir, "lib.txt"), []byte("lib\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	symlink(t, readDir, filepath.Join(root, "to-read"))
	symlink(t, writeDir, filepath.Join(root, "to-write"))
	dirs, err := ResolveExtraDirs(root, []ExtraDir{{Path: readDir, Mode: DirRead}, {Path: writeDir, Mode: DirWrite}}, false)
	if err != nil {
		t.Fatal(err)
	}
	e := &Executor{Root: root, ExtraDirs: dirs, EnforceSymlinks: true}
	if r := symlinkTool(e, "read_file", `{"path":"to-read/lib.txt"}`); r.IsError || !strings.Contains(r.Text, "lib") {
		t.Errorf("read through an allowed extra dir: %q", r.Text)
	}
	if r := symlinkTool(e, "write_file", `{"path":"to-read/new.txt","content":"x"}`); !r.IsError || !strings.Contains(r.Text, "mode read") {
		t.Errorf("write through a read-mode extra dir: %q", r.Text)
	}
	if r := symlinkTool(e, "write_file", `{"path":"to-write/new.txt","content":"x"}`); r.IsError {
		t.Errorf("write through a write-mode extra dir: %q", r.Text)
	}
	if b, _ := os.ReadFile(filepath.Join(writeDir, "new.txt")); string(b) != "x" {
		t.Errorf("extra dir write = %q", b)
	}
}

func jsonString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '\\', '"':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
