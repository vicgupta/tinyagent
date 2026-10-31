package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// dirsSetup returns a canonical root plus a read and a write directory
// outside it, each holding a file.
func dirsSetup(t *testing.T) (root, readDir, writeDir string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root, readDir, writeDir = filepath.Join(base, "root"), filepath.Join(base, "ref"), filepath.Join(base, "out")
	for _, d := range []string{root, readDir, writeDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o644)
	os.WriteFile(filepath.Join(readDir, "lib.go"), []byte("ref sentinel\n"), 0o644)
	os.WriteFile(filepath.Join(writeDir, "gen.txt"), []byte("old\n"), 0o644)
	return root, readDir, writeDir
}

func dirsExecutor(t *testing.T, root, readDir, writeDir string) *Executor {
	t.Helper()
	dirs, err := ResolveExtraDirs(root, []ExtraDir{{Path: readDir, Mode: DirRead}, {Path: writeDir, Mode: DirWrite}}, false)
	if err != nil {
		t.Fatal(err)
	}
	return &Executor{Root: root, ExtraDirs: dirs, ApplyPatch: true}
}

func dirTool(e *Executor, name string, args map[string]any) (string, bool) {
	r := e.RunResult(context.Background(), toolCall("c", name, args))
	return r.Text, r.IsError
}

func TestResolveExtraDirsValidation(t *testing.T) {
	root, readDir, writeDir := dirsSetup(t)
	file := filepath.Join(readDir, "lib.go")
	cases := []struct {
		name string
		in   []ExtraDir
		want string
	}{
		{"missing", []ExtraDir{{Path: filepath.Join(readDir, "nope"), Mode: DirRead}}, "does not exist"},
		{"file", []ExtraDir{{Path: file, Mode: DirRead}}, "not a directory"},
		{"fs root", []ExtraDir{{Path: "/", Mode: DirRead}}, "filesystem root"},
		{"equals cwd", []ExtraDir{{Path: root, Mode: DirRead}}, "is the working directory"},
		{"inside cwd", []ExtraDir{{Path: mkdir(t, filepath.Join(root, "sub")), Mode: DirRead}}, "inside the working directory"},
		{"contains cwd", []ExtraDir{{Path: filepath.Dir(root), Mode: DirRead}}, "contains the working directory"},
		{"duplicate", []ExtraDir{{Path: readDir, Mode: DirRead}, {Path: readDir, Mode: DirWrite}}, "listed twice"},
		{"overlap", []ExtraDir{{Path: writeDir, Mode: DirWrite}, {Path: mkdir(t, filepath.Join(writeDir, "x")), Mode: DirRead}}, "overlaps"},
		{"bad mode", []ExtraDir{{Path: readDir, Mode: "rw"}}, "must be read or write"},
		{"no mode", []ExtraDir{{Path: readDir}}, "mode is required"},
	}
	for _, c := range cases {
		if _, err := ResolveExtraDirs(root, c.in, false); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
	var many []ExtraDir
	for i := 0; i <= MaxExtraDirs; i++ {
		many = append(many, ExtraDir{Path: mkdir(t, filepath.Join(writeDir, "m", string(rune('a'+i)))), Mode: DirRead})
	}
	if _, err := ResolveExtraDirs(root, many, false); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Errorf("cap: err = %v", err)
	}
	// Relative paths are taken from root; symlinks resolve; read-only forces read.
	link := filepath.Join(filepath.Dir(root), "link")
	if err := os.Symlink(writeDir, link); err != nil {
		t.Fatal(err)
	}
	got, err := ResolveExtraDirs(root, []ExtraDir{{Path: "../link", Mode: DirWrite}}, true)
	if err != nil || len(got) != 1 || got[0].Path != writeDir || got[0].Mode != DirRead {
		t.Errorf("relative/symlink/read-only: %v %v", got, err)
	}
	if got, err := ResolveExtraDirs(root, nil, false); got != nil || err != nil {
		t.Errorf("empty: %v %v", got, err)
	}
}

func mkdir(t *testing.T, p string) string {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestExtraDirsReadAndWrite(t *testing.T) {
	root, readDir, writeDir := dirsSetup(t)
	e := dirsExecutor(t, root, readDir, writeDir)
	if out, isErr := dirTool(e, "read_file", map[string]any{"path": filepath.Join(readDir, "lib.go")}); isErr || !strings.Contains(out, "ref sentinel") {
		t.Errorf("read in read dir: %q", out)
	}
	if out, isErr := dirTool(e, "list_files", map[string]any{"path": readDir}); isErr || out != "lib.go" {
		t.Errorf("list_files in read dir: %q", out)
	}
	if out, isErr := dirTool(e, "list_files", map[string]any{"path": readDir, "pattern": "*.go"}); isErr || !strings.Contains(out, filepath.Join(readDir, "lib.go")) {
		t.Errorf("list_files pattern in read dir: %q", out)
	}
	if out, isErr := dirTool(e, "grep", map[string]any{"pattern": "sentinel", "path": readDir}); isErr || !strings.Contains(out, filepath.Join(readDir, "lib.go")) {
		t.Errorf("grep in read dir: %q", out)
	}
	target := filepath.Join(writeDir, "new", "f.txt")
	if out, isErr := dirTool(e, "write_file", map[string]any{"path": target, "content": "hi\n"}); isErr {
		t.Errorf("write in write dir: %q", out)
	}
	if b, _ := os.ReadFile(target); string(b) != "hi\n" {
		t.Errorf("written = %q", b)
	}
	if out, isErr := dirTool(e, "edit_file", map[string]any{"path": filepath.Join(writeDir, "gen.txt"), "old_str": "old", "new_str": "new"}); isErr {
		t.Errorf("edit in write dir: %q", out)
	}
	// Relative paths still resolve against root only.
	if out, isErr := dirTool(e, "read_file", map[string]any{"path": "main.go"}); isErr || !strings.Contains(out, "package main") {
		t.Errorf("relative read: %q", out)
	}
	if _, isErr := dirTool(e, "read_file", map[string]any{"path": "../ref/lib.go"}); !isErr {
		t.Error("relative path escaping root was allowed")
	}
	// Paths outside root and every additional directory are refused.
	other := mkdir(t, filepath.Join(filepath.Dir(root), "other"))
	os.WriteFile(filepath.Join(other, "x"), []byte("x"), 0o644)
	if out, isErr := dirTool(e, "read_file", map[string]any{"path": filepath.Join(other, "x")}); !isErr || !strings.Contains(out, "outside the working directory") {
		t.Errorf("outside read: %q", out)
	}
}

func TestExtraDirsReadModeRefusesWrites(t *testing.T) {
	root, readDir, writeDir := dirsSetup(t)
	e := dirsExecutor(t, root, readDir, writeDir)
	lib := filepath.Join(readDir, "lib.go")
	for _, c := range []struct {
		name string
		args map[string]any
	}{
		{"write_file", map[string]any{"path": lib, "content": "x"}},
		{"write_file", map[string]any{"path": filepath.Join(readDir, "new.txt"), "content": "x"}},
		{"edit_file", map[string]any{"path": lib, "old_str": "ref", "new_str": "x"}},
	} {
		out, isErr := dirTool(e, c.name, c.args)
		if !isErr || !strings.Contains(out, readDir) || !strings.Contains(out, "mode read") {
			t.Errorf("%s %v: %q", c.name, c.args["path"], out)
		}
	}
	// apply_patch is atomic: a read-dir path refuses the whole patch.
	patch := "*** Begin Patch\n*** Add File: " + filepath.Join(writeDir, "p.txt") + "\n+x\n*** Update File: " + lib + "\n@@\n-ref sentinel\n+changed\n*** End Patch"
	if out, isErr := dirTool(e, "apply_patch", map[string]any{"input": patch}); !isErr || !strings.Contains(out, "mode read") {
		t.Errorf("apply_patch: %q", out)
	}
	if _, err := os.Stat(filepath.Join(writeDir, "p.txt")); err == nil {
		t.Error("apply_patch wrote part of a refused patch")
	}
	if b, _ := os.ReadFile(lib); string(b) != "ref sentinel\n" {
		t.Errorf("read dir file changed: %q", b)
	}
	// A symlink in the write dir pointing into the read dir is refused too.
	if err := os.Symlink(lib, filepath.Join(writeDir, "sneaky")); err != nil {
		t.Fatal(err)
	}
	if out, isErr := dirTool(e, "write_file", map[string]any{"path": filepath.Join(writeDir, "sneaky"), "content": "x"}); !isErr || !strings.Contains(out, "mode read") {
		t.Errorf("symlink write: %q", out)
	}
}

func TestExtraDirsReadOnlyRun(t *testing.T) {
	root, readDir, writeDir := dirsSetup(t)
	dirs, err := ResolveExtraDirs(root, []ExtraDir{{Path: writeDir, Mode: DirWrite}}, true)
	if err != nil {
		t.Fatal(err)
	}
	e := &Executor{Root: root, ExtraDirs: dirs, ReadOnly: true}
	if out, isErr := dirTool(e, "read_file", map[string]any{"path": filepath.Join(writeDir, "gen.txt")}); isErr || !strings.Contains(out, "old") {
		t.Errorf("read-only read: %q", out)
	}
	if _, isErr := dirTool(e, "write_file", map[string]any{"path": filepath.Join(writeDir, "gen.txt"), "content": "x"}); !isErr {
		t.Error("read-only run wrote to an additional directory")
	}
	// The scope refuses even if the tool itself were reachable.
	sc := &scope{extra: []ExtraDir{{Path: writeDir, Mode: DirWrite}}, readOnly: true}
	if err := sc.checkWrite(filepath.Join(writeDir, "gen.txt"), "gen.txt"); err == nil {
		t.Error("read-only scope allowed a write")
	}
	_ = readDir
}

func TestExtraDirsFileAccess(t *testing.T) {
	root, readDir, writeDir := dirsSetup(t)
	os.WriteFile(filepath.Join(readDir, ".env"), []byte("TOKEN=1\n"), 0o644)
	os.WriteFile(filepath.Join(writeDir, "go.sum"), []byte("sum\n"), 0o644)
	fa, err := NewFileAccess(root, []string{".env"}, []string{"/go.sum"})
	if err != nil {
		t.Fatal(err)
	}
	e := dirsExecutor(t, root, readDir, writeDir)
	fa.AddDirs(e.ExtraDirs)
	e.FileAccess = fa
	if out, isErr := dirTool(e, "read_file", map[string]any{"path": filepath.Join(readDir, ".env")}); !isErr || !strings.Contains(out, "deny_read") {
		t.Errorf("deny_read in additional dir: %q", out)
	}
	if out, _ := dirTool(e, "grep", map[string]any{"pattern": "TOKEN", "path": readDir}); strings.Contains(out, "TOKEN=1") {
		t.Errorf("grep revealed a denied file: %q", out)
	}
	if out, isErr := dirTool(e, "write_file", map[string]any{"path": filepath.Join(writeDir, "go.sum"), "content": "x"}); !isErr || !strings.Contains(out, "deny_write") {
		t.Errorf("anchored deny_write in additional dir: %q", out)
	}
}

func TestExtraDirsNotice(t *testing.T) {
	if ExtraDirsNotice(nil) != "" {
		t.Error("notice without dirs")
	}
	n := ExtraDirsNotice([]ExtraDir{{Path: "/a", Mode: DirRead}, {Path: "/b", Mode: DirWrite}})
	if !strings.Contains(n, "/a (read)") || !strings.Contains(n, "/b (write)") {
		t.Errorf("notice = %q", n)
	}
	base := "system prompt\n\nskills"
	if got := StripExtraDirsNotice(base + n + "\n\nread-only"); got != base+"\n\nread-only" {
		t.Errorf("strip = %q", got)
	}
	if got := StripExtraDirsNotice(base); got != base {
		t.Errorf("strip without notice = %q", got)
	}
}

func TestContainerRunArgsMountsExtraDirs(t *testing.T) {
	args := strings.Join(containerRunArgs("img", []string{"--network=none"}, "/w", []ExtraDir{{Path: "/ref", Mode: DirRead}, {Path: "/out", Mode: DirWrite}}), " ")
	for _, want := range []string{"-v /w:/w -v /ref:/ref:ro -v /out:/out -w /w", "--network=none img sleep infinity"} {
		if !strings.Contains(args, want) {
			t.Errorf("args %q missing %q", args, want)
		}
	}
	if args := strings.Join(containerRunArgs("img", nil, "/w", nil), " "); strings.Count(args, "-v ") != 1 {
		t.Errorf("without extra dirs: %q", args)
	}
}
