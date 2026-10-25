package tools

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func mustAccess(t *testing.T, root string, read, write []string) *FileAccess {
	t.Helper()
	fa, err := NewFileAccess(root, read, write)
	if err != nil {
		t.Fatal(err)
	}
	return fa
}

func TestFileAccessNilWhenEmpty(t *testing.T) {
	fa, err := NewFileAccess(t.TempDir(), nil, nil)
	if err != nil || fa != nil {
		t.Fatalf("got %v, %v", fa, err)
	}
	if fa.CheckRead("/x", "x") != nil || fa.CheckWrite("/x", "x") != nil || fa.Hidden("/x") {
		t.Error("nil policy denied something")
	}
}

func TestFileAccessPatternErrors(t *testing.T) {
	for _, p := range []string{"", "  ", "!x", "/", "a/../b", "[", "a//b"} {
		if err := ValidateAccessPattern(p); err == nil {
			t.Errorf("%q accepted", p)
		}
		if _, err := NewFileAccess(t.TempDir(), []string{p}, nil); err == nil || !strings.Contains(err.Error(), "file_access.deny_read") {
			t.Errorf("%q: %v", p, err)
		}
	}
	for _, p := range []string{".env", "*.pem", "/build", "secrets/", "**/id_rsa", "a/**/b", "k?y[0-9]"} {
		if err := ValidateAccessPattern(p); err != nil {
			t.Errorf("%q: %v", p, err)
		}
	}
}

func TestFileAccessMatching(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{
		"build/out.txt": "", "src/build/x.go": "", "secrets/k": "", "src/secrets": "",
		"a/x/y/b": "", "a/b": "", "deep/z/id_rsa": "", "key1": "", ".env": "", "src/.env": "",
	})
	cases := []struct {
		pat  string
		path string
		want bool
	}{
		{".env", ".env", true},
		{".env", "src/.env", true},
		{".env", "src/.env.example", false},
		{"*.pem", "certs/a.pem", true},
		{"/build", "build/out.txt", true},
		{"/build", "src/build/x.go", false},
		{"build", "src/build/x.go", true},
		{"secrets/", "secrets/k", true},
		{"secrets/", "src/secrets", false}, // a file, and the rule is dirs only
		{"secrets/", "secrets", true},
		{"a/**/b", "a/b", true},
		{"a/**/b", "a/x/y/b", true},
		{"a/**/b", "c/a/b", false},
		{"**/id_rsa", "deep/z/id_rsa", true},
		{"**/id_rsa", "id_rsa", true},
		{"k?y[0-9]", "key1", true},
		{"k?y[0-9]", "keyz", false},
		{"src/*.go", "src/build/x.go", false},
		{"src/**", "src/build/x.go", true},
	}
	for _, c := range cases {
		fa := mustAccess(t, root, []string{c.pat}, nil)
		if got := fa.Hidden(filepath.Join(root, filepath.FromSlash(c.path))); got != c.want {
			t.Errorf("%q vs %q: got %v, want %v", c.pat, c.path, got, c.want)
		}
	}
}

func TestFileAccessCaseFolding(t *testing.T) {
	root := t.TempDir()
	fa := mustAccess(t, root, []string{".ENV"}, nil)
	got := fa.Hidden(filepath.Join(root, ".env"))
	if want := runtime.GOOS == "darwin" || runtime.GOOS == "windows"; got != want {
		t.Errorf("case-folded match = %v on %s", got, runtime.GOOS)
	}
}

func TestFileAccessReadTools(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{".env": "TOKEN=sentinel-123\n", "secrets/a.txt": "sentinel-123\n", "ok.txt": "fine sentinel-123\n", "src/.env": "sentinel-123\n"})
	fa := mustAccess(t, root, []string{".env", "secrets/"}, nil)

	for _, p := range []string{".env", "src/.env", "secrets/a.txt", "./secrets/../.env"} {
		_, err := readFile(root, p, 0, 0, faScope(fa))
		var de *DeniedError
		if !errors.As(err, &de) || de.List != "deny_read" || !strings.Contains(err.Error(), "file_access.deny_read rule") {
			t.Errorf("read %s: %v", p, err)
		}
	}
	if out, err := readFile(root, "ok.txt", 0, 0, faScope(fa)); err != nil || !strings.Contains(out, "fine") {
		t.Errorf("read ok.txt: %q %v", out, err)
	}
	if _, _, err := viewImage(root, "secrets/a.txt", 1<<20, faScope(fa)); err == nil || !strings.Contains(err.Error(), "deny_read") {
		t.Errorf("view_image: %v", err)
	}

	out, err := listFiles(root, "", "", "", faScope(fa))
	if err != nil || strings.Contains(out, ".env") || strings.Contains(out, "secrets") || !strings.Contains(out, "ok.txt") || !strings.HasSuffix(out, "(2 paths hidden by file_access rules)") {
		t.Errorf("list: %q %v", out, err)
	}
	out, err = listFiles(root, "", "*", "", faScope(fa))
	if err != nil || strings.Contains(out, ".env") || strings.Contains(out, "a.txt") || !strings.Contains(out, "ok.txt") || !strings.Contains(out, "paths hidden by file_access rules") {
		t.Errorf("glob: %q %v", out, err)
	}
	out, err = listFiles(root, "", "*.go", "", faScope(fa))
	if err != nil || out != "no matches\n(1 path hidden by file_access rules)" {
		t.Errorf("glob with no matching hidden files counts only the denied dir: %q %v", out, err)
	}
	if _, err := listFiles(root, "secrets", "", "", faScope(fa)); err == nil || !strings.Contains(err.Error(), "deny_read") {
		t.Errorf("list denied dir: %v", err)
	}

	out, err = grepWithContext(root, "sentinel", "", "", false, false, 0, "", faScope(fa))
	if err != nil || strings.Contains(out, ".env") || strings.Contains(out, "a.txt") || !strings.Contains(out, "ok.txt") || !strings.Contains(out, "paths hidden by file_access rules") {
		t.Errorf("grep: %q %v", out, err)
	}
	out, err = grepWithContext(root, "sentinel", "", "*.txt", false, false, 0, "", faScope(fa))
	if err != nil || !strings.HasSuffix(out, "(1 path hidden by file_access rules)") {
		t.Errorf("grep include counts only filtered-in files: %q %v", out, err)
	}
	if _, err := grepWithContext(root, "x", ".env", "", false, false, 0, "", faScope(fa)); err == nil || !strings.Contains(err.Error(), "deny_read") {
		t.Errorf("grep denied file: %v", err)
	}
	out, err = grepWithContext(root, "nope", "", "", false, false, 0, "", faScope(fa))
	if err != nil || !strings.HasPrefix(out, "no matches\n(") {
		t.Errorf("grep no matches: %q %v", out, err)
	}
}

func TestFileAccessWriteTools(t *testing.T) {
	root := t.TempDir()
	writeFiles(t, root, map[string]string{"go.sum": "orig\n", ".env": "orig\n", "vendor/x.go": "orig\n", "main.go": "orig\n"})
	fa := mustAccess(t, root, []string{".env"}, []string{"go.sum", "vendor/"})

	for _, p := range []string{"go.sum", "vendor/x.go", "vendor/new.go", ".env"} {
		if err := writeFile(root, p, "new\n", faScope(fa)); err == nil {
			t.Errorf("write %s allowed", p)
		}
		if _, err := editFile(root, p, "orig", "new", false, faScope(fa)); err == nil {
			t.Errorf("edit %s allowed", p)
		}
	}
	if err := writeFile(root, ".env", "x", faScope(fa)); err == nil || !strings.Contains(err.Error(), "deny_read") {
		t.Errorf("deny_read should also deny writes: %v", err)
	}
	// deny_write paths stay readable.
	if out, err := readFile(root, "go.sum", 0, 0, faScope(fa)); err != nil || !strings.Contains(out, "orig") {
		t.Errorf("read go.sum: %q %v", out, err)
	}
	if err := writeFile(root, "main.go", "new\n", faScope(fa)); err != nil {
		t.Errorf("write main.go: %v", err)
	}

	// A multi-file patch touching one denied path changes nothing.
	patch := "*** Begin Patch\n*** Update File: main.go\n@@\n-new\n+patched\n*** Add File: vendor/added.go\n+x\n*** End Patch"
	if _, err := applyPatch(root, patch, faScope(fa)); err == nil || !strings.Contains(err.Error(), "deny_write") {
		t.Errorf("patch: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "main.go")); string(b) != "new\n" {
		t.Errorf("main.go changed to %q", b)
	}
	if _, err := os.Stat(filepath.Join(root, "vendor/added.go")); err == nil {
		t.Error("vendor/added.go created")
	}
	// A move into a denied path is refused too.
	move := "*** Begin Patch\n*** Update File: main.go\n*** Move to: go.sum\n@@\n-new\n+moved\n*** End Patch"
	if _, err := applyPatch(root, move, faScope(fa)); err == nil || !strings.Contains(err.Error(), "go.sum") {
		t.Errorf("move: %v", err)
	}
	if _, err := applyPatch(root, "*** Begin Patch\n*** Update File: main.go\n@@\n-new\n+ok\n*** End Patch", faScope(fa)); err != nil {
		t.Errorf("allowed patch: %v", err)
	}
}

func TestFileAccessSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on windows")
	}
	root := t.TempDir()
	writeFiles(t, root, map[string]string{".env": "TOKEN=x\n", "secrets/k": "k\n", "ok.txt": "ok\n"})
	if err := os.Symlink(".env", filepath.Join(root, "innocent.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("secrets", filepath.Join(root, "linkdir")); err != nil {
		t.Fatal(err)
	}
	fa := mustAccess(t, root, []string{".env", "/secrets/"}, nil)
	for _, p := range []string{"innocent.txt", "linkdir/k", "linkdir/new"} {
		if _, err := readFile(root, p, 0, 0, faScope(fa)); err == nil {
			t.Errorf("read through symlink %s allowed", p)
		}
		if err := writeFile(root, p, "x", faScope(fa)); err == nil {
			t.Errorf("write through symlink %s allowed", p)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "secrets/new")); err == nil {
		t.Error("write created secrets/new through a symlink")
	}
	out, err := listFiles(root, "", "*", "", faScope(fa))
	if err != nil || strings.Contains(out, "innocent.txt") || strings.Contains(out, "TOKEN") {
		t.Errorf("glob shows symlink to denied file: %q %v", out, err)
	}
	out, err = grepWithContext(root, "TOKEN", "", "", false, false, 0, "", faScope(fa))
	if err != nil || strings.Contains(out, "TOKEN=") {
		t.Errorf("grep through symlink: %q %v", out, err)
	}
}
