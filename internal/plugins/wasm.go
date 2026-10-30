package plugins

// Code plugins run as WebAssembly modules through Extism (pure Go, wazero),
// never in the host process. Each module declares the host functions,
// filesystem paths and network hosts it needs in a WasmManifest; a load is
// allowed only when that declaration is a subset of the caller's
// Permissions. The declaration recorded at first load is pinned to the
// module name: a later Swap must stay within it, so a replacement can never
// gain access its predecessor didn't have. Everything is denied by default:
// no WASI, no paths, no hosts, and only declared-and-permitted host
// functions are linked.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"sync/atomic"

	extism "github.com/extism/go-sdk"
	"github.com/tetratelabs/wazero"
)

// Namespaces a guest module may import from.
const (
	HostUserNamespace = "extism:host/user" // tinyagent-provided host functions
	hostEnvNamespace  = "extism:host/env"  // Extism kernel (memory, input/output, gated http)
	wasiNamespace     = "wasi_snapshot_preview1"
)

// ErrPermission marks a load or swap refused because the module asks for
// more than it may have.
var ErrPermission = errors.New("permission denied")

// ErrTainted marks a load or swap refused because the session is locked
// down after untrusted content entered it.
var ErrTainted = errors.New("session is tainted: module loads and swaps are refused")

// Permissions is a set of capabilities: what a caller grants, or what a
// module declares it needs.
type Permissions struct {
	HostFuncs []string          `json:"host_functions,omitempty"`
	Paths     map[string]string `json:"allowed_paths,omitempty"` // host path -> guest path
	Hosts     []string          `json:"allowed_hosts,omitempty"`
	WASI      bool              `json:"wasi,omitempty"`
}

// WasmManifest describes one code module version.
type WasmManifest struct {
	Name     string      `json:"name"`
	Version  string      `json:"version"`
	Requires Permissions `json:"requires"`
}

// HostFunc is a host function the caller may offer to modules.
type HostFunc = extism.HostFunction

// Tainter reports whether the session is locked down (lockdown.State).
type Tainter interface{ Tainted() bool }

// subsetOf returns an error naming the first capability in p that grant
// does not cover.
func (p Permissions) subsetOf(grant Permissions) error {
	in := func(s string, set []string) bool {
		for _, x := range set {
			if x == s {
				return true
			}
		}
		return false
	}
	for _, f := range p.HostFuncs {
		if !in(f, grant.HostFuncs) {
			return fmt.Errorf("%w: host function %q", ErrPermission, f)
		}
	}
	for src, dst := range p.Paths {
		if g, ok := grant.Paths[src]; !ok || g != dst {
			return fmt.Errorf("%w: path %q", ErrPermission, src)
		}
	}
	for _, h := range p.Hosts {
		if !in(h, grant.Hosts) {
			return fmt.Errorf("%w: network host %q", ErrPermission, h)
		}
	}
	if p.WASI && !grant.WASI {
		return fmt.Errorf("%w: wasi", ErrPermission)
	}
	return nil
}

// checkImports compiles wasm without instantiating it and rejects any
// import the manifest didn't declare.
func checkImports(ctx context.Context, wasm []byte, req Permissions) error {
	rt := wazero.NewRuntime(ctx)
	defer rt.Close(ctx)
	cm, err := rt.CompileModule(ctx, wasm)
	if err != nil {
		return fmt.Errorf("invalid wasm module: %w", err)
	}
	for _, f := range cm.ImportedFunctions() {
		mod, name, _ := f.Import()
		switch mod {
		case hostEnvNamespace:
		case HostUserNamespace:
			if !contains(req.HostFuncs, name) {
				return fmt.Errorf("%w: module imports undeclared host function %q", ErrPermission, name)
			}
		case wasiNamespace:
			if !req.WASI {
				return fmt.Errorf("%w: module imports WASI %q without declaring wasi", ErrPermission, name)
			}
		default:
			return fmt.Errorf("%w: module imports %s.%s", ErrPermission, mod, name)
		}
	}
	return nil
}

func contains(set []string, s string) bool {
	for _, x := range set {
		if x == s {
			return true
		}
	}
	return false
}

// instance is one running module version. refs counts in-flight calls
// plus one for being current; it is closed when the count reaches zero.
type instance struct {
	manifest WasmManifest
	plugin   *extism.Plugin
	mu       sync.Mutex // Extism plugins are not goroutine-safe
	refs     atomic.Int64
	closed   atomic.Bool
}

func (in *instance) release() {
	if in.refs.Add(-1) == 0 {
		in.closed.Store(true)
		in.plugin.Close(context.Background())
	}
}

// WasmModule is a swappable code module. Calls go to the current instance;
// Swap replaces it without blocking calls already running.
type WasmModule struct {
	host    *WasmHost
	name    string
	pinned  Permissions // recorded at first load; never widened
	cur     atomic.Pointer[instance]
	swapMu  sync.Mutex
}

// WasmHost loads code modules for one caller.
type WasmHost struct {
	Grant Permissions
	Funcs []HostFunc // host functions the caller can offer, by name
	Taint Tainter    // optional

	mu      sync.Mutex
	modules map[string]*WasmModule
}

func (h *WasmHost) tainted() bool { return h.Taint != nil && h.Taint.Tainted() }

func (h *WasmHost) instantiate(ctx context.Context, m WasmManifest, wasm []byte) (*instance, error) {
	if err := checkImports(ctx, wasm, m.Requires); err != nil {
		return nil, err
	}
	var funcs []extism.HostFunction
	for _, f := range h.Funcs {
		if f.Namespace == HostUserNamespace && contains(m.Requires.HostFuncs, f.Name) {
			funcs = append(funcs, f)
		}
	}
	for _, name := range m.Requires.HostFuncs {
		found := false
		for _, f := range funcs {
			found = found || f.Name == name
		}
		if !found {
			return nil, fmt.Errorf("host function %q is permitted but not provided", name)
		}
	}
	hosts := append([]string{}, m.Requires.Hosts...)
	paths := map[string]string{}
	for k, v := range m.Requires.Paths {
		paths[k] = v
	}
	em := extism.Manifest{
		Wasm:         []extism.Wasm{extism.WasmData{Data: wasm, Name: "main"}},
		AllowedHosts: hosts,
		AllowedPaths: paths,
	}
	p, err := extism.NewPlugin(ctx, em, extism.PluginConfig{EnableWasi: m.Requires.WASI}, funcs)
	if err != nil {
		return nil, fmt.Errorf("instantiate %s@%s: %w", m.Name, m.Version, err)
	}
	in := &instance{manifest: m, plugin: p}
	in.refs.Store(1)
	return in, nil
}

// Load loads the first version of a module. The manifest must be a subset
// of the host's grant; it is then pinned as the module's permission list.
func (h *WasmHost) Load(ctx context.Context, m WasmManifest, wasm []byte) (*WasmModule, error) {
	if h.tainted() {
		return nil, ErrTainted
	}
	if !ValidName(m.Name) {
		return nil, fmt.Errorf("invalid module name %q", m.Name)
	}
	if err := m.Requires.subsetOf(h.Grant); err != nil {
		return nil, fmt.Errorf("load %s: %w", m.Name, err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.modules[m.Name]; ok {
		return nil, fmt.Errorf("module %q is already loaded; use Swap", m.Name)
	}
	in, err := h.instantiate(ctx, m, wasm)
	if err != nil {
		return nil, err
	}
	mod := &WasmModule{host: h, name: m.Name, pinned: clonePerms(m.Requires)}
	mod.cur.Store(in)
	if h.modules == nil {
		h.modules = map[string]*WasmModule{}
	}
	h.modules[m.Name] = mod
	return mod, nil
}

// LoadFile is Load with the module read from path.
func (h *WasmHost) LoadFile(ctx context.Context, m WasmManifest, path string) (*WasmModule, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return h.Load(ctx, m, b)
}

func clonePerms(p Permissions) Permissions {
	q := Permissions{WASI: p.WASI}
	q.HostFuncs = append([]string{}, p.HostFuncs...)
	q.Hosts = append([]string{}, p.Hosts...)
	sort.Strings(q.HostFuncs)
	q.Paths = map[string]string{}
	for k, v := range p.Paths {
		q.Paths[k] = v
	}
	return q
}

// Permissions returns the permission list pinned at first load.
func (m *WasmModule) Permissions() Permissions { return clonePerms(m.pinned) }

// Version is the current version string.
func (m *WasmModule) Version() string { return m.cur.Load().manifest.Version }

// Call runs an exported function on the current instance. A swap during
// the call doesn't affect it: it finishes on the instance it started on.
func (m *WasmModule) Call(ctx context.Context, fn string, input []byte) (uint32, []byte, error) {
	var in *instance
	for {
		in = m.cur.Load()
		if in == nil {
			return 0, nil, fmt.Errorf("module %q is closed", m.name)
		}
		if n := in.refs.Load(); n > 0 && in.refs.CompareAndSwap(n, n+1) {
			break
		}
	}
	defer in.release()
	in.mu.Lock()
	defer in.mu.Unlock()
	return in.plugin.CallWithContext(ctx, fn, input)
}

// Swap replaces the module with a new version. The new manifest must stay
// within the permissions pinned at first load (and the host's grant). The
// new instance is built first; on any error the old one keeps running.
// The old instance closes once its in-flight calls finish.
func (m *WasmModule) Swap(ctx context.Context, nm WasmManifest, wasm []byte) error {
	if m.host.tainted() {
		return ErrTainted
	}
	if nm.Name != m.name {
		return fmt.Errorf("swap %s: manifest names %q", m.name, nm.Name)
	}
	if err := nm.Requires.subsetOf(m.pinned); err != nil {
		return fmt.Errorf("swap %s: widens pinned permissions: %w", m.name, err)
	}
	if err := nm.Requires.subsetOf(m.host.Grant); err != nil {
		return fmt.Errorf("swap %s: %w", m.name, err)
	}
	m.swapMu.Lock()
	defer m.swapMu.Unlock()
	in, err := m.host.instantiate(ctx, nm, wasm)
	if err != nil {
		return err
	}
	old := m.cur.Swap(in)
	if old != nil {
		old.release()
	}
	return nil
}

// SwapFile is Swap with the module read from path.
func (m *WasmModule) SwapFile(ctx context.Context, nm WasmManifest, path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return m.Swap(ctx, nm, b)
}

// Close tears down the current instance and unregisters the module.
func (m *WasmModule) Close() {
	m.swapMu.Lock()
	defer m.swapMu.Unlock()
	if old := m.cur.Swap(nil); old != nil {
		old.release()
	}
	m.host.mu.Lock()
	delete(m.host.modules, m.name)
	m.host.mu.Unlock()
}
