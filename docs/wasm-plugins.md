# WebAssembly code plugins

Code plugins run as WebAssembly modules through [Extism](https://extism.org)
(pure Go, on wazero; no cgo), never in the tinyagent process. The API lives in
`internal/plugins` (`wasm.go`, `datamodule.go`).

- **Manifest.** Each module version has a `WasmManifest` listing what it
  needs: `host_functions` (in the `extism:host/user` namespace),
  `allowed_paths`, `allowed_hosts`, and `wasi`.
- **Deny by default.** `WasmHost.Load` succeeds only when the manifest is a
  subset of the caller's grant. The module's imports are inspected before
  instantiation: importing an undeclared host function, WASI without `wasi`,
  or any unknown namespace is refused. Only declared host functions are
  linked; paths and hosts map to Extism's `AllowedPaths`/`AllowedHosts`.
- **Swap.** `WasmModule.Swap`/`SwapFile` build the new instance first, switch
  an atomic pointer, then release the old one, which closes after its
  in-flight calls finish. The permission list recorded at first load is
  pinned: a new version that asks for anything outside it is rejected and
  the old version keeps running.
- **Lockdown.** Once the session is tainted, loads and swaps return
  `ErrTainted`.
- **Data modules.** Prompts, skill files (markdown with YAML front-matter)
  and retry policies (JSON or TOML) load without the sandbox via
  `DataModule.Set`: schema check, sha256 content version, atomic switch, and
  one-step `Rollback`.

## Declaring a code module in a plugin

A plugin declares modules under `extensions.tinyagent.wasm_modules` in
`plugin.json`; the file path is relative to the plugin root and must stay
inside it:

```json
{
  "$schema": "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
  "name": "kit",
  "extensions": {"tinyagent": {"wasm_modules": [{
    "name": "m", "file": "wasm/m.wasm", "version": "1.0.0",
    "exports": [{"name": "echo", "description": "Echo the arguments"}],
    "requires": {"allowed_paths": {"ro:data": "/data"}, "allowed_hosts": ["api.example.com"], "wasi": true}
  }]}}
}
```

The configuration authorizes modules by name, like `mcp_servers`; modules
not listed are never loaded:

```yaml
plugins:
  - path: /abs/path/to/kit
    wasm_modules: [m]
```

The grant is derived from the session and is never wider: tinyagent offers
no host functions to plugin modules, `allowed_paths` (relative to `--cwd`,
`ro:` for read-only) must stay inside `--cwd` and pass `file_access`, and
`allowed_hosts` must be exact entries of `sandbox_allowed_domains`. A
refused module is reported as `warning: plugin <name> wasm module <m>
refused: ...` and the other plugins still load. Read-only runs load none.

Each export becomes a tool `<plugin>__<export>` alongside MCP tools; the
tool's JSON arguments are the module input, its output is the result, and a
non-zero return code is an error. Like MCP tools they are refused once the
run is tainted. Each activation (a run, or each ACP session in one process)
reloads modules: an unchanged file is reused and a changed one is swapped in
under the permissions pinned at first load.

## Version store, rollback and prune

Every module version TinyAgent loads is kept in an append-only store under
`<cwd>/.tinyagent/modules/` (keep `.tinyagent/` out of git):

```
.tinyagent/modules/<plugin>__<module>/
  <sha256>.wasm | .md | .json | .toml   the version; never overwritten
  <sha256>.meta.json                    manifest, permissions, load time
  pinned.json                           permissions of the first load
  current                               active hash (temp file + rename)
  source                                hash last read from the plugin
```

When a plugin's module file changes, the new content is added under its
hash and `current` moves to it; a new version that needs wider permissions
than the first load is refused. Every activation loads the version named by
`current` and checks its hash, so a modified stored file is refused with
"stored module does not match its hash". Data modules given a store
(`DataModule.Store`) are versioned the same way.

```
tinyagent plugins rollback <plugin>__<module> <sha256>   # or --plugins-rollback <module>@<sha256>
tinyagent plugins prune [--keep 5]                       # or --plugins-prune [--plugins-keep 5]
```

Rollback only moves `current`; it is refused when the target needs wider
permissions than the first load, when the target fails its hash check, and,
inside a run, once the run is tainted. Prune keeps each module's current
version and the newest `--keep` others (default 5); current is never
removed.

## Go source modules and the TinyGo toolchain

**TinyGo is an external dependency, not part of TinyAgent.** TinyAgent
never embeds TinyGo in its binary (no `go:embed`, no bundled archives) and
the repository never contains it. TinyAgent downloads the official TinyGo
release separately, on first use or with `tinyagent tinygo install`, and
verifies it against a SHA-256 pinned in the source before extracting it.

A module can be declared as Go source instead of a prebuilt `.wasm`:

```json
{"name": "text", "source": "go/text", "lang": "go", "version": "1",
 "exports": [{"name": "upper", "description": "Uppercase the input"}],
 "requires": {"wasi": true}}
```

`source` is a directory relative to the plugin root (no `file`). Its
top-level `.go` files (no tests, no `go.mod`, standard library only) form a
`package main` with no `main` function; each export is a
`//go:wasmexport <name>` function returning `int32` (0 for success).
TinyAgent adds a small shim providing `input() []byte` (the call's argument
JSON) and `output([]byte)` (the result):

```go
package main

import "bytes"

//go:wasmexport upper
func upper() int32 {
	output(bytes.ToUpper(input()))
	return 0
}
```

TinyGo's `wasip1` runtime imports WASI, so declare `"wasi": true`.

When the plugin loads, TinyAgent runs `tinygo build -target=wasip1
-buildmode=c-shared -no-debug -opt=z` in a temporary directory under the OS
sandbox (Landlock/Seatbelt) with writes confined to that directory and the
network denied (`GOPROXY=off`). It refuses to build where the sandbox is
unavailable and after the session is tainted. The result goes through the
version store (above) and the usual permission check; an unchanged source
reuses the stored build (and a rollback) instead of recompiling. TinyGo
itself calls `go`, so a Go toolchain must be on `PATH`.

### Managing TinyGo

- `tinyagent tinygo install` (`--tinygo-install`) downloads the pinned
  release into `os.UserCacheDir()/tinyagent/toolchains/tinygo-<version>/`,
  verifies its checksum (a mismatch is refused and leaves nothing cached)
  and extracts it safely (no path traversal or escaping links). With a
  verified copy cached it says so and downloads nothing.
- `tinyagent tinygo update` (`--tinygo-update`) installs the version pinned
  in this TinyAgent build if needed and removes other cached versions.
- `tinyagent tinygo status` (`--tinygo-status`) prints the pinned version
  and checksum, whether it is installed and verified, and the path in use.

To use your own TinyGo instead, set the `tinygo` config key or the
`TINYAGENT_TINYGO` environment variable to its executable; nothing is
downloaded then.

### Bumping the pinned TinyGo version

1. Pick a release on <https://github.com/tinygo-org/tinygo/releases>.
2. Download `tinygo<ver>.<os>-<arch>.tar.gz` for linux/darwin × amd64/arm64
   and run `sha256sum` on each (cross-check with the release's checksums).
3. Update `Version` and `Checksums` in `internal/tinygo/toolchain.go`.
4. Run `go test ./internal/tinygo ./cmd/tinyagent` with the new TinyGo
   installed (`tinyagent tinygo update`) so the integration test runs (it
   skips without TinyGo; `TINYAGENT_REQUIRE_TINYGO=1` makes that a failure).
