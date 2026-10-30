package plugins

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vicgupta/tinyagent/internal/config"
)

func testStore(t *testing.T) *ModuleStore {
	t.Helper()
	n := 0
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return &ModuleStore{Dir: filepath.Join(t.TempDir(), "modules"), Now: func() time.Time { n++; return base.Add(time.Duration(n) * time.Minute) }}
}

func TestStoreNeverOverwritesAndMovesPointer(t *testing.T) {
	s := testStore(t)
	h1, err := s.Put("m", "wasm", []byte("v1"), Permissions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(s.Dir, "m", h1+".wasm")
	fi1, _ := os.Stat(p)
	meta1, _ := os.ReadFile(filepath.Join(s.Dir, "m", h1+".meta.json"))
	if _, err := s.Put("m", "wasm", []byte("v1"), Permissions{WASI: true}, nil); err != nil {
		t.Fatal(err)
	}
	fi2, _ := os.Stat(p)
	meta2, _ := os.ReadFile(filepath.Join(s.Dir, "m", h1+".meta.json"))
	if !os.SameFile(fi1, fi2) || string(meta1) != string(meta2) {
		t.Fatal("re-putting a hash rewrote it")
	}
	if err := s.SetCurrent("m", h1); err != nil {
		t.Fatal(err)
	}
	h2, _ := s.Put("m", "wasm", []byte("v2"), Permissions{}, nil)
	if err := s.SetCurrent("m", h2); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.Current("m"); c != h2 {
		t.Fatalf("current = %s", c)
	}
	if b, _, err := s.Read("m", h1); err != nil || string(b) != "v1" {
		t.Fatalf("old version lost: %q %v", b, err)
	}
	if err := s.SetCurrent("m", strings.Repeat("0", 64)); err == nil {
		t.Fatal("pointer moved to a missing hash")
	}
	if _, err := s.Put("../x", "wasm", nil, Permissions{}, nil); err == nil {
		t.Fatal("traversal module name accepted")
	}
}

func TestStorePointerWriteIsAtomic(t *testing.T) {
	s := testStore(t)
	var hs []string
	for _, c := range []string{"a", "b", "c", "d"} {
		h, _ := s.Put("m", "md", []byte(c), Permissions{}, nil)
		hs = append(hs, h)
	}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	bad := make(chan string, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			if c, err := s.Current("m"); err != nil {
				select {
				case bad <- err.Error():
				default:
				}
			} else if c != "" && !strings.Contains(strings.Join(hs, " "), c) {
				select {
				case bad <- c:
				default:
				}
			}
		}
	}()
	for i := 0; i < 300; i++ {
		if err := s.SetCurrent("m", hs[i%len(hs)]); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
	select {
	case b := <-bad:
		t.Fatalf("reader saw a partial pointer: %q", b)
	default:
	}
	ents, _ := os.ReadDir(filepath.Join(s.Dir, "m"))
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Fatalf("temp file left: %s", e.Name())
		}
	}
}

func TestStoreRollbackRules(t *testing.T) {
	s := testStore(t)
	narrow := Permissions{Hosts: []string{"a.example"}}
	h1, _ := s.Put("m", "wasm", []byte("v1"), narrow, nil) // pins narrow
	wide, _ := s.Put("m", "wasm", []byte("wide"), Permissions{Hosts: []string{"a.example", "b.example"}}, nil)
	h2, _ := s.Put("m", "wasm", []byte("v2"), Permissions{}, nil)
	s.SetCurrent("m", h2)
	if err := s.Rollback("m", wide, nil); !errors.Is(err, ErrPermission) {
		t.Fatalf("widening rollback: %v", err)
	}
	var tn taint = true
	if err := s.Rollback("m", h1, &tn); !errors.Is(err, ErrTainted) {
		t.Fatalf("rollback after taint: %v", err)
	}
	if c, _ := s.Current("m"); c != h2 {
		t.Fatal("refused rollback moved the pointer")
	}
	if err := s.Rollback("m", h1, nil); err != nil {
		t.Fatal(err)
	}
	if b, _, _ := s.ReadCurrent("m"); string(b) != "v1" {
		t.Fatalf("current after rollback = %q", b)
	}
}

func TestStoreTamperDetected(t *testing.T) {
	s := testStore(t)
	h, _ := s.Put("m", "wasm", []byte("v1"), Permissions{}, nil)
	s.SetCurrent("m", h)
	os.Chmod(filepath.Join(s.Dir, "m", h+".wasm"), 0o644)
	os.WriteFile(filepath.Join(s.Dir, "m", h+".wasm"), []byte("evil"), 0o644)
	_, _, err := s.ReadCurrent("m")
	if !errors.Is(err, ErrTampered) || !strings.Contains(err.Error(), "refusing to load") {
		t.Fatalf("tampered read: %v", err)
	}
	if err := s.Rollback("m", h, nil); !errors.Is(err, ErrTampered) {
		t.Fatalf("rollback to tampered: %v", err)
	}
}

func TestStorePruneKeepsCurrentAndN(t *testing.T) {
	s := testStore(t)
	var hs []string
	for i := 0; i < 8; i++ {
		h, _ := s.Put("m", "json", []byte{byte('a' + i)}, Permissions{}, nil)
		hs = append(hs, h)
	}
	s.SetCurrent("m", hs[0]) // the oldest is current
	removed, err := s.Prune("m", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 4 {
		t.Fatalf("removed %d, want 4", len(removed))
	}
	vs, _ := s.Versions("m")
	got := map[string]bool{}
	for _, v := range vs {
		got[v.Hash] = true
	}
	for _, h := range []string{hs[0], hs[7], hs[6], hs[5]} {
		if !got[h] {
			t.Fatalf("prune removed %s", h[:8])
		}
	}
	if len(vs) != 4 {
		t.Fatalf("%d versions left", len(vs))
	}
	if _, err := s.Prune("m", 0); err != nil {
		t.Fatal(err)
	}
	if vs, _ := s.Versions("m"); len(vs) != 1 || vs[0].Hash != hs[0] {
		t.Fatalf("prune 0 left %+v", vs)
	}
}

func TestStoreMetadataSidecar(t *testing.T) {
	s := testStore(t)
	p := Permissions{WASI: true, Hosts: []string{"x.example"}}
	h, _ := s.Put("m", "wasm", []byte("v"), p, json.RawMessage(`{"name":"m"}`))
	m, err := s.Meta("m", h)
	if err != nil || !m.Permissions.WASI || m.LoadedAt.IsZero() || !strings.Contains(string(m.Manifest), `"name": "m"`) {
		t.Fatalf("meta = %+v %v", m, err)
	}
}

func TestWasmPoolStoreRollbackRestoresBehavior(t *testing.T) {
	root := t.TempDir()
	good := codePlugin(t, "good", `{}`, echoWasm(0))
	cfg := []config.Plugin{{Path: good, WasmModules: []string{"m"}}}
	ls, err := Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	st := testStore(t)
	g := SessionGrant{Root: root}
	ctx := context.Background()
	var w strings.Builder
	pool := WasmPool{Store: st}
	defer pool.Close()
	wt := pool.Activate(ctx, ls, cfg, g, &w)
	if out, isErr, _ := wt.Call(ctx, "good__echo", "hi"); isErr || out != "hi" {
		t.Fatalf("v1: %q %v (%s)", out, isErr, w.String())
	}
	sn := StoreName("good", "m")
	v1, _ := st.Current(sn)

	os.WriteFile(filepath.Join(good, "wasm", "m.wasm"), echoWasm(3), 0o644)
	wt = pool.Activate(ctx, ls, cfg, g, &w)
	if out, isErr, _ := wt.Call(ctx, "good__echo", "hi"); !isErr || !strings.Contains(out, "exit code 3") {
		t.Fatalf("v2: %q %v", out, isErr)
	}
	if vs, _ := st.Versions(sn); len(vs) != 2 {
		t.Fatalf("store has %d versions", len(vs))
	}
	if err := st.Rollback(sn, v1, nil); err != nil {
		t.Fatal(err)
	}
	// A new process (fresh pool) and a re-activation both load the pointer's version.
	wt = pool.Activate(ctx, ls, cfg, g, &w)
	if out, isErr, _ := wt.Call(ctx, "good__echo", "hi"); isErr || out != "hi" {
		t.Fatalf("after rollback: %q %v", out, isErr)
	}
	fresh := WasmPool{Store: st}
	defer fresh.Close()
	wt = fresh.Activate(ctx, ls, cfg, g, &w)
	if out, isErr, _ := wt.Call(ctx, "good__echo", "hi"); isErr || out != "hi" {
		t.Fatalf("fresh after rollback: %q %v", out, isErr)
	}

	// Tampering with the current version is refused at startup.
	f := filepath.Join(st.Dir, sn, v1+".wasm")
	os.WriteFile(f, echoWasm(7), 0o644)
	w.Reset()
	tampered := WasmPool{Store: st}
	defer tampered.Close()
	if wt = tampered.Activate(ctx, ls, cfg, g, &w); len(wt.Specs) != 0 || !strings.Contains(w.String(), "does not match its hash") {
		t.Fatalf("tampered: %+v %q", wt.Specs, w.String())
	}
}

func TestWasmPoolStoreRefusesWiderNewVersion(t *testing.T) {
	root := t.TempDir()
	os.Mkdir(filepath.Join(root, "data"), 0o755)
	p := codePlugin(t, "p", `{}`, echoWasm(0))
	cfg := []config.Plugin{{Path: p, WasmModules: []string{"m"}}}
	ls, _ := Load(cfg)
	st := testStore(t)
	g := SessionGrant{Root: root}
	var w strings.Builder
	pool := WasmPool{Store: st}
	defer pool.Close()
	pool.Activate(context.Background(), ls, cfg, g, &w)
	pj := `{"$schema":"` + SchemaV1 + `","name":"p","extensions":{"tinyagent":{"wasm_modules":[` +
		`{"name":"m","file":"wasm/m.wasm","version":"2","exports":[{"name":"echo"}],"requires":{"allowed_paths":{"ro:data":"/data"}}}]}}}`
	os.WriteFile(filepath.Join(p, "plugin.json"), []byte(pj), 0o644)
	os.WriteFile(filepath.Join(p, "wasm", "m.wasm"), echoWasm(1), 0o644)
	ls, _ = Load(cfg)
	w.Reset()
	fresh := WasmPool{Store: st}
	defer fresh.Close()
	if wt := fresh.Activate(context.Background(), ls, cfg, g, &w); len(wt.Specs) != 0 || !strings.Contains(w.String(), "widens the first load's permissions") {
		t.Fatalf("wider: %+v %q", wt.Specs, w.String())
	}
	if vs, _ := st.Versions(StoreName("p", "m")); len(vs) != 1 {
		t.Fatal("wider version was stored")
	}
}

func TestDataModuleStore(t *testing.T) {
	st := testStore(t)
	var tn taint
	d := &DataModule{Name: "p", Kind: DataPrompt, Store: st, StoreName: "p", Taint: &tn}
	v1, err := d.Set([]byte("---\nname: one\n---\nhi"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Set([]byte("---\nname: two\n---\nhi")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(st.Dir, "p", v1.Hash+".md")); err != nil {
		t.Fatal(err)
	}
	v, err := d.RollbackTo(v1.Hash)
	if err != nil || v.Meta["name"] != "one" || d.Current().Hash != v1.Hash {
		t.Fatalf("rollback: %+v %v", v, err)
	}
	tn = true
	if _, err := d.RollbackTo(v1.Hash); !errors.Is(err, ErrTainted) {
		t.Fatalf("after taint: %v", err)
	}
	r := &DataModule{Name: "r", Kind: DataRetry, Store: st, StoreName: "r"}
	rv, _ := r.Set([]byte(`{"max_attempts":2,"backoff_ms":1}`))
	if _, err := os.Stat(filepath.Join(st.Dir, "r", rv.Hash+".json")); err != nil {
		t.Fatal(err)
	}
}
