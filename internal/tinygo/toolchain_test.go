package tinygo

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

type entry struct {
	name, body, link string
	typ              byte
}

func tgz(t *testing.T, ents ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range ents {
		h := &tar.Header{Name: e.name, Mode: 0o755, Typeflag: e.typ, Linkname: e.link}
		if e.typ == 0 {
			h.Typeflag = tar.TypeReg
		}
		if h.Typeflag == tar.TypeReg {
			h.Size = int64(len(e.body))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			tw.Write([]byte(e.body))
		}
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func sum(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// fakeRelease serves archives per version and counts requests.
type fakeRelease struct {
	srv  *httptest.Server
	hits atomic.Int32
	arch map[string][]byte // version -> archive
}

func newFake(t *testing.T) *fakeRelease {
	f := &fakeRelease{arch: map[string][]byte{}}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.hits.Add(1)
		for v, b := range f.arch {
			if r.URL.Path == "/v"+v+"/tinygo"+v+".linux-amd64.tar.gz" {
				w.Write(b)
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeRelease) manager(t *testing.T, cache, version string, checksum string) *Manager {
	return &Manager{CacheDir: cache, BaseURL: f.srv.URL, Version: version, GOOS: "linux", GOARCH: "amd64",
		Checksums: map[string]string{"linux/amd64": checksum}, HTTP: f.srv.Client()}
}

func goodArchive(t *testing.T, v string) []byte {
	return tgz(t, entry{name: "tinygo/", typ: tar.TypeDir}, entry{name: "tinygo/bin/tinygo", body: "#!/bin/sh\necho " + v + "\n"},
		entry{name: "tinygo/lib/x", body: "x"}, entry{name: "tinygo/lib/y", typ: tar.TypeSymlink, link: "x"})
}

func TestInstallDownloadsVerifiesAndCaches(t *testing.T) {
	f := newFake(t)
	a := goodArchive(t, "1.0")
	f.arch["1.0"] = a
	cache := t.TempDir()
	m := f.manager(t, cache, "1.0", sum(a))
	bin, already, err := m.Install(context.Background())
	if err != nil || already {
		t.Fatalf("install: %v already=%v", err, already)
	}
	if bin != filepath.Join(cache, "tinygo-1.0", "bin", "tinygo") {
		t.Fatalf("bin = %s", bin)
	}
	if b, _ := os.ReadFile(bin); !strings.Contains(string(b), "echo 1.0") {
		t.Fatalf("bin content %q", b)
	}
	if f.hits.Load() != 1 {
		t.Fatalf("hits = %d", f.hits.Load())
	}
	// Second install: cached, no network.
	f.srv.Close()
	bin2, already, err := m.Install(context.Background())
	if err != nil || !already || bin2 != bin {
		t.Fatalf("second install: %v already=%v %s", err, already, bin2)
	}
	if p, from, err := m.Resolve(context.Background(), false); err != nil || from != "cache" || p != bin {
		t.Fatalf("resolve: %s %s %v", p, from, err)
	}
	if s := m.Status(); !s.Installed || s.Path != bin || s.Pinned != "1.0" || s.Checksum != sum(a) {
		t.Fatalf("status %+v", s)
	}
}

func TestInstallChecksumMismatchLeavesNothing(t *testing.T) {
	f := newFake(t)
	f.arch["1.0"] = goodArchive(t, "1.0")
	cache := t.TempDir()
	m := f.manager(t, cache, "1.0", strings.Repeat("0", 64))
	if _, _, err := m.Install(context.Background()); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v", err)
	}
	ents, _ := os.ReadDir(cache)
	if len(ents) != 0 {
		t.Fatalf("cache not empty: %v", ents)
	}
	if _, _, err := m.Resolve(context.Background(), false); !errors.Is(err, ErrMissing) {
		t.Fatalf("resolve: %v", err)
	}
}

func TestTamperedCacheIsNotTrusted(t *testing.T) {
	f := newFake(t)
	a := goodArchive(t, "1.0")
	f.arch["1.0"] = a
	m := f.manager(t, t.TempDir(), "1.0", sum(a))
	if _, _, err := m.Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(m.Dir(), marker), []byte("bogus"), 0o644)
	if _, already, err := m.Install(context.Background()); err != nil || already {
		t.Fatalf("reinstall: %v already=%v", err, already)
	}
}

func TestUpdateFetchesNewPinnedAndRemovesOld(t *testing.T) {
	f := newFake(t)
	a1, a2 := goodArchive(t, "1.0"), goodArchive(t, "2.0")
	f.arch["1.0"], f.arch["2.0"] = a1, a2
	cache := t.TempDir()
	if _, _, err := f.manager(t, cache, "1.0", sum(a1)).Install(context.Background()); err != nil {
		t.Fatal(err)
	}
	m2 := f.manager(t, cache, "2.0", sum(a2))
	if s := m2.Status(); s.Installed || len(s.Others) != 1 || s.Others[0] != "1.0" {
		t.Fatalf("status before update %+v", s)
	}
	bin, already, removed, err := m2.Update(context.Background())
	if err != nil || already || len(removed) != 1 {
		t.Fatalf("update: %v already=%v removed=%v", err, already, removed)
	}
	if b, _ := os.ReadFile(bin); !strings.Contains(string(b), "echo 2.0") {
		t.Fatalf("bin %q", b)
	}
	if _, err := os.Stat(filepath.Join(cache, "tinygo-1.0")); !os.IsNotExist(err) {
		t.Fatal("old version kept")
	}
	if _, already, _, err := m2.Update(context.Background()); err != nil || !already {
		t.Fatalf("second update: %v already=%v", err, already)
	}
}

func TestExtractRejectsUnsafeEntries(t *testing.T) {
	cases := map[string][]entry{
		"dotdot":         {{name: "tinygo/../../evil", body: "x"}},
		"absolute":       {{name: "/tmp/evil", body: "x"}},
		"symlink escape": {{name: "tinygo/l", typ: tar.TypeSymlink, link: "../../etc"}},
		"absolute link":  {{name: "tinygo/l", typ: tar.TypeSymlink, link: "/etc/passwd"}},
		"write via link": {{name: "tinygo/d", typ: tar.TypeDir}, {name: "tinygo/l", typ: tar.TypeSymlink, link: "d"}, {name: "tinygo/l/f", body: "x"}},
		"hardlink out":   {{name: "tinygo/h", typ: tar.TypeLink, link: "../../etc/passwd"}},
		"device":         {{name: "tinygo/dev", typ: tar.TypeChar}},
	}
	for name, ents := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "a.tar.gz")
			os.WriteFile(p, tgz(t, ents...), 0o644)
			dst := filepath.Join(dir, "out")
			os.Mkdir(dst, 0o755)
			if err := extractTarGz(p, dst); err == nil {
				t.Fatal("unsafe archive extracted")
			}
			if _, err := os.Stat(filepath.Join(dir, "evil")); err == nil {
				t.Fatal("wrote outside dst")
			}
		})
	}
}

func TestResolve(t *testing.T) {
	ctx := context.Background()
	exe := filepath.Join(t.TempDir(), "tinygo")
	os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755)
	m := &Manager{CacheDir: t.TempDir(), Version: "1.0", GOOS: "linux", GOARCH: "amd64",
		Checksums: map[string]string{"linux/amd64": strings.Repeat("a", 64)}, BaseURL: "http://127.0.0.1:1"}
	if _, _, err := m.Resolve(ctx, false); !errors.Is(err, ErrMissing) {
		t.Fatalf("missing: %v", err)
	}
	m.Override = exe
	if p, from, err := m.Resolve(ctx, false); err != nil || from != "override" || p != exe {
		t.Fatalf("override: %s %s %v", p, from, err)
	}
	m.Override = filepath.Join(t.TempDir(), "nope")
	if _, _, err := m.Resolve(ctx, true); err == nil {
		t.Fatal("bad override accepted")
	}
	m.Override, m.GOOS = "", "plan9"
	if _, _, err := m.Install(ctx); err == nil || !strings.Contains(err.Error(), "no pinned") {
		t.Fatalf("unpinned platform: %v", err)
	}
}

func TestPinnedChecksumsWellFormed(t *testing.T) {
	for _, p := range []string{"linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64"} {
		c := Checksums[p]
		if _, err := hex.DecodeString(c); err != nil || len(c) != 64 {
			t.Errorf("%s: bad checksum %q", p, c)
		}
	}
}

// TinyGo is an external download: this package must never embed it.
func TestNoEmbed(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		b, _ := os.ReadFile(f)
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		if bytes.Contains(b, []byte("\n//go:"+"embed")) || bytes.Contains(b, []byte(`"embed"`)) {
			t.Errorf("%s uses go:embed; TinyGo must be downloaded, never bundled", f)
		}
	}
	if len(files) == 0 {
		t.Fatal("no sources found")
	}
}

// TestDefaultCacheInCwd covers issue #337: the cache defaults to
// ./.tinyagent/cache/toolchains in cwd, a .tinyagent symlink that leads
// outside cwd is refused, and the override is kept as given (it may be a
// system-wide install outside cwd).
func TestDefaultCacheInCwd(t *testing.T) {
	t.Setenv(EnvOverride, "")
	cwd := t.TempDir()
	m, err := Default(cwd, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(cwd, ".tinyagent", "cache", "toolchains"); m.CacheDir != want {
		t.Fatalf("CacheDir = %s, want %s", m.CacheDir, want)
	}
	if _, err := os.Stat(filepath.Join(cwd, ".tinyagent")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Default created files: %v", err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(cwd, ".tinyagent")); err != nil {
		t.Skip("no symlinks:", err)
	}
	if _, err := Default(cwd, ""); err == nil || !strings.Contains(err.Error(), "inside --cwd") {
		t.Errorf("symlinked .tinyagent outside cwd: err = %v", err)
	}
	os.Remove(filepath.Join(cwd, ".tinyagent"))
	exe := filepath.Join(outside, "tinygo")
	m, err = Default(cwd, exe)
	if err != nil || m.Override != exe {
		t.Errorf("override outside cwd: %v %+v", err, m)
	}
	t.Setenv(EnvOverride, exe)
	if m, err = Default(cwd, ""); err != nil || m.Override != exe {
		t.Errorf("env override: %v %+v", err, m)
	}
}

func TestLegacyCacheHint(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "cache"))
	uc, err := os.UserCacheDir()
	if err != nil || !strings.HasPrefix(uc, home) {
		t.Skip("user cache dir isn't under the fake HOME here")
	}
	if h := LegacyCacheHint(); h != "" {
		t.Fatalf("hint with no old cache: %q", h)
	}
	old := filepath.Join(uc, "tinyagent", "toolchains")
	os.MkdirAll(old, 0o755)
	h := LegacyCacheHint()
	if !strings.HasPrefix(h, "hint: ") || !strings.Contains(h, old) || !strings.Contains(h, CacheSubdir) || strings.Contains(h, "\n") {
		t.Fatalf("hint = %q", h)
	}
}
