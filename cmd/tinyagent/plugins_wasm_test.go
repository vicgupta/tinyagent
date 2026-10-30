package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// echoWasm hand-encodes a module whose export "echo" copies its input to
// its output through the Extism kernel (see internal/plugins tests):
//
//	(import "extism:host/env" "input_offset" (func (result i64)))
//	(import "extism:host/env" "input_length" (func (result i64)))
//	(import "extism:host/env" "output_set" (func (param i64 i64)))
//	(func (export "echo") (result i32) call 0 call 1 call 2 i32.const 0)
func echoWasm() []byte {
	sec := func(id byte, b ...byte) []byte { return append([]byte{id, byte(len(b))}, b...) }
	str := func(s string) []byte { return append([]byte{byte(len(s))}, s...) }
	imp := func(name string, typ byte) []byte {
		return append(append(str("extism:host/env"), str(name)...), 0x00, typ)
	}
	out := []byte{0x00, 0x61, 0x73, 0x6d, 1, 0, 0, 0}
	out = append(out, sec(1, 3, 0x60, 0, 1, 0x7e, 0x60, 2, 0x7e, 0x7e, 0, 0x60, 0, 1, 0x7f)...)
	imps := []byte{3}
	imps = append(imps, imp("input_offset", 0)...)
	imps = append(imps, imp("input_length", 0)...)
	imps = append(imps, imp("output_set", 1)...)
	out = append(out, sec(2, imps...)...)
	out = append(out, sec(3, 1, 2)...)
	out = append(out, sec(7, append(append([]byte{1}, str("echo")...), 0x00, 3)...)...)
	body := []byte{0x00, 0x10, 0, 0x10, 1, 0x10, 2, 0x41, 0, 0x0b}
	return append(out, sec(10, append([]byte{1, byte(len(body))}, body...)...)...)
}

func wasmPlugin(t *testing.T, name, requires string) string {
	t.Helper()
	dir := t.TempDir()
	pj := `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"` + name + `","extensions":{"tinyagent":{"wasm_modules":[` +
		`{"name":"m","file":"m.wasm","version":"1","exports":[{"name":"echo","description":"Echo the arguments"}],"requires":` + requires + `}]}}}`
	os.WriteFile(filepath.Join(dir, "plugin.json"), []byte(pj), 0o644)
	os.WriteFile(filepath.Join(dir, "m.wasm"), echoWasm(), 0o644)
	return dir
}

func TestPluginWasmTool(t *testing.T) {
	good := wasmPlugin(t, "kit", `{}`)
	bad := wasmPlugin(t, "grabby", `{"allowed_hosts":["evil.example"]}`)
	cfg := pluginConfig(t, "plugins:\n  - path: "+bad+"\n    wasm_modules: [m]\n  - path: "+good+"\n    wasm_modules: [m]\n")
	f := newToolCallOpenAI(t, [2]string{"kit__echo", `{"msg":"hello-wasm"}`})
	code, _, stderr := runCLI(t, "-t", "x", "-m", "openai/gpt-4o", "--no-stream", "--cwd", t.TempDir(), "-c", cfg)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !strings.Contains(stderr, `plugin grabby wasm module m refused: permission denied: network host "evil.example"`) {
		t.Errorf("refusal not reported: %s", stderr)
	}
	if !strings.Contains(f.bodies[0], `"kit__echo"`) || !strings.Contains(f.bodies[0], "Echo the arguments") || strings.Contains(f.bodies[0], "grabby__echo") {
		t.Errorf("tools offered: %s", f.bodies[0])
	}
	if !strings.Contains(f.bodies[1], `hello-wasm`) || strings.Contains(f.bodies[1], "error:") {
		t.Errorf("tool result: %s", f.bodies[1])
	}

	// Read-only runs load no modules.
	f = newToolCallOpenAI(t)
	code, _, stderr = runCLI(t, "-t", "x", "-m", "openai/gpt-4o", "--no-stream", "--cwd", t.TempDir(), "-c", cfg, "--read-only")
	if code != 0 || strings.Contains(f.bodies[0], "kit__echo") || !strings.Contains(stderr, "wasm modules are not loaded") {
		t.Errorf("read-only: exit %d, %s", code, stderr)
	}
}
