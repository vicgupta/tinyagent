package plugins

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/config"
)

func goSourcePlugin(t *testing.T, src string) string {
	t.Helper()
	dir := t.TempDir()
	pj := `{"$schema":"` + SchemaV1 + `","name":"gp","extensions":{"tinyagent":{"wasm_modules":[` +
		`{"name":"m","source":"go/m","lang":"go","version":"1","exports":[{"name":"echo","description":"Echo"}],"requires":{}}]}}}`
	os.MkdirAll(filepath.Join(dir, "go", "m"), 0o755)
	os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(pj), 0o644)
	os.WriteFile(filepath.Join(dir, "go", "m", "m.go"), []byte(src), 0o644)
	return dir
}

func TestGoSourceModuleCompiledStoredAndCached(t *testing.T) {
	root := t.TempDir()
	dir := goSourcePlugin(t, "package main\n")
	cfg := []config.Plugin{{Path: dir, WasmModules: []string{"m"}}}
	store := &ModuleStore{Dir: filepath.Join(root, "modules")}
	compiles := 0
	compile := func(_ context.Context, files map[string][]byte) ([]byte, error) {
		compiles++
		if _, ok := files["m.go"]; !ok {
			t.Errorf("files = %v", files)
		}
		return echoWasm(0), nil
	}
	activate := func() string {
		ls, err := Load(cfg)
		if err != nil {
			t.Fatal(err)
		}
		pool := WasmPool{Store: store, Compile: compile}
		defer pool.Close()
		var w bytes.Buffer
		wt := pool.Activate(context.Background(), ls, cfg, SessionGrant{Root: root}, &w)
		if w.Len() > 0 {
			t.Fatalf("warnings: %s", w.String())
		}
		out, isErr, ok := wt.Call(context.Background(), "gp__echo", `hi`)
		if !ok || isErr {
			t.Fatalf("call: %q %v %v", out, isErr, ok)
		}
		return out
	}
	if out := activate(); out != "hi" || compiles != 1 {
		t.Fatalf("first: %q compiles=%d", out, compiles)
	}
	activate()
	if compiles != 1 {
		t.Fatalf("unchanged source recompiled: %d", compiles)
	}
	if v, _ := store.Versions(StoreName("gp", "m")); len(v) != 1 {
		t.Fatalf("versions = %v", v)
	}
	os.WriteFile(filepath.Join(dir, "go", "m", "m.go"), []byte("package main\n// v2\n"), 0o644)
	activate()
	if compiles != 2 {
		t.Fatalf("changed source not rebuilt: %d", compiles)
	}
}

func TestGoSourceModuleRefusals(t *testing.T) {
	root := t.TempDir()
	dir := goSourcePlugin(t, "package main\n")
	cfg := []config.Plugin{{Path: dir, WasmModules: []string{"m"}}}
	ls, err := Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	run := func(p *WasmPool, g SessionGrant) string {
		defer p.Close()
		var w bytes.Buffer
		p.Activate(context.Background(), ls, cfg, g, &w)
		return w.String()
	}
	if w := run(&WasmPool{}, SessionGrant{Root: root}); !strings.Contains(w, "need the module store") {
		t.Errorf("no store: %q", w)
	}
	failing := func(context.Context, map[string][]byte) ([]byte, error) { return nil, errors.New("boom") }
	if w := run(&WasmPool{Store: &ModuleStore{Dir: t.TempDir()}, Compile: failing}, SessionGrant{Root: root}); !strings.Contains(w, "boom") {
		t.Errorf("compile error: %q", w)
	}
	called := false
	spy := func(context.Context, map[string][]byte) ([]byte, error) { called = true; return echoWasm(0), nil }
	w := run(&WasmPool{Store: &ModuleStore{Dir: t.TempDir()}, Compile: spy}, SessionGrant{Root: root, Taint: alwaysTainted{}})
	if called || w == "" {
		t.Errorf("tainted session compiled (called=%v) %q", called, w)
	}
}

type alwaysTainted struct{}

func (alwaysTainted) Tainted() bool { return true }
