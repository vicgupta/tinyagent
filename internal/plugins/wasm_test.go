package plugins

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	extism "github.com/extism/go-sdk"
)

// Test modules are hand-encoded here rather than committed as .wasm files.
//
// wasmModule builds a minimal module with one type "() -> i32", an optional
// imported function <ns>.<name> of that type, and an exported function
// "run" of that type. When an import is present, run's body is
// `call 0` (returns whatever the host function returns); otherwise it is
// `i32.const ret` (ret must be < 64 to fit a one-byte LEB128).
func wasmModule(ns, name string, ret byte) []byte {
	section := func(id byte, body []byte) []byte {
		return append([]byte{id, byte(len(body))}, body...)
	}
	str := func(s string) []byte { return append([]byte{byte(len(s))}, s...) }
	out := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00} // \0asm v1
	out = append(out, section(1, []byte{1, 0x60, 0, 1, 0x7f})...) // type 0: () -> i32
	runIdx := byte(0)
	body := []byte{0x00, 0x41, ret, 0x0b} // no locals; i32.const ret; end
	if name != "" {
		imp := []byte{1}
		imp = append(imp, str(ns)...)
		imp = append(imp, str(name)...)
		imp = append(imp, 0x00, 0x00) // func, type 0
		out = append(out, section(2, imp)...)
		runIdx = 1
		body = []byte{0x00, 0x10, 0x00, 0x0b} // no locals; call 0; end
	}
	out = append(out, section(3, []byte{1, 0})...) // one func of type 0
	exp := append([]byte{1}, str("run")...)
	exp = append(exp, 0x00, runIdx)
	out = append(out, section(7, exp)...)
	code := append([]byte{1, byte(len(body))}, body...)
	return append(out, section(10, code)...)
}

// constHost returns a host function "name" () -> i32 that returns v.
func constHost(name string, v uint64) HostFunc {
	return extism.NewHostFunctionWithStack(name, func(_ context.Context, _ *extism.CurrentPlugin, s []uint64) {
		s[0] = v
	}, nil, []extism.ValueType{extism.ValueTypeI32})
}

func run(t *testing.T, m *WasmModule) uint32 {
	t.Helper()
	rc, _, err := m.Call(context.Background(), "run", nil)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	return rc
}

type taint bool

func (t *taint) Tainted() bool { return bool(*t) }

func TestWasmLoadAllowedWhenSubset(t *testing.T) {
	h := &WasmHost{
		Grant: Permissions{HostFuncs: []string{"get", "other"}, Hosts: []string{"example.com"}, Paths: map[string]string{"/tmp": "/tmp"}},
		Funcs: []HostFunc{constHost("get", 42), constHost("other", 1)},
	}
	m, err := h.Load(context.Background(), WasmManifest{Name: "calc", Version: "1", Requires: Permissions{HostFuncs: []string{"get"}, Hosts: []string{"example.com"}}}, wasmModule(HostUserNamespace, "get", 0))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if got := run(t, m); got != 42 {
		t.Fatalf("run = %d, want 42", got)
	}
}

func TestWasmLoadDeniedOutsideGrant(t *testing.T) {
	h := &WasmHost{Grant: Permissions{HostFuncs: []string{"get"}, Hosts: []string{"a.com"}, Paths: map[string]string{"/ok": "/ok"}}, Funcs: []HostFunc{constHost("get", 1), constHost("secret", 2)}}
	cases := map[string]Permissions{
		"host func": {HostFuncs: []string{"secret"}},
		"path":      {Paths: map[string]string{"/etc": "/etc"}},
		"net host":  {Hosts: []string{"evil.com"}},
		"wasi":      {WASI: true},
	}
	for name, req := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := h.Load(context.Background(), WasmManifest{Name: "m", Version: "1", Requires: req}, wasmModule("", "", 1))
			if !errors.Is(err, ErrPermission) {
				t.Fatalf("err = %v, want ErrPermission", err)
			}
		})
	}
}

func TestWasmUndeclaredHostImportFails(t *testing.T) {
	// The module imports "secret" but declares nothing: refused even though
	// the host could provide it.
	h := &WasmHost{Grant: Permissions{HostFuncs: []string{"secret"}}, Funcs: []HostFunc{constHost("secret", 9)}}
	_, err := h.Load(context.Background(), WasmManifest{Name: "m", Version: "1"}, wasmModule(HostUserNamespace, "secret", 0))
	if !errors.Is(err, ErrPermission) {
		t.Fatalf("err = %v, want ErrPermission", err)
	}
	// Importing from an unknown namespace is refused too.
	_, err = h.Load(context.Background(), WasmManifest{Name: "m", Version: "1"}, wasmModule("env", "secret", 0))
	if !errors.Is(err, ErrPermission) {
		t.Fatalf("env import: err = %v, want ErrPermission", err)
	}
	// WASI imports need wasi declared.
	_, err = h.Load(context.Background(), WasmManifest{Name: "m", Version: "1"}, wasmModule("wasi_snapshot_preview1", "sched_yield", 0))
	if !errors.Is(err, ErrPermission) {
		t.Fatalf("wasi import: err = %v, want ErrPermission", err)
	}
}

func TestWasmSwapFromBytesAndFile(t *testing.T) {
	h := &WasmHost{}
	ctx := context.Background()
	m, err := h.Load(ctx, WasmManifest{Name: "m", Version: "1"}, wasmModule("", "", 1))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	v1 := m.cur.Load()
	if run(t, m) != 1 {
		t.Fatal("v1")
	}
	if err := m.Swap(ctx, WasmManifest{Name: "m", Version: "2"}, wasmModule("", "", 2)); err != nil {
		t.Fatal(err)
	}
	if run(t, m) != 2 || m.Version() != "2" {
		t.Fatal("v2 not active")
	}
	if !v1.closed.Load() {
		t.Fatal("old instance not closed")
	}
	path := filepath.Join(t.TempDir(), "m.wasm")
	if err := os.WriteFile(path, wasmModule("", "", 3), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.SwapFile(ctx, WasmManifest{Name: "m", Version: "3"}, path); err != nil {
		t.Fatal(err)
	}
	if run(t, m) != 3 {
		t.Fatal("v3 not active")
	}
}

func TestWasmSwapCannotWiden(t *testing.T) {
	h := &WasmHost{Grant: Permissions{HostFuncs: []string{"get"}}, Funcs: []HostFunc{constHost("get", 5)}}
	ctx := context.Background()
	m, err := h.Load(ctx, WasmManifest{Name: "m", Version: "1"}, wasmModule("", "", 1))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	// "get" is in the caller's grant but not in the pinned list from v1.
	err = m.Swap(ctx, WasmManifest{Name: "m", Version: "2", Requires: Permissions{HostFuncs: []string{"get"}}}, wasmModule(HostUserNamespace, "get", 0))
	if !errors.Is(err, ErrPermission) {
		t.Fatalf("err = %v, want ErrPermission", err)
	}
	// Smuggling the import without declaring it fails as well.
	if err := m.Swap(ctx, WasmManifest{Name: "m", Version: "2"}, wasmModule(HostUserNamespace, "get", 0)); !errors.Is(err, ErrPermission) {
		t.Fatalf("undeclared import: err = %v", err)
	}
	if run(t, m) != 1 || m.Version() != "1" {
		t.Fatal("old instance should keep running")
	}
}

func TestWasmInFlightCallSurvivesSwap(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	block := extism.NewHostFunctionWithStack("block", func(_ context.Context, _ *extism.CurrentPlugin, s []uint64) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		s[0] = 10
	}, nil, []extism.ValueType{extism.ValueTypeI32})
	h := &WasmHost{Grant: Permissions{HostFuncs: []string{"block"}}, Funcs: []HostFunc{block}}
	ctx := context.Background()
	req := Permissions{HostFuncs: []string{"block"}}
	m, err := h.Load(ctx, WasmManifest{Name: "m", Version: "1", Requires: req}, wasmModule(HostUserNamespace, "block", 0))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	v1 := m.cur.Load()
	done := make(chan uint32)
	go func() {
		rc, _, err := m.Call(ctx, "run", nil)
		if err != nil {
			t.Error(err)
		}
		done <- rc
	}()
	<-entered
	// Swap must not block on the in-flight call; v2 needs no permissions.
	swapped := make(chan error)
	go func() { swapped <- m.Swap(ctx, WasmManifest{Name: "m", Version: "2"}, wasmModule("", "", 2)) }()
	select {
	case err := <-swapped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("swap blocked on in-flight call")
	}
	if run(t, m) != 2 {
		t.Fatal("new calls should use v2")
	}
	if v1.closed.Load() {
		t.Fatal("old instance closed while a call was running")
	}
	close(release)
	if rc := <-done; rc != 10 {
		t.Fatalf("in-flight call = %d, want 10", rc)
	}
	if !v1.closed.Load() {
		t.Fatal("old instance not closed after in-flight call finished")
	}
}

func TestWasmRefusedAfterTaint(t *testing.T) {
	var tt taint
	h := &WasmHost{Taint: &tt}
	ctx := context.Background()
	m, err := h.Load(ctx, WasmManifest{Name: "m", Version: "1"}, wasmModule("", "", 1))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	tt = true
	if err := m.Swap(ctx, WasmManifest{Name: "m", Version: "2"}, wasmModule("", "", 2)); !errors.Is(err, ErrTainted) {
		t.Fatalf("swap err = %v", err)
	}
	if _, err := h.Load(ctx, WasmManifest{Name: "n", Version: "1"}, wasmModule("", "", 1)); !errors.Is(err, ErrTainted) {
		t.Fatalf("load err = %v", err)
	}
	if run(t, m) != 1 {
		t.Fatal("old instance should keep running")
	}
}

func TestDataModuleSchemaAndSwitch(t *testing.T) {
	d := &DataModule{Name: "sys", Kind: DataPrompt}
	v1, err := d.Set([]byte("---\nname: sys\n---\nBe brief.\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Set([]byte("no front matter")); err == nil {
		t.Fatal("invalid prompt accepted")
	}
	if d.Current() != v1 {
		t.Fatal("invalid content replaced active version")
	}
	v2, err := d.Set([]byte("---\nname: sys\n---\nBe thorough.\n"))
	if err != nil || v2.Hash == v1.Hash || d.Current() != v2 {
		t.Fatalf("switch failed: %v", err)
	}
	if err := d.Rollback(); err != nil || d.Current() != v1 {
		t.Fatal("rollback failed")
	}

	s := &DataModule{Name: "sk", Kind: DataSkill}
	if _, err := s.Set([]byte("---\nname: x\n---\n")); err == nil || !strings.Contains(err.Error(), "description") {
		t.Fatalf("skill without description: %v", err)
	}

	r := &DataModule{Name: "retry", Kind: DataRetry}
	if v, err := r.Set([]byte(`{"max_attempts": 3, "backoff_ms": 100}`)); err != nil || v.Retry.MaxAttempts != 3 {
		t.Fatalf("json retry: %v", err)
	}
	path := filepath.Join(t.TempDir(), "retry.toml")
	os.WriteFile(path, []byte("max_attempts = 5\n"), 0o600)
	if v, err := r.SetFile(path); err != nil || v.Retry.MaxAttempts != 5 {
		t.Fatalf("toml retry: %v", err)
	}
	for _, bad := range []string{`{"max_attempts": 0}`, `{"max_attempts": 2, "bogus": 1}`, "max_attempts = \"x\"\n"} {
		if _, err := r.Set([]byte(bad)); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	if r.Current().Retry.MaxAttempts != 5 {
		t.Fatal("invalid policy replaced active version")
	}
}
