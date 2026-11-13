package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// faultFS is a patchFS that fails its failAt-th change (1-based; 0 never)
// and, after that, every change to a path in failPaths. A failing
// WriteFile first writes half the data, like a full disk.
type faultFS struct {
	osPatchFS
	failAt    int
	failPaths map[string]bool
	calls     int
	failed    bool
	touched   []string
	onFail    func() // runs once, at the injected failure
}

func (f *faultFS) step(name string) error {
	f.calls++
	f.touched = append(f.touched, name)
	if !f.failed && f.calls == f.failAt {
		f.failed = true
		if f.onFail != nil {
			f.onFail()
		}
		return fmt.Errorf("injected failure on %s", filepath.Base(name))
	}
	if f.failed && f.failPaths[name] {
		return fmt.Errorf("injected restore failure on %s", filepath.Base(name))
	}
	return nil
}

func (f *faultFS) WriteFile(name string, data []byte, perm os.FileMode) error {
	if err := f.step(name); err != nil {
		_ = os.WriteFile(name, data[:len(data)/2], perm)
		return err
	}
	return f.osPatchFS.WriteFile(name, data, perm)
}

func (f *faultFS) CreateFile(name string, data []byte, perm os.FileMode) error {
	if err := f.step(name); err != nil {
		return err
	}
	return f.osPatchFS.CreateFile(name, data, perm)
}

func (f *faultFS) Remove(name string) error {
	if err := f.step(name); err != nil {
		return err
	}
	return f.osPatchFS.Remove(name)
}

func (f *faultFS) MkdirAll(name string, perm os.FileMode) error {
	if err := f.step(name); err != nil {
		return err
	}
	return f.osPatchFS.MkdirAll(name, perm)
}

func (f *faultFS) Chmod(name string, mode os.FileMode) error {
	if err := f.step(name); err != nil {
		return err
	}
	return f.osPatchFS.Chmod(name, mode)
}

func (f *faultFS) Symlink(target, name string) error {
	if err := f.step(name); err != nil {
		return err
	}
	return f.osPatchFS.Symlink(target, name)
}

// entryState is everything rollback must restore about one tree entry.
type entryState struct {
	kind    string // "dir", "file" or "link"
	content string
	mode    os.FileMode
	link    string
}

// fullTree returns every entry under root (directories and symlinks
// included) and its state.
func fullTree(t *testing.T, root string) map[string]entryState {
	t.Helper()
	out := map[string]entryState{}
	err := filepath.Walk(root, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if p == root {
			return nil
		}
		name := rel(root, p)
		switch {
		case fi.Mode()&os.ModeSymlink != 0:
			l, err := os.Readlink(p)
			if err != nil {
				return err
			}
			out[name] = entryState{kind: "link", link: l}
		case fi.IsDir():
			out[name] = entryState{kind: "dir", mode: fi.Mode().Perm()}
		default:
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			out[name] = entryState{kind: "file", content: string(b), mode: fi.Mode().Perm()}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func diffTree(a, b map[string]entryState) []string {
	var out []string
	for k, v := range a {
		if w, ok := b[k]; !ok {
			out = append(out, "missing "+k)
		} else if w != v {
			out = append(out, fmt.Sprintf("%s: got %+v, want %+v", k, w, v))
		}
	}
	for k := range b {
		if _, ok := a[k]; !ok {
			out = append(out, "extra "+k)
		}
	}
	sort.Strings(out)
	return out
}

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func skipRollbackOnWindows(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("failure injection checks POSIX modes and symlinks")
	}
}

const mixedPatch = `*** Begin Patch
*** Add File: new/deep/a.txt
+added
*** Update File: u.txt
-old
+updated
*** Update File: src/s.txt
*** Move to: moved/to/d.txt
-src
+moved
*** Delete File: del/d.txt
*** Add File: z.txt
+last
*** End Patch`

func mixedTree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"u.txt":     "old\n",
		"src/s.txt": "src\n",
		"del/d.txt": "delete me\n",
		"keep.txt":  "untouched\n",
	})
	for name, mode := range map[string]os.FileMode{"u.txt": 0o640, "src/s.txt": 0o750, "del/d.txt": 0o600} {
		if err := os.Chmod(filepath.Join(root, name), mode); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// TestApplyPatchRollbackAtEveryWrite injects a failure at every change
// apply_patch makes for a patch mixing add, update, move and delete, and
// checks the tree is restored byte for byte, modes and directories included.
func TestApplyPatchRollbackAtEveryWrite(t *testing.T) {
	skipRollbackOnWindows(t)
	count := &faultFS{}
	out, _, err := applyPatchFilesFS(mixedTree(t), mixedPatch, nil, count)
	if err != nil {
		t.Fatal(err)
	}
	if want := "ok\nA new/deep/a.txt\nM u.txt (1 hunk)\nR src/s.txt -> moved/to/d.txt\nD del/d.txt\nA z.txt"; out != want {
		t.Fatalf("success output = %q, want %q", out, want)
	}
	if count.calls < 10 {
		t.Fatalf("only %d changes to inject failures into", count.calls)
	}
	for k := 1; k <= count.calls; k++ {
		t.Run(fmt.Sprintf("change %d", k), func(t *testing.T) {
			root := mixedTree(t)
			snapDir := filepath.Join(t.TempDir(), "undo", "s")
			sc := &scope{snapDir: snapDir}
			before := fullTree(t, root)
			var snapsAtFailure []string
			f := &faultFS{failAt: k}
			f.onFail = func() { snapsAtFailure = listDir(t, snapDir) }
			_, written, err := applyPatchFilesFS(root, mixedPatch, sc, f)
			if err == nil {
				t.Fatal("patch succeeded, want the injected failure")
			}
			msg := err.Error()
			if !strings.Contains(msg, "failed: injected failure") ||
				!strings.Contains(msg, "every file this patch touched is as it was before the call") ||
				strings.Contains(msg, "unknown state") {
				t.Errorf("err = %v", err)
			}
			if written != nil {
				t.Errorf("written = %v, want none", written)
			}
			if d := diffTree(fullTree(t, root), before); len(d) > 0 {
				t.Errorf("tree after rollback differs:\n%s", strings.Join(d, "\n"))
			}
			// Rollback takes no new undo snapshot.
			if got := listDir(t, snapDir); strings.Join(got, ",") != strings.Join(snapsAtFailure, ",") {
				t.Errorf("snapshots after rollback = %v, at failure %v", got, snapsAtFailure)
			}
			// Every change, forward or rollback, is to a patch path or a
			// directory created for one (MkdirAll of root, which exists,
			// changes nothing).
			allowed := map[string]bool{root: true}
			for _, p := range []string{"new/deep/a.txt", "new/deep", "new", "u.txt", "src/s.txt", "moved/to/d.txt", "moved/to", "moved", "del/d.txt", "z.txt"} {
				allowed[filepath.Join(root, filepath.FromSlash(p))] = true
			}
			for _, p := range f.touched {
				if !allowed[p] {
					t.Errorf("changed %s, outside the patch", p)
				}
			}
		})
	}
}

// TestApplyPatchRollbackFailureNamesUnknownPaths makes some restores fail
// and checks the error names exactly those paths while the rest are
// restored.
func TestApplyPatchRollbackFailureNamesUnknownPaths(t *testing.T) {
	skipRollbackOnWindows(t)
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"a.txt": "a\n", "b.txt": "b\n", "c.txt": "c\n"})
	patch := "*** Begin Patch\n*** Add File: new/x.txt\n+x\n*** Update File: a.txt\n-a\n+A\n*** Update File: b.txt\n-b\n+B\n*** Delete File: c.txt\n*** End Patch"
	count := &faultFS{}
	if _, _, err := applyPatchFilesFS(root, patch, nil, count); err != nil {
		t.Fatal(err)
	}

	root = t.TempDir()
	writeFiles(t, root, map[string]string{"a.txt": "a\n", "b.txt": "b\n", "c.txt": "c\n"})
	f := &faultFS{failAt: count.calls, failPaths: map[string]bool{
		filepath.Join(root, "a.txt"): true,
		filepath.Join(root, "new"):   true,
	}}
	_, _, err := applyPatchFilesFS(root, patch, nil, f)
	if err == nil {
		t.Fatal("patch succeeded, want the injected failure")
	}
	msg := err.Error()
	for _, want := range []string{
		"operation 4 (delete c.txt) failed: injected failure on c.txt",
		"\nrollback incomplete; unknown state: a.txt, new\n",
		"  a.txt: injected restore failure on a.txt",
		"  new: created directory not removed: injected restore failure on new",
		"not applied: D c.txt",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("err missing %q:\n%s", want, msg)
		}
	}
	if strings.Contains(msg, "as it was before the call") {
		t.Errorf("err claims a full rollback:\n%s", msg)
	}
	for name, want := range map[string]string{"b.txt": "b\n", "c.txt": "c\n"} {
		if b, _ := os.ReadFile(filepath.Join(root, name)); string(b) != want {
			t.Errorf("%s = %q, want %q", name, b, want)
		}
	}
	if _, err := os.Lstat(filepath.Join(root, "new", "x.txt")); !os.IsNotExist(err) {
		t.Errorf("added file not removed: %v", err)
	}
}

// TestApplyPatchRollbackSymlinks checks a deleted symlink comes back as the
// same symlink and a file updated through a symlink gets its bytes back,
// with symlink containment off and on.
func TestApplyPatchRollbackSymlinks(t *testing.T) {
	skipRollbackOnWindows(t)
	setup := func(t *testing.T) string {
		root := t.TempDir()
		writeFiles(t, root, map[string]string{"real.txt": "real\n", "other.txt": "other\n"})
		for link, target := range map[string]string{"link.txt": "real.txt", "dlink.txt": "other.txt"} {
			if err := os.Symlink(target, filepath.Join(root, link)); err != nil {
				t.Fatal(err)
			}
		}
		return root
	}
	patch := "*** Begin Patch\n*** Update File: link.txt\n-real\n+changed\n*** Delete File: dlink.txt\n*** Add File: z.txt\n+z\n*** End Patch"
	for _, enforce := range []bool{false, true} {
		t.Run(fmt.Sprintf("enforce=%v", enforce), func(t *testing.T) {
			sc := func(root string) *scope {
				return &scope{enforceSymlinks: enforce, root: root, realRoot: root}
			}
			root := setup(t)
			count := &faultFS{}
			if _, _, err := applyPatchFilesFS(root, patch, sc(root), count); err != nil {
				t.Fatal(err)
			}
			root = setup(t)
			before := fullTree(t, root)
			f := &faultFS{failAt: count.calls}
			_, _, err := applyPatchFilesFS(root, patch, sc(root), f)
			if err == nil || !strings.Contains(err.Error(), "rolled back: M link.txt (1 hunk), D dlink.txt;") {
				t.Fatalf("err = %v", err)
			}
			if d := diffTree(fullTree(t, root), before); len(d) > 0 {
				t.Errorf("tree after rollback differs:\n%s", strings.Join(d, "\n"))
			}
		})
	}
}

// TestApplyPatchRollbackLeavesReplacedPathAlone checks rollback does not
// overwrite a path that is no longer the kind of entry it backed up.
func TestApplyPatchRollbackLeavesReplacedPathAlone(t *testing.T) {
	skipRollbackOnWindows(t)
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"a.txt": "a\n", "b.txt": "b\n"})
	patch := "*** Begin Patch\n*** Delete File: a.txt\n*** Update File: b.txt\n-b\n+B\n*** End Patch"
	f := &faultFS{failAt: 2}
	// Something else puts a directory where the deleted file was.
	f.onFail = func() { _ = os.Mkdir(filepath.Join(root, "a.txt"), 0o755) }
	_, _, err := applyPatchFilesFS(root, patch, nil, f)
	if err == nil || !strings.Contains(err.Error(), "unknown state: a.txt\n") {
		t.Fatalf("err = %v", err)
	}
	if fi, err := os.Lstat(filepath.Join(root, "a.txt")); err != nil || !fi.IsDir() {
		t.Errorf("a.txt was changed by rollback: %v %v", fi, err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "b.txt")); string(b) != "b\n" {
		t.Errorf("b.txt = %q", b)
	}
}
