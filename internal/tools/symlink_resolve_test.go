package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A relative link target is resolved the way the OS does it: a ".." after
// a symlinked component steps out of that symlink's real target, not out
// of its lexical location. root/link -> "hop/../x" with root/hop pointing
// outside lands outside, though the target names an in-root path lexically.
func TestSymlinkEnforcementDotDotAfterSymlink(t *testing.T) {
	root, outside := symlinkEscapeSetup(t)
	deep := filepath.Join(outside, "a", "b")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	symlink(t, deep, filepath.Join(root, "hop"))
	symlink(t, "hop/../x", filepath.Join(root, "link"))
	e := &Executor{Root: root, EnforceSymlinks: true}
	if r := symlinkTool(e, "write_file", `{"path":"link","content":"x"}`); !r.IsError || !strings.Contains(r.Text, "resolves to") {
		t.Errorf("write through hop/../x: %q", r.Text)
	}
	if _, err := os.Stat(filepath.Join(outside, "a", "x")); !os.IsNotExist(err) {
		t.Fatal("write escaped to outside/a/x")
	}
	// The same chain to an existing file is refused for reads too.
	if err := os.WriteFile(filepath.Join(outside, "a", "y"), []byte("SECRET\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	symlink(t, "hop/../y", filepath.Join(root, "link2"))
	if r := symlinkTool(e, "read_file", `{"path":"link2"}`); !r.IsError || strings.Contains(r.Text, "SECRET") {
		t.Errorf("read through hop/../y: %q", r.Text)
	}
}

// A new file under a symlinked directory chain is checked through every
// link, including a relative link inside the outside tree.
func TestSymlinkEnforcementIntermediateDirs(t *testing.T) {
	root, outside := symlinkEscapeSetup(t)
	if err := os.MkdirAll(filepath.Join(root, "in"), 0o755); err != nil {
		t.Fatal(err)
	}
	symlink(t, "../in", filepath.Join(root, "in", "self"))
	symlink(t, filepath.Join(outside, "dir"), filepath.Join(root, "in", "out"))
	e := &Executor{Root: root, EnforceSymlinks: true}
	if r := symlinkTool(e, "write_file", `{"path":"in/self/new/file.txt","content":"ok"}`); r.IsError {
		t.Errorf("write under an in-root link chain: %q", r.Text)
	}
	if r := symlinkTool(e, "write_file", `{"path":"in/self/out/new/file.txt","content":"x"}`); !r.IsError || !strings.Contains(r.Text, "resolves to") {
		t.Errorf("write under in/self/out: %q", r.Text)
	}
	if _, err := os.Stat(filepath.Join(outside, "dir", "new")); !os.IsNotExist(err) {
		t.Fatal("write created a directory outside the root")
	}
}

// A symlink loop is refused with an actionable error, not followed.
func TestSymlinkEnforcementLoop(t *testing.T) {
	root, _ := symlinkEscapeSetup(t)
	symlink(t, "loop2", filepath.Join(root, "loop1"))
	symlink(t, "loop1", filepath.Join(root, "loop2"))
	e := &Executor{Root: root, EnforceSymlinks: true}
	if r := symlinkTool(e, "write_file", `{"path":"loop1","content":"x"}`); !r.IsError {
		t.Errorf("write through a symlink loop: %q", r.Text)
	}
}
