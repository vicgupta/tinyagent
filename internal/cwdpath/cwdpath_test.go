package cwdpath

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestResolve(t *testing.T) {
	cwd := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(cwd, "a"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"a/b.toml", "new/dir/x", "./c", filepath.Join(cwd, "a", "b")} {
		got, err := Resolve(cwd, p)
		if err != nil || !Within(got, cwd) {
			t.Errorf("Resolve(%q) = %q, %v; want a path inside cwd", p, got, err)
		}
	}
	for _, p := range []string{"", ".", "..", "../x", "a/../../x", "~/x", filepath.Join(outside, "x")} {
		if got, err := Resolve(cwd, p); !errors.Is(err, ErrOutside) {
			t.Errorf("Resolve(%q) = %q, %v; want ErrOutside", p, got, err)
		}
	}
	if runtime.GOOS == "windows" {
		return
	}
	if err := os.Symlink(outside, filepath.Join(cwd, "link")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"link", "link/x/y", filepath.Join(cwd, "link", "z")} {
		if got, err := Resolve(cwd, p); !errors.Is(err, ErrOutside) {
			t.Errorf("Resolve(%q) through an outside symlink = %q, %v; want ErrOutside", p, got, err)
		}
	}
	if err := os.Symlink(filepath.Join(cwd, "a"), filepath.Join(cwd, "inside")); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(cwd, "inside/x"); err != nil {
		t.Errorf("symlink inside cwd refused: %v", err)
	}
}

func TestRef(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "w.yaml"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Ref(root, "w.yaml")
	if err != nil || filepath.Base(got) != "w.yaml" {
		t.Fatalf("Ref(w.yaml) = %q, %v", got, err)
	}
	for _, bad := range []string{filepath.Join(root, "w.yaml"), "/etc/passwd", "../x.yaml", "a/../../x.yaml"} {
		if _, err := Ref(root, bad); !errors.Is(err, ErrOutside) {
			t.Errorf("Ref(%q) err = %v, want ErrOutside", bad, err)
		}
	}
}
