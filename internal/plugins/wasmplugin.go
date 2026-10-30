package plugins

// Wiring of WebAssembly code modules into plugin loading. A plugin declares
// its modules in plugin.json:
//
//	"extensions": {"tinyagent": {"wasm_modules": [{
//	  "name": "calc", "file": "wasm/calc.wasm", "version": "1.0.0",
//	  "exports": [{"name": "add", "description": "Add two numbers"}],
//	  "requires": {"allowed_paths": {"ro:data": "/data"}, "allowed_hosts": ["api.example.com"], "wasi": true}
//	}]}}
//
// The configuration authorizes modules by name (plugins[].wasm_modules).
// Each authorized module is loaded through the Extism sandbox with its
// requires list checked against a grant derived from the session (see
// SessionGrant), and each export becomes a tool named <plugin>__<export>.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/vicgupta/tinyagent/internal/config"
	"github.com/vicgupta/tinyagent/internal/model"
)

// MaxWasmBytes caps a module file's size.
const MaxWasmBytes = 64 << 20

// WasmExport is one exported function offered as a tool.
type WasmExport struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

// WasmDecl is one module declared in plugin.json.
type WasmDecl struct {
	Name     string       `json:"name"`
	File     string       `json:"file"`
	Version  string       `json:"version"`
	Exports  []WasmExport `json:"exports"`
	Requires Permissions  `json:"requires"`
}

// WasmDecls returns the modules a plugin declares.
func WasmDecls(m *Manifest) ([]WasmDecl, error) {
	raw, ok := m.Extensions["tinyagent"]
	if !ok {
		return nil, nil
	}
	var ext struct {
		WasmModules []WasmDecl `json:"wasm_modules"`
	}
	if err := json.Unmarshal(raw, &ext); err != nil {
		return nil, fmt.Errorf("%s: extensions.tinyagent: %w", ManifestFile, err)
	}
	seen := map[string]bool{}
	for _, d := range ext.WasmModules {
		if !ValidName(d.Name) || seen[d.Name] {
			return nil, fmt.Errorf("%s: wasm module name %q is invalid or repeated", ManifestFile, d.Name)
		}
		seen[d.Name] = true
		if d.File == "" || filepath.IsAbs(d.File) {
			return nil, fmt.Errorf("%s: wasm module %s: file must be a relative path", ManifestFile, d.Name)
		}
		for _, e := range d.Exports {
			if !toolSafe(e.Name) {
				return nil, fmt.Errorf("%s: wasm module %s: export name %q is invalid", ManifestFile, d.Name, e.Name)
			}
		}
	}
	return ext.WasmModules, nil
}

func toolSafe(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !(c == '_' || c == '-' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// SessionGrant derives what the session lets a module have. It is never
// wider than the session: tinyagent offers no host functions to modules;
// paths must resolve inside Root and pass CheckPath (the session's
// file_access rules); hosts must be listed in Hosts (the session's
// sandbox_allowed_domains, exact entries only); WASI itself grants no
// filesystem or network.
type SessionGrant struct {
	Root      string
	Hosts     []string
	CheckPath func(abs string, write bool) error // nil allows
	Taint     Tainter
}

// resolve maps a module's requires list to absolute host paths and returns
// it with the grant that covers whatever of it the session allows.
func (g SessionGrant) resolve(pluginName string, req Permissions) (want, grant Permissions) {
	want = Permissions{HostFuncs: req.HostFuncs, Hosts: req.Hosts, WASI: req.WASI, Paths: map[string]string{}}
	grant = Permissions{WASI: true, Paths: map[string]string{}}
	for src, dst := range req.Paths {
		ro := strings.HasPrefix(src, "ro:")
		rel := strings.TrimPrefix(src, "ro:")
		abs := rel
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(g.Root, rel)
		}
		key := abs
		if ro {
			key = "ro:" + abs
		}
		want.Paths[key] = dst
		if g.pathOK(abs, !ro) {
			grant.Paths[key] = dst
		}
	}
	for _, h := range req.Hosts {
		if contains(g.Hosts, h) {
			grant.Hosts = append(grant.Hosts, h)
		}
	}
	return want, grant
}

func (g SessionGrant) pathOK(abs string, write bool) bool {
	root, err := filepath.EvalSymlinks(g.Root)
	if err != nil {
		return false
	}
	if _, err := ResolveInside(root, abs); err != nil {
		return false
	}
	return g.CheckPath == nil || g.CheckPath(abs, write) == nil
}

type wasmTool struct {
	mod    *WasmModule
	export string
}

type pooled struct {
	host *WasmHost
	mod  *WasmModule
	hash string
}

// WasmPool keeps loaded code modules for the process. Activate loads them
// on first use and, on a later activation (an ACP session, a reload),
// swaps in a module whose file changed, under the permission list pinned at
// first load.
type WasmPool struct {
	// Store, when set, keeps every version of each module; activation loads
	// the version its current pointer names (verifying its hash), and a
	// changed plugin file is added as a new version and made current.
	Store *ModuleStore

	mu      sync.Mutex
	modules map[string]*pooled // plugin root + "\x00" + module name
}

// WasmTools is the tool catalog of one activation.
type WasmTools struct {
	Specs []model.ToolSpec
	tools map[string]wasmTool
}

// Activate loads or refreshes every authorized module of the loaded
// plugins. A module that is refused is reported on w and skipped; the
// others still load.
func (p *WasmPool) Activate(ctx context.Context, ls []Loaded, cfg []config.Plugin, g SessionGrant, w io.Writer) *WasmTools {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.modules == nil {
		p.modules = map[string]*pooled{}
	}
	out := &WasmTools{tools: map[string]wasmTool{}}
	for i, l := range ls {
		if l.Err != nil || i >= len(cfg) || len(cfg[i].WasmModules) == 0 {
			continue
		}
		decls, err := WasmDecls(l.Manifest)
		if err != nil {
			fmt.Fprintf(w, "warning: plugin %s: %v\n", l.Manifest.Name, err)
			continue
		}
		byName := map[string]WasmDecl{}
		for _, d := range decls {
			byName[d.Name] = d
		}
		for _, name := range cfg[i].WasmModules {
			d, ok := byName[name]
			if !ok {
				fmt.Fprintf(w, "warning: plugin %s: wasm module %q is not declared in %s\n", l.Manifest.Name, name, ManifestFile)
				continue
			}
			mod, err := p.activate(ctx, l.Manifest, d, g)
			if err != nil {
				fmt.Fprintf(w, "warning: plugin %s wasm module %s refused: %v\n", l.Manifest.Name, d.Name, err)
				continue
			}
			prefix := strings.ReplaceAll(l.Manifest.Name, ".", "_")
			for _, e := range d.Exports {
				tn := prefix + "__" + e.Name
				if _, dup := out.tools[tn]; dup || len(tn) > 64 {
					fmt.Fprintf(w, "warning: plugin %s wasm module %s: tool name %q is invalid or repeated\n", l.Manifest.Name, d.Name, tn)
					continue
				}
				desc := e.Description
				if desc == "" {
					desc = "WebAssembly tool from plugin " + l.Manifest.Name
				}
				out.tools[tn] = wasmTool{mod: mod, export: e.Name}
				out.Specs = append(out.Specs, model.ToolSpec{Type: "function", Function: model.FunctionSpec{
					Name: tn, Description: desc, Parameters: map[string]any{"type": "object"}}})
			}
		}
	}
	sort.Slice(out.Specs, func(a, b int) bool { return out.Specs[a].Function.Name < out.Specs[b].Function.Name })
	return out
}

func (p *WasmPool) activate(ctx context.Context, m *Manifest, d WasmDecl, g SessionGrant) (*WasmModule, error) {
	if g.Taint != nil && g.Taint.Tainted() {
		return nil, ErrTainted
	}
	path, err := ResolveInside(m.Root, filepath.Join(m.Root, d.File))
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > MaxWasmBytes {
		return nil, fmt.Errorf("%s is not a regular file of at most %d bytes", d.File, MaxWasmBytes)
	}
	wasm, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(wasm)
	hash := hex.EncodeToString(sum[:])
	want, grant := g.resolve(m.Name, d.Requires)
	if len(want.HostFuncs) > 0 {
		return nil, fmt.Errorf("%w: host function %q is not offered by tinyagent", ErrPermission, want.HostFuncs[0])
	}
	if p.Store != nil {
		if wasm, hash, want, err = p.fromStore(m, d, wasm, hash, want); err != nil {
			return nil, err
		}
	}
	wm := WasmManifest{Name: d.Name, Version: d.Version + "+" + hash[:12], Requires: want}
	key := m.Root + "\x00" + d.Name
	if e, ok := p.modules[key]; ok {
		// Re-activation: the session's grant may be narrower now.
		if err := e.mod.Permissions().subsetOf(grant); err != nil {
			return nil, err
		}
		e.host.Grant = grant
		if e.hash != hash {
			if err := e.mod.Swap(ctx, wm, wasm); err != nil {
				return nil, err
			}
			e.hash = hash
		}
		return e.mod, nil
	}
	if err := want.subsetOf(grant); err != nil {
		return nil, err
	}
	h := &WasmHost{Grant: grant}
	mod, err := h.Load(ctx, wm, wasm)
	if err != nil {
		return nil, err
	}
	p.modules[key] = &pooled{host: h, mod: mod, hash: hash}
	return mod, nil
}

// fromStore adds the plugin's file to the store when it changed since the
// last ingest (refusing permissions wider than the first load's), then
// returns the verified current version and its permissions.
func (p *WasmPool) fromStore(m *Manifest, d WasmDecl, wasm []byte, hash string, want Permissions) ([]byte, string, Permissions, error) {
	sn := StoreName(m.Name, d.Name)
	if p.Store.Source(sn) != hash {
		if pin, err := p.Store.Pinned(sn); err == nil {
			if err := want.subsetOf(pin); err != nil {
				return nil, "", want, fmt.Errorf("new version widens the first load's permissions: %w", err)
			}
		}
		decl, _ := json.Marshal(d)
		if _, err := p.Store.Put(sn, "wasm", wasm, want, decl); err != nil {
			return nil, "", want, err
		}
		if err := p.Store.SetCurrent(sn, hash); err != nil {
			return nil, "", want, err
		}
		if err := p.Store.SetSource(sn, hash); err != nil {
			return nil, "", want, err
		}
	}
	b, meta, err := p.Store.ReadCurrent(sn)
	if err != nil {
		return nil, "", want, err
	}
	return b, meta.Hash, meta.Permissions, nil
}

// Close tears down every module.
func (p *WasmPool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, e := range p.modules {
		e.mod.Close()
		delete(p.modules, k)
	}
}

// Call runs a tool. The arguments JSON is the module's input and its
// output is the result; a non-zero return code marks an error. ok is false
// for names that aren't WebAssembly tools.
func (t *WasmTools) Call(ctx context.Context, name, raw string) (out string, isErr, ok bool) {
	if t == nil {
		return "", false, false
	}
	wt, ok := t.tools[name]
	if !ok {
		return "", false, false
	}
	rc, b, err := wt.mod.Call(ctx, wt.export, []byte(raw))
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return "error: canceled", true, true
		}
		return "error: wasm module " + wt.mod.name + ": " + err.Error(), true, true
	}
	if rc != 0 {
		return fmt.Sprintf("error: exit code %d: %s", rc, b), true, true
	}
	return string(b), false, true
}
