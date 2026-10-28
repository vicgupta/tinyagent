package plugins

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writePlugin(t *testing.T, manifest string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ManifestFile), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestValidName(t *testing.T) {
	good := []string{"a", "review-kit", "a.b", "x1", strings.Repeat("a", 64)}
	bad := []string{"", "-a", "a-", ".a", "a.", "A", "a--b", "a..b", "a_b", strings.Repeat("a", 65)}
	for _, s := range good {
		if !ValidName(s) {
			t.Errorf("ValidName(%q) = false", s)
		}
	}
	for _, s := range bad {
		if ValidName(s) {
			t.Errorf("ValidName(%q) = true", s)
		}
	}
}

func TestLoadManifestOK(t *testing.T) {
	dir := writePlugin(t, `{"$schema":"`+SchemaV1+`","name":"review-kit","version":"1.2.0","description":"d","extensions":{"x":1}}`)
	m, err := LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "review-kit" || m.Version != "1.2.0" || m.Description != "d" || len(m.Warnings) != 0 {
		t.Fatalf("got %+v", m)
	}
}

func TestLoadManifestWarnings(t *testing.T) {
	dir := writePlugin(t, `{"$schema":"`+SchemaV1+`","name":"p","zeta":1,"alpha":2,"extensions":[]}`)
	m, err := LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(m.Warnings, "\n")
	for _, want := range []string{"extensions is not an object", `unknown field "alpha"`, `unknown field "zeta"`} {
		if !strings.Contains(got, want) {
			t.Errorf("warnings %q missing %q", got, want)
		}
	}
	if strings.Index(got, "alpha") > strings.Index(got, "zeta") {
		t.Errorf("unknown fields not sorted: %q", got)
	}
}

func TestLoadManifestRejects(t *testing.T) {
	cases := map[string]string{
		"missing $schema": `{"name":"p"}`,
		"unsupported":     `{"$schema":"https://example.com/x.json","name":"p"}`,
		"missing name":    `{"$schema":"` + SchemaV1 + `"}`,
		"invalid name":    `{"$schema":"` + SchemaV1 + `","name":"Bad--Name"}`,
		"invalid JSON":    `{`,
		"must be a JSON":  `null`,
		"trailing data":   `{"$schema":"` + SchemaV1 + `","name":"p"} {}`,
	}
	for want, body := range cases {
		_, err := LoadManifest(writePlugin(t, body))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v", want, err)
		}
	}
}

func TestLoadManifestPathErrors(t *testing.T) {
	if _, err := LoadManifest("relative/dir"); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Errorf("relative: %v", err)
	}
	if _, err := LoadManifest(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("missing dir: want error")
	}
	if _, err := LoadManifest(t.TempDir()); err == nil || !strings.Contains(err.Error(), ManifestFile) {
		t.Errorf("missing manifest: %v", err)
	}
	f := filepath.Join(t.TempDir(), "file")
	os.WriteFile(f, nil, 0o644)
	if _, err := LoadManifest(f); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("file: %v", err)
	}
}

func TestLoadManifestSymlinkOutsideRoot(t *testing.T) {
	outside := writePlugin(t, `{"$schema":"`+SchemaV1+`","name":"p"}`)
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(outside, ManifestFile), filepath.Join(dir, ManifestFile)); err != nil {
		t.Skip("symlinks unsupported:", err)
	}
	if _, err := LoadManifest(dir); err == nil || !strings.Contains(err.Error(), "outside the plugin root") {
		t.Errorf("err = %v", err)
	}
}

func TestLoadManifestSymlinkedRoot(t *testing.T) {
	real := writePlugin(t, `{"$schema":"`+SchemaV1+`","name":"p"}`)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("symlinks unsupported:", err)
	}
	m, err := LoadManifest(link)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(real)
	if m.Root != want {
		t.Errorf("Root = %q, want %q", m.Root, want)
	}
}
