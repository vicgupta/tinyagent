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
