# Plugins

### Plugins

*Experimental.* tinyagent can load [Agent Plugins](https://agent-plugins.org): directories with a `plugin.json` manifest that bundle skills. List them in the config:

```yaml
skills: true
plugins:
  - path: /home/me/plugins/release-kit
```

- `path` must be absolute and an existing directory; the same directory listed twice (after resolving symlinks) is a config error, as are unknown keys. A profile's `plugins:` replaces the base list.
- The manifest is validated (`$schema`, `name`, optional `version`). A plugin with an invalid or missing manifest is skipped with a warning; unknown manifest fields only warn. Two plugins with the same `name` are a config error (exit 2).
- With `--skills`, skills under `<plugin>/skills/` join the catalog with the lowest precedence (see [Agent Skills](project-context.md#agent-skills)). Skill folders or a `skills` directory that resolve outside the plugin root are skipped with a warning.
- **MCP servers.** A plugin's `mcp.json` servers start only when listed under that plugin's `mcp_servers`, with an explicit allowlist, exactly like `mcp_servers` entries:

  ```yaml
  plugins:
    - path: /home/me/plugins/review-kit
      mcp_servers:
        - server: validator        # key in the plugin's mcp.json
          alias: reviewkit         # tools appear as reviewkit__<tool>
          tools: [lint, explain]
          resources: ["validator://rules/*"]   # optional
          discovery_timeout_seconds: 15        # optional
          call_timeout_seconds: 60             # optional
  ```

  - Only `stdio` servers run. `streamable-http` and `sse` entries are reported as unsupported and skipped. A `./` command must stay inside the plugin; other commands are bare names found on `PATH`, never run through a shell.
  - The server's working directory is the plugin root unless `cwd` (`./...`, `${PLUGIN_ROOT}/...` or `${PLUGIN_DATA}/...`) says otherwise. `PLUGIN_ROOT` and `PLUGIN_DATA` are set for the server (a plugin can't override them) and expanded once in `args`, `env` values and `cwd`. `env` values from `mcp.json` are set as given, on top of the usual minimal MCP environment.
  - `PLUGIN_DATA` is `<user config dir>/tinyagent/plugin-data/<name>` (for example `~/.config/tinyagent/plugin-data/review-kit`), created with mode 0700 before the first launch and kept between runs; delete it to reset the plugin's data.
  - An invalid `mcp.json` disables only that plugin's MCP servers, and an invalid entry only that entry, with a warning. A listed server that is missing, invalid or unsupported, a rejected plugin with `mcp_servers`, an alias already used by another server, or more than 8 servers in total exits 2 before any model request.
  - Plugin servers otherwise follow every `mcp_servers` rule: read-only runs and resumed runs don't start them, and lockdown, hooks and tool output handling apply.
- `-v` prints each loaded plugin; warnings go to stderr and, with `--json`, as `warning` events.
- **Inspection.** `tinyagent --list-plugins [-c <file>] [--profile <name>]` prints, per plugin, its path, name and version and `ok`, `warnings: N` or `rejected: <reason>`; each plugin skill (and which skill shadows it); and each `mcp.json` server with its type and state: `authorized as <alias> (N tools)`, `not authorized`, `unsupported transport` or `invalid: <reason>`. It makes no model request, needs no credentials, starts no servers or hooks and writes nothing (not even `PLUGIN_DATA`). It exits 0 when every plugin loads, 1 when a plugin is rejected or a configured server is missing or invalid, and 2 on a config error or when combined with `-t`, `--resume` or another mode. `--check` prints a `plugins:` line (configured, rejected and authorized server counts) and fails on the same errors that stop a run.
- **Records.** `run.started` (and so `--json` and `--log-db`) and `-o` trajectories gain `plugins`: each loaded plugin's `name`, `version`, `path`, `plugin_json_sha256`, `mcp_json_sha256` (when it has one), `skills` (its skills in the catalog, with `--skills`) and `mcp_servers` (`server`, `alias`). `mcp.json` `env` values are never recorded. With no `plugins` key nothing is added.
- **Resume.** A changed skills catalog refuses to resume as usual. A changed `plugin.json` or `mcp.json` digest only warns, naming the plugin.
- Versions of tinyagent without plugin support reject the `plugins` key as unknown.

**Trust:** a plugin is code and prompt input you chose to install. Its MCP servers run with your permissions and are not sandboxed by `--sandbox`, like configured MCP servers. Only list plugins you trust.

## WebAssembly plugin modules

Plugins can also ship code modules that run as WebAssembly through [Extism](https://extism.org), never inside the tinyagent process. Full reference: [wasm-plugins.md](wasm-plugins.md).

- **Declaring and loading.** `plugin.json` declares modules under `extensions.tinyagent.wasm_modules`, and the config authorizes them by name (`wasm_modules`). Each module's requested permissions (paths, hosts, WASI, host functions) must fit a grant derived from the session (`--cwd` and `file_access` paths, `sandbox_allowed_domains` hosts, no host functions); a refused module prints a warning and other plugins still load. Exports become `<plugin>__<export>` tools, like MCP tools. They are refused once the session is tainted, and `--read-only` runs load no modules. When a plugin is reactivated, unchanged modules are reused and changed ones are swapped in with the permissions from the first load pinned.
- **Version store.** Every module version is kept, append-only, under `<cwd>/.tinyagent/modules/<plugin>__<module>/` as `<sha256>.<ext>` files that are never overwritten, each with a `.meta.json` sidecar. `pinned.json` records the first-load permissions and a `current` pointer (written atomically) selects the active version. A changed module file is stored as a new version (refused if it asks for more than the first load); a stored file whose hash doesn't match is refused.
- **Rollback.** `tinyagent plugins rollback <module> <sha256>` (`--plugins-rollback <module>@<sha256>`) moves the pointer back to a stored version. It is refused if that version needs wider permissions than the first load, if its hash doesn't match, or after taint.
- **Prune.** `tinyagent plugins prune [--keep N]` (`--plugins-prune [--plugins-keep N]`) removes old versions, keeping the current one and the newest N others (default 5). The current version is never removed.
- **Go source modules (TinyGo).** A module can be given as Go source (`"source": "<dir>", "lang": "go"`). tinyagent compiles it to `.wasm` with TinyGo inside the OS sandbox (writes confined, network denied), stores the build in the version store and loads it through the same permission check. **TinyGo is an external dependency, shipped separately by the TinyGo project and not part of tinyagent:** it is never embedded in the tinyagent binary or repository. tinyagent downloads a pinned TinyGo release on first use (or with `tinyagent tinygo install`), verifies its SHA-256 checksum before extracting it, and caches it under the user cache directory (`tinyagent/toolchains/`). `tinyagent tinygo update` moves to the version pinned in the current build, and `tinyagent tinygo status` shows what is installed. To use your own TinyGo, set the `tinygo` config key or `TINYAGENT_TINYGO`. TinyGo also needs a Go toolchain on `PATH`.
