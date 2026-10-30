package plugins

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vicgupta/tinyagent/internal/config"
)

// echoWasm hand-encodes a module whose export "echo" copies its input to
// its output through the Extism kernel and returns rc:
//
//	(import "extism:host/env" "input_offset" (func (result i64)))   ;; 0
//	(import "extism:host/env" "input_length" (func (result i64)))   ;; 1
//	(import "extism:host/env" "output_set" (func (param i64 i64)))  ;; 2
//	(func (export "echo") (result i32) call 0 call 1 call 2 i32.const rc)
func echoWasm(rc byte) []byte {
	sec := func(id byte, b ...byte) []byte { return append([]byte{id, byte(len(b))}, b...) }
	str := func(s string) []byte { return append([]byte{byte(len(s))}, s...) }
	imp := func(name string, typ byte) []byte {
		b := append(str("extism:host/env"), str(name)...)
		return append(b, 0x00, typ)
	}
	out := []byte{0x00, 0x61, 0x73, 0x6d, 1, 0, 0, 0}
	out = append(out, sec(1, 3,
		0x60, 0, 1, 0x7e, // t0: () -> i64
		0x60, 2, 0x7e, 0x7e, 0, // t1: (i64, i64) -> ()
		0x60, 0, 1, 0x7f)...) // t2: () -> i32
	var imps []byte
	imps = append(imps, 3)
	imps = append(imps, imp("input_offset", 0)...)
	imps = append(imps, imp("input_length", 0)...)
	imps = append(imps, imp("output_set", 1)...)
	out = append(out, sec(2, imps...)...)
	out = append(out, sec(3, 1, 2)...)
	out = append(out, sec(7, append(append([]byte{1}, str("echo")...), 0x00, 3)...)...)
	body := []byte{0x00, 0x10, 0, 0x10, 1, 0x10, 2, 0x41, rc, 0x0b}
	return append(out, sec(10, append([]byte{1, byte(len(body))}, body...)...)...)
}

// codePlugin writes a plugin dir declaring one wasm module "m" with the
// given requires JSON.
func codePlugin(t *testing.T, name, requires string, wasm []byte) string {
	t.Helper()
	dir := t.TempDir()
	pj := `{"$schema":"` + SchemaV1 + `","name":"` + name + `","extensions":{"tinyagent":{"wasm_modules":[` +
		`{"name":"m","file":"wasm/m.wasm","version":"1","exports":[{"name":"echo","description":"Echo input"}],"requires":` + requires + `}]}}}`
	os.MkdirAll(filepath.Join(dir, "wasm"), 0o755)
	os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(pj), 0o644)
	os.WriteFile(filepath.Join(dir, "wasm", "m.wasm"), wasm, 0o644)
	return dir
}

func TestWasmPluginActivateAndReload(t *testing.T) {
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, "data"), 0o755)
	good := codePlugin(t, "good", `{"allowed_paths":{"ro:data":"/data"}}`, echoWasm(0))
	bad := codePlugin(t, "bad", `{"allowed_hosts":["evil.example"]}`, echoWasm(0))
	cfg := []config.Plugin{{Path: bad, WasmModules: []string{"m"}}, {Path: good, WasmModules: []string{"m"}}}
	ls, err := Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var pool WasmPool
	defer pool.Close()
	var w bytes.Buffer
	g := SessionGrant{Root: root, Hosts: []string{"api.example"}}
	wt := pool.Activate(context.Background(), ls, cfg, g, &w)
	if !strings.Contains(w.String(), `plugin bad wasm module m refused: permission denied: network host "evil.example"`) {
		t.Errorf("warning = %q", w.String())
	}
	if len(wt.Specs) != 1 || wt.Specs[0].Function.Name != "good__echo" {
		t.Fatalf("specs = %+v", wt.Specs)
	}
	out, isErr, ok := wt.Call(context.Background(), "good__echo", `{"x":1}`)
	if !ok || isErr || out != `{"x":1}` {
		t.Fatalf("call = %q %v %v", out, isErr, ok)
	}
	mod := pool.modules[ls[1].Manifest.Root+"\x00m"].mod
	v1 := mod.Version()

	// Re-activation with an unchanged file reuses the instance; a changed
	// file is swapped in.
	pool.Activate(context.Background(), ls, cfg, g, &w)
	if mod.Version() != v1 {
		t.Fatal("unchanged module was swapped")
	}
	os.WriteFile(filepath.Join(good, "wasm", "m.wasm"), echoWasm(3), 0o644)
	wt = pool.Activate(context.Background(), ls, cfg, g, &w)
	if mod.Version() == v1 {
		t.Fatal("changed module not swapped")
	}
	if out, isErr, _ := wt.Call(context.Background(), "good__echo", "hi"); !isErr || !strings.Contains(out, "exit code 3") {
		t.Fatalf("after swap: %q %v", out, isErr)
	}

	// A narrower session (data now denied by file_access) refuses the module.
	w.Reset()
	g.CheckPath = func(string, bool) error { return os.ErrPermission }
	wt = pool.Activate(context.Background(), ls, cfg, g, &w)
	if len(wt.Specs) != 0 || !strings.Contains(w.String(), `plugin good wasm module m refused: permission denied: path`) {
		t.Fatalf("narrowed: %+v %q", wt.Specs, w.String())
	}
}

func TestWasmPluginRefusals(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	cases := map[string]struct{ requires, want string }{
		"host function": {`{"host_functions":["exec"]}`, `host function "exec" is not offered`},
		"outside root":  {`{"allowed_paths":{"/etc":"/etc"}}`, `path "/etc"`},
		"undeclared":    {`{}`, "permission denied"}, // module imports nothing undeclared, but see below
	}
	for name, c := range cases {
		if name == "undeclared" {
			continue
		}
		dir := codePlugin(t, "p", c.requires, echoWasm(0))
		cfg := []config.Plugin{{Path: dir, WasmModules: []string{"m"}}}
		ls, _ := Load(cfg)
		var pool WasmPool
		var w bytes.Buffer
		wt := pool.Activate(ctx, ls, cfg, SessionGrant{Root: root}, &w)
		if len(wt.Specs) != 0 || !strings.Contains(w.String(), c.want) {
			t.Errorf("%s: %q", name, w.String())
		}
	}
	// Unauthorized modules are not loaded at all; tainted sessions refuse.
	dir := codePlugin(t, "p", `{}`, echoWasm(0))
	ls, _ := Load([]config.Plugin{{Path: dir}})
	var pool WasmPool
	if wt := pool.Activate(ctx, ls, []config.Plugin{{Path: dir}}, SessionGrant{Root: root}, &bytes.Buffer{}); len(wt.Specs) != 0 {
		t.Error("unauthorized module loaded")
	}
	tt := taint(true)
	var w bytes.Buffer
	cfg := []config.Plugin{{Path: dir, WasmModules: []string{"m"}}}
	if wt := pool.Activate(ctx, ls, cfg, SessionGrant{Root: root, Taint: &tt}, &w); len(wt.Specs) != 0 || !strings.Contains(w.String(), "tainted") {
		t.Errorf("tainted: %q", w.String())
	}
}
