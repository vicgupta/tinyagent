package cwdpath

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRef(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	os.MkdirAll(filepath.Join(root, "skills", "ok"), 0o755)
	if got, err := Ref(root, "skills/ok"); err != nil || !strings.HasSuffix(got, filepath.Join("skills", "ok")) {
		t.Errorf("inside: %q %v", got, err)
	}
	if _, err := Ref(root, "skills/new"); err != nil {
		t.Errorf("missing but inside: %v", err)
	}
	for _, ref := range []string{"../x", "skills/../../x", "skills/..", filepath.Join(outside, "x"), "/etc/passwd"} {
		if _, err := Ref(root, ref); err == nil {
			t.Errorf("%q accepted", ref)
		}
	}
	if runtime.GOOS == "windows" {
		return
	}
	if err := os.Symlink(outside, filepath.Join(root, "skills", "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := Ref(root, "skills/escape"); err == nil || !strings.Contains(err.Error(), "outside") {
		t.Errorf("symlink escape: %v", err)
	}
	if err := os.Symlink(filepath.Join(root, "skills", "ok"), filepath.Join(root, "skills", "alias")); err != nil {
		t.Fatal(err)
	}
	if _, err := Ref(root, "skills/alias"); err != nil {
		t.Errorf("symlink inside: %v", err)
	}
}

func TestResolve(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if _, err := Resolve(root, outside); err == nil {
		t.Error("outside accepted")
	}
	if _, err := Resolve(root, filepath.Join(root, "a", "..", "..", "x")); err == nil {
		t.Error("cleaned escape accepted")
	}
	if _, err := Resolve(root, root); err != nil {
		t.Errorf("root itself: %v", err)
	}
}
