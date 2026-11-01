package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/plugins"
	"github.com/vicgupta/tinyagent/internal/tinygo"
	"github.com/vicgupta/tinyagent/internal/tools"
)

func fakeTinygoArchive(t *testing.T) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := "#!/bin/sh\n"
	tw.WriteHeader(&tar.Header{Name: "tinygo/bin/tinygo", Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
	tw.Write([]byte(body))
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func TestTinygoCLI(t *testing.T) {
	arch := fakeTinygoArchive(t)
	h := sha256.Sum256(arch)
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++; w.Write(arch) }))
	defer srv.Close()
	cache := t.TempDir()
	old, oldHint := newTinygoManager, tinygoLegacyHint
	defer func() { newTinygoManager, tinygoLegacyHint = old, oldHint }()
	tinygoLegacyHint = func() string { return "" }
	newTinygoManager = func(_, override string) (*tinygo.Manager, error) {
		return &tinygo.Manager{CacheDir: cache, BaseURL: srv.URL, Version: "9.9.9", GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
			Checksums: map[string]string{runtime.GOOS + "/" + runtime.GOARCH: hex.EncodeToString(h[:])}, HTTP: srv.Client(), Override: override}, nil
	}
	cfgPath := filepath.Join(t.TempDir(), "c.yaml")
	os.WriteFile(cfgPath, []byte("model: x\n"), 0o644)
	do := func(o options) (string, int) {
		o.configPath = cfgPath
		var out, errw bytes.Buffer
		code := runTinygo(&o, &out, &errw)
		return out.String() + errw.String(), code
	}
	if out, code := do(options{tinygoStatus: true}); code != 0 || !strings.Contains(out, "installed: no") || !strings.Contains(out, "pinned: TinyGo 9.9.9") {
		t.Fatalf("status: %d %s", code, out)
	}
	if out, code := do(options{tinygoInstall: true}); code != 0 || !strings.Contains(out, "installed TinyGo 9.9.9") || hits != 1 {
		t.Fatalf("install: %d %s hits=%d", code, out, hits)
	}
	if out, code := do(options{tinygoInstall: true}); code != 0 || !strings.Contains(out, "already installed") || hits != 1 {
		t.Fatalf("reinstall: %d %s hits=%d", code, out, hits)
	}
	os.MkdirAll(filepath.Join(cache, "tinygo-1.0.0", "bin"), 0o755)
	if out, code := do(options{tinygoUpdate: true}); code != 0 || !strings.Contains(out, "removed old TinyGo") || !strings.Contains(out, "up to date") {
		t.Fatalf("update: %d %s", code, out)
	}
	if out, code := do(options{tinygoStatus: true}); code != 0 || !strings.Contains(out, "checksum OK") || !strings.Contains(out, filepath.Join(cache, "tinygo-9.9.9")) {
		t.Fatalf("status after: %d %s", code, out)
	}
	if sub := findSubcommand("tinygo"); sub == nil {
		t.Fatal("no tinygo subcommand")
	}
}

// TestTinygoIntegration builds a Go plugin module with a real TinyGo, under
// the OS sandbox, and calls it through Extism. It runs when TinyGo is
// available (TINYAGENT_TINYGO or a cached install) and skips otherwise.
func TestTinygoIntegration(t *testing.T) {
	skip := t.Skip // CI sets TINYAGENT_REQUIRE_TINYGO so a skip there fails
	if os.Getenv("TINYAGENT_REQUIRE_TINYGO") == "1" {
		skip = t.Fatal
	}
	m, err := tinygo.Default(".", "")
	if err != nil {
		skip(err)
	}
	bin, _, err := m.Resolve(context.Background(), false)
	if err != nil {
		skip("TinyGo not available: ", err)
	}
	if _, err := exec.LookPath("go"); err != nil {
		skip("TinyGo needs a Go toolchain on PATH")
	}
	if _, err := tools.NewSandbox(t.TempDir(), nil, false, nil); err != nil {
		skip("no OS sandbox: ", err)
	}
	dir := t.TempDir()
	pj := `{"$schema":"` + plugins.SchemaV1 + `","name":"tg","extensions":{"tinyagent":{"wasm_modules":[` +
		`{"name":"m","source":"src","lang":"go","version":"1","exports":[{"name":"upper","description":"Uppercase"}],"requires":{"wasi":true}}]}}}`
	os.MkdirAll(filepath.Join(dir, "src"), 0o755)
	os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(pj), 0o644)
	os.WriteFile(filepath.Join(dir, "src", "upper.go"), []byte(`package main

import "bytes"

//go:wasmexport upper
func upper() int32 {
	output(bytes.ToUpper(input()))
	return 0
}
`), 0o644)
	cfg := []config.Plugin{{Path: dir, WasmModules: []string{"m"}}}
	ls, err := plugins.Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	var w bytes.Buffer
	pool := plugins.WasmPool{Store: moduleStore(root), Compile: goModuleCompiler(root, bin, &w)}
	defer pool.Close()
	wt := pool.Activate(context.Background(), ls, cfg, plugins.SessionGrant{Root: root}, &w)
	out, isErr, ok := wt.Call(context.Background(), "tg__upper", `{"text":"hi"}`)
	if !ok || isErr || out != `{"TEXT":"HI"}` {
		t.Fatalf("call: %q isErr=%v ok=%v log=%s", out, isErr, ok, w.String())
	}
}

// TestTinygoCLIOnlyCwd covers issue #337: with HOME, XDG_* and the user
// cache and config dirs pointed at a temp dir that holds a cached TinyGo in
// the old location, the toolchain is cached in ./.tinyagent/cache/toolchains
// inside --cwd, the old cache is never used (only one hint line names it)
// and nothing is written outside --cwd.
func TestTinygoCLIOnlyCwd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, "cache"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "cache"))
	t.Setenv("APPDATA", filepath.Join(home, "config"))
	t.Setenv(tinygo.EnvOverride, "")
	ucache, err := os.UserCacheDir()
	if err != nil {
		t.Skip("no user cache dir:", err)
	}
	if !strings.HasPrefix(ucache, home) {
		t.Skipf("user cache dir %s isn't under the fake HOME on this platform", ucache)
	}
	// A verified-looking cached install in the old location.
	oldDir := filepath.Join(ucache, "tinyagent", "toolchains", "tinygo-"+tinygo.Version)
	os.MkdirAll(filepath.Join(oldDir, "bin"), 0o755)
	os.WriteFile(filepath.Join(oldDir, "bin", "tinygo"), []byte("#!/bin/sh\n"), 0o755)
	os.WriteFile(filepath.Join(oldDir, ".tinyagent-verified"), []byte(tinygo.Checksums[runtime.GOOS+"/"+runtime.GOARCH]+"\n"), 0o644)
	before := treeListing(t, home)

	cwd := t.TempDir()
	os.WriteFile(filepath.Join(cwd, "tinyagent.yaml"), []byte("model: x\n"), 0o644)
	var out, errw bytes.Buffer
	code := runTinygo(&options{cwd: cwd, tinygoStatus: true}, &out, &errw)
	if code != 0 {
		t.Fatalf("status: %d %s %s", code, out.String(), errw.String())
	}
	if !strings.Contains(out.String(), "installed: no") || !strings.Contains(out.String(), "using: none") {
		t.Errorf("old cache was used: %s", out.String())
	}
	if n := strings.Count(errw.String(), "hint: the TinyGo cache in "); n != 1 || !strings.Contains(errw.String(), filepath.Dir(oldDir)) {
		t.Errorf("want one hint naming %s, stderr %q", filepath.Dir(oldDir), errw.String())
	}
	m, err := tinygo.Default(cwd, "")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(cwd, ".tinyagent", "cache", "toolchains"); m.CacheDir != want {
		t.Errorf("cache dir %s, want %s", m.CacheDir, want)
	}
	if after := treeListing(t, home); after != before {
		t.Errorf("files outside --cwd changed:\nbefore %s\nafter  %s", before, after)
	}

	// No old cache: no hint.
	os.RemoveAll(filepath.Join(ucache, "tinyagent"))
	out.Reset()
	errw.Reset()
	if code := runTinygo(&options{cwd: cwd, tinygoStatus: true}, &out, &errw); code != 0 || strings.Contains(errw.String(), "hint:") {
		t.Errorf("unexpected hint: %d %q", code, errw.String())
	}
}

// treeListing lists every path under dir with its size.
func treeListing(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil {
			fmt.Fprintf(&b, "%s:%d;", p, fi.Size())
		}
		return nil
	})
	return b.String()
}
