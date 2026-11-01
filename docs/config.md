# Configuration

## Config

tinyagent reads its config from **`tinyagent.yaml` in the project directory** (`--cwd`, default `.`). Run `tinyagent init` (or `tinyagent --init-config`) to write a commented starter there; it writes with mode 0600, refuses an existing file or a symlink (exit 2), and makes no model request. `tinyagent init --force` replaces an existing file atomically (still refusing a symlink); `tinyagent init -c PATH` writes to PATH instead, creating parent directories. It cannot be combined with `-t`, `--resume`, `--rewind`, or `--update`.

**Lookup order:**

1. `-c PATH`, if given. A missing or unreadable file is an error, with no fallback.
2. `tinyagent.yaml` in `--cwd`, exactly that directory: tinyagent does not look in parent directories, and the process directory doesn't matter when `--cwd` is set.
3. The built-in defaults.

A `tinyagent.yaml` that exists but can't be read or parsed is an error naming the file; nothing falls back. `--check` and `-v` show which file was used: `<path> (cwd)`, `<path> (from -c)` or `none (built-in defaults)`.

**The per-user config is no longer read.** Earlier versions loaded `~/.config/tinyagent/config.yaml` (macOS `~/Library/Application Support/tinyagent/config.yaml`, Windows `%AppData%\tinyagent\config.yaml`). That file is now ignored; when it exists and no `tinyagent.yaml` was found, tinyagent prints `note: user config <path> is no longer read; run tinyagent init --migrate to copy it to ./tinyagent.yaml`. `tinyagent init --migrate` copies it into `--cwd` once (it refuses an existing `tinyagent.yaml` unless `--force`, and never deletes the original). To keep one config for many projects, pass it with `-c`, e.g. an alias `tinyagent -c ~/.config/tinyagent/config.yaml`.

**Precedence**, highest first: explicit command-line flags, environment variables (such as `$AGENT_MODEL`, which still outranks the file's `model:`), the selected `--profile`, the config file, the built-in defaults. A flag counts only when given, so `--read-only=false` overrides `read_only: true`, and a repeatable list flag (`--fallback-model`, `--verify-check`, ...) replaces the file's list rather than adding to it.

### Trusting a project config

A `tinyagent.yaml` found in `--cwd` usually comes with the repository, so by default it can't make tinyagent run programs or loosen your security settings. These keys are **ignored with a warning** naming them, unless you pass `--trust-config`:

`mcp_servers`, `hooks`, `command_tools`, `lsp.servers`, `plugins`, `tinygo`, `claude_code.path`, `codex_exec.path`, `opencode.path`, `handoff.startup_command`, `verify_on_stop.checks`, `attempts.checks`, `acp.client_mcp_servers`, `container.run_args`, `permissions`, `file_access`, `command_env`, `redact_secrets`, `untrusted_content`, `memory`.

There is no prompt, so `--trust-config` works the same in CI. Pass it only for a repository you trust, after reading its `tinyagent.yaml`. A file given with `-c` is always trusted (you chose it), even if it is the same file. Command-line flags such as `--verify-check` and `--attempt-check` still work without `--trust-config`. Elsewhere in these docs, "read only from `-c`, never from the repository" means: from `-c`, or from `./tinyagent.yaml` with `--trust-config`.

**Secrets.** `tinyagent.yaml` may be committed, so never put API keys or tokens in it: name environment variables instead (`api_key_env`, `env:` lists, `headers_env`). If the file holds personal settings, add `tinyagent.yaml` to `.gitignore`.

Every key is optional, but an unrecognized key (a typo like `step_limt`) is an error naming the file and the key, rather than silently ignored. Command-line flags override the file.

Set `read_only: true` to make repository analysis the default. `--read-only` overrides the config value, including `--read-only=false`. In this mode, executor dispatch allows only `read_file`, `list_files`, and `grep`; writes, edits, shell commands, and background process tools are denied even if task text or repository instructions request them. Denials are returned to the model as tool errors. The restriction is model tool authority, not an OS sandbox (for that, see `--sandbox` under Sandboxing commands): an explicit `-o` trajectory destination can be written by the CLI, provider requests still send their context to the selected service, the Codex provider may refresh its credential file, the [`claude-code`, `codex-exec` and `opencode`](models.md#cli-handoff-providers) providers map read-only onto their CLI's own restriction (see there), and the opt-in [`web_fetch`](long-runs.md#web-fetch) tool, which only reads allowlisted URLs, stays available when enabled, as does the read-only [`lsp`](long-runs.md#language-servers-lsp) tool (its servers are still started and write logs under `.tinyagent/lsp`). `--update` is an administrative action outside task-tool policy. Readable secrets (unless hidden with [`file_access`](permissions.md#restricting-file-access) rules), symlinks that point outside `--cwd`, existing lexical path behavior, and AGENTS.md discovery retain their existing limitations; read-only mode does not make an untrusted repository safe.

`model` saves a default model, so `tinyagent -t "fix the failing test"` needs no `-m`. The model is chosen in this order: `-m`/`--model` if given (even `-m openai/gpt-4o`), then a nonempty `$AGENT_MODEL`, then a nonempty `model` in the config, then `openai/gpt-4o`.

```yaml
model: ollama/llama3.1    # same <provider>/<model> syntax as -m
system_prompt: |          # {{cwd}} and {{os}} are substituted
  You are a coding agent...
read_only: true            # expose only read_file, list_files, and grep
sandbox: false             # OS-confine bash/start_process; see Sandboxing commands
# sandbox_backend: auto    # Linux engine: auto (bwrap + Landlock, else Landlock), landlock or bwrap
# sandbox_allowed_domains: [proxy.golang.org]  # with sandbox, the only hosts commands may reach
environment: local         # or container; see Execution environments
command_env:               # omitted by default (inherit everything); see Command environment
  inherit: core            # all, core or none
  exclude_secrets: true
file_access:               # omitted by default (no rules); see Restricting file access
  deny_read: [".env", "*.pem", "secrets/"]
  deny_write: [go.sum, "vendor/"]
permissions:               # omitted by default (no policy); see Permissions
  default: allow           # allow (default), ask or deny, when no rule matches
  non_interactive: deny    # what an ask becomes with nobody to answer: deny (default) or allow
  rules:                   # a profile's rules replace the base list
    - {tool: bash, command: "go test *", decision: allow}
    - {tool: "bash|start_process", command: "git push *", decision: ask}
    - {tool: bash, command: "curl *", decision: deny, message: "use web_fetch"}
    - {tool: "write_file|edit_file|apply_patch", path: "migrations/**", decision: deny}
    - {tool: web_fetch, host: "*.internal.example.com", decision: ask}
redact_secrets:            # mask known secret values in tool output; see Redacting secrets from tool output
  enabled: false           # same as --redact-secrets
  env: [DATABASE_URL]      # more variables (names or globs) whose values are secrets
  allow: [PUBLIC_KEY]      # secret-named variables not to mask
  patterns: false          # also mask token shapes (sk-..., ghp_..., Bearer ..., xox?-...)
additional_dirs:           # omitted by default (only --cwd); see Additional directories
  - {path: /home/me/src/shared-lib, mode: read}
step_limit: 50
cost_limit: 2.0
token_limit: 0            # max input+output tokens per run, delegates and workers included; 0 = unlimited
wall_time_seconds: 1800
max_format_errors: 3      # consecutive malformed-call steps before RepeatedFormatError (>= 1)
bash_timeout_seconds: 120
max_output_chars: 30000   # tool output is truncated (bytes, cut on UTF-8 boundaries), keeping head and tail
max_output_tokens: 0      # per-request model output budget; 0 = provider default (8192 on anthropic)
max_truncations: 3        # consecutive cut-off responses before OutputTruncated (>= 1)
max_background_processes: 4   # start_process processes running at once (0 uses 4)
input_price_per_mtok: 3   # override the built-in price table
output_price_per_mtok: 15
context_window: 200000    # tokens; overrides the built-in table, used for compaction
plan:
  enabled: false          # give the model the update_plan tool (same as --plan)
skills: false             # discover Agent Skills and offer the skill tool (same as --skills)
apply_patch: false        # offer the apply_patch tool (same as --apply-patch)
scoped_agents_md: false   # deliver AGENTS.md files below --cwd on first access (same as --scoped-agents-md)
memory:                   # project memory; see Project memory (-c, or ./tinyagent.yaml with --trust-config)
  enabled: false          # load active entries into the system prompt (same as --memory)
  approval: pending       # pending or auto: policy for entries the remember tool saves
  max_writes_per_run: 5   # entries one run may add, replace or forget; >= 1
  max_entries: 50         # active entries kept per project; >= 1
  max_bytes: 8192         # cap on the Project memory prompt section; 1 to 65536
  backend: markdown       # markdown or sqlite
  path: .tinyagent/memory # markdown folder; relative to --cwd, must stay inside it
  sqlite_path: .tinyagent/memory/memory.sqlite  # relative to --cwd, must stay inside it
handoff:                  # session handoff file; see Session handoff (-c, or ./tinyagent.yaml with --trust-config)
  enabled: false          # read the file at start and rewrite it at the end (same as --handoff)
  path: .tinyagent/handoff.md  # relative to --cwd; must stay inside it
  max_bytes: 16384        # bytes of the file loaded into the first message; >= 1
  startup_command: ""     # optional shell command run in --cwd before the first turn
  startup_timeout_seconds: 120  # bound on the startup command; >= 1
  startup_max_output: 8192      # bytes of startup output (the tail) given to the model; >= 1
subagents:
  enabled: false          # offer the delegate tool: a read-only researcher subagent (same as --subagents)
  model: ""               # child model, e.g. openai/gpt-5-mini; empty uses the parent model
  max_steps: 30           # step cap per delegated task; 0 means the default (30)
models:                  # per-role model routing; see Model roles (all optional, off by default)
  auxiliary:              # models for helper calls outside the agent loop
    model: ""             # default for compaction and review (same as --aux-model); empty uses the main model
    tasks: {}             # per-task override: compaction, review (same as --route task=spec)
    fallback_models: []   # the role's own fallback chain, at most 3; never inherits fallback_models
  delegated:              # models for delegated children
    model: ""             # default for delegate and worker children; empty uses the main model
    tasks: {}             # per-task override: delegate, worker (e.g. worker: openai/gpt-5-mini)
    fallback_models: []   # the role's own fallback chain for children, at most 3
chat_history: true       # record chat input in <cwd>/history/chat_history (false is the same as --no-chat-history); see Interactive chat
chat_markdown: true      # render Markdown in chat replies (false shows raw Markdown, like /raw); see Interactive chat
ask_user:
  enabled: false          # offer the ask_user tool (same as --ask-user); see Asking for input
  max_questions: 3        # questions per run, counted across resumes; 1 to 20
web_fetch:
  enabled: false          # offer the web_fetch tool (same as --web-fetch)
  allowed_domains: []     # hosts it may read, e.g. [pkg.go.dev, "*.python.org"]; required when enabled
  max_bytes: 2097152      # response bytes read per fetch (2 MiB); must be >= 1
  timeout_seconds: 20     # per fetch, including redirects; 1 to 120
lsp:                      # language servers; see Language servers (LSP)
  enabled: false          # same as --lsp
  diagnostics_after_edit: true  # append diagnostics to edit results; false keeps only the lsp tool
  diagnostics_wait_ms: 3000     # longest wait for diagnostics after an edit; 100 to 30000
  max_diagnostics: 20     # per file; 1 to 200
  severity: error         # error, or warning (errors and warnings)
  servers: []             # name, command, args, extensions, env, initialization_options, startup_timeout_seconds
images:
  enabled: false          # view_image tool and MCP image pass-through (same as --images)
max_image_bytes: 5242880  # largest image sent to the model (5 MiB); must be >= 1
tool_output_files:        # save over-limit tool output; see Tool output files
  enabled: false          # same as --tool-output-files
  dir: .tinyagent/tool-output   # relative to --cwd; must stay inside it
  max_runs: 20            # keep this many earlier run directories; 0 keeps all
snapshots:                # workspace snapshots for --rewind (same as --snapshots); `snapshots: true` also works
  enabled: false
  max_file_bytes: 2097152 # larger files are skipped and never restored (2 MiB); must be >= 1
log_db:                   # opt-in SQLite event log; see Event log database
  enabled: false
  path: .tinyagent/tinyagent.db   # relative to --cwd
  level: metadata         # metadata (no bodies) or full (redacted bodies)
  max_days: 30            # prune runs older than this; 0 keeps all
  max_runs: 500           # keep at most this many runs; 0 keeps all
  max_payload_bytes: 65536
otel:                     # opt-in OpenTelemetry trace export (same as --otel); see OpenTelemetry export
  enabled: false
  endpoint: ""            # base URL; empty uses OTEL_EXPORTER_OTLP_(TRACES_)ENDPOINT, else localhost:4318
  protocol: http/protobuf # or http/json
  headers_env: []         # env vars holding k=v header lists; values never come from this file
  timeout: 10s
  service_name: tinyagent
  content: false          # include redacted prompt/tool/assistant text
  max_attribute_bytes: 4096
reasoning_effort: medium  # optional; see Reasoning; --reasoning-effort wins
fallback_models: []       # optional, at most 3; see Model fallback; --fallback-model replaces it
openai_api: auto          # openai provider transport: auto, responses, or chat; --openai-api wins
claude_code:              # optional; claude-code provider, see CLI handoff providers in models.md
  path: ""                # claude binary; empty looks up `claude` on PATH; ~/ is expanded
codex_exec:               # optional; codex-exec provider
  path: ""                # codex binary; empty looks up `codex` on PATH
opencode:                 # optional; opencode provider
  path: ""                # opencode binary; empty looks up `opencode` on PATH
providers:                # omitted by default; see Named providers
  - id: gateway           # use as -m gateway/<model>
    api: chat             # chat, responses, or anthropic
    base_url: https://llm.internal.example.com/v1
    api_key_env: GATEWAY_API_KEY   # optional; names only, never the key itself
    models:
      vendor/model-x: {input_price_per_mtok: 2, output_price_per_mtok: 8, context_window: 128000}
mcp_servers:                 # omitted by default; each entry runs a trusted local executable
  - alias: docs
    command: /usr/local/bin/team-docs-mcp
    args: [--stdio]
    tools: [search, fetch]   # explicit authorization; all entries are required to exist
    resources: ["docs://guides/*", "schema://db/orders"]   # optional resource URI allowlist; see Local MCP tools
    env: [TEAM_DOCS_TOKEN]   # names only; tinyagent copies their current values to the child
    discovery_timeout_seconds: 15
    call_timeout_seconds: 60
command_tools:               # omitted by default; see Command tools below
  - name: run_tests
    description: Run Go tests for the given package patterns. Use instead of bash for tests.
    parameters:              # JSON Schema, same subset as --output-schema
      type: object
      properties:
        packages: {type: array, items: {type: string, pattern: "^\\./[A-Za-z0-9_./-]*(\\.\\.\\.)?$"}, minItems: 1}
      required: [packages]
      additionalProperties: false
    argv: [go, test, -count=1, "{{packages}}"]   # no shell; each placeholder is a whole element
    timeout_seconds: 300     # default bash_timeout_seconds, range 1..600
    dir: .                   # optional, relative to --cwd and inside it
hooks:                       # omitted by default; see Tool hooks below
  pre_tool:
    - match: "bash|start_process"   # Go regexp matched against the whole tool name; omit to match every tool
      command: /usr/local/bin/tinyagent-policy
      args: [--profile, ci]
      env: [POLICY_TOKEN]           # names only, as in mcp_servers
      timeout_seconds: 10           # default 30, range 1..600
      on_error: deny                # pre_tool only; deny (default) or allow
  post_tool:
    - match: "write_file|edit_file"
      command: /usr/local/bin/fmt-check
      timeout_seconds: 60
  stop:                         # runs before a successful finish; no match or on_error
    - command: /usr/local/bin/verify-tests
      timeout_seconds: 300
  max_stop_continuations: 5     # default 5, range 1..50
verify_on_stop:              # off by default; see Verify before stop below
  enabled: false             # --verify-on-stop
  commands: [go test ./...]  # --verify-command (repeatable) replaces this list
  patterns: []               # Go regexps; built-in test/build patterns when commands, checks and patterns are all empty
  checks: []                 # --verify-check (repeatable) replaces this list; computational checks run in order at stop, cheapest first
  review: false              # --verify-review; after every check passes, one model review of the changes (report only; needs checks)
  max_reprompts: 2           # --max-verify-reprompts; default 2, range 1..10
attempts:                    # omitted by default; only used with --attempts, see Best-of-N attempts below
  checks:                    # replaced as a whole by --attempt-check
    - command: go test ./...
      timeout_seconds: 600   # default 600, range 1..3600
  stop_on_pass: true         # default true; false runs all N attempts
  keep: all                  # all (default), failed or none: which non-winning worktrees survive a win
acp:                         # --acp editor mode; see Editor integration (ACP)
  client_mcp_servers: false  # start the MCP servers the editor sends with session/new
profiles:                    # omitted by default; named overlays selected with --profile, see Profiles below
  review:
    model: openai/gpt-5-mini
    read_only: true
```
### Workflows

`tinyagent.yaml` has no workflow-specific keys. Stage settings (`model`, `profile`, `step_limit`, `cost_limit`, `token_limit`, `time_limit`, `retries`, `check_timeout`) live in the workflow file passed to `--workflow`, and each stage otherwise uses this config, including `profiles` and `verify_on_stop`. See [Workflows](workflows.md#flags-and-settings).

### Verify before stop

`verify_on_stop` (or `--verify-on-stop`, `--verify-command`, `--verify-check`, `--max-verify-reprompts`) keeps a run that changed files from finishing until a test or build command has passed, re-prompting the model up to `max_reprompts` times and otherwise marking the run unverified. `verify_on_stop.checks` lists computational checks (lint, type check, tests) that tinyagent runs itself, in order, when the model finishes with unverified changes; see [Computational checks](verify.md#computational-checks). `verify_on_stop.review` (or `--verify-review`) then asks the model once to review the changes, report only; see [Inferential review](verify.md#inferential-review). See [Verify before stop](verify.md#verify-before-stop) for the full rules.

### Profiles

`profiles` holds named overlays in the same config file tinyagent already loads (`./tinyagent.yaml`, or the `-c` file). `--profile <name>` applies one of them over the file's base settings for that run, so you can switch between setups such as a cheap read-only review, CI with a sandbox and stop hooks, or a local model with one flag. A profile only lists the settings that differ from the base, and it can set anything the file can, including config-only settings like `command_env`, `file_access`, `hooks`, `mcp_servers`, `lsp` and `providers`.

```yaml
model: anthropic/claude-sonnet-4-5
cost_limit: 5
# providers, mcp_servers, ... defined here are shared by every profile
profiles:
  review:
    model: openai/gpt-5-mini
    read_only: true
    cost_limit: 0.5
  ci:
    sandbox: true
    command_env: {inherit: core}
    step_limit: 80
    hooks:
      stop:
        - command: /usr/local/bin/verify-tests
          timeout_seconds: 300
```

```sh
tinyagent --profile review -t "review this change"
tinyagent --profile ci -t - -o run.json --json < task.md
```

- **Precedence**, lowest to highest: built-in defaults, the base config file, the selected profile, explicit flags. A profile's `model` takes the place of the config `model` in the usual model order, so `-m` and a nonempty `$AGENT_MODEL` still win over it.
- **Merging**: mappings merge key by key, recursively, so a profile with `web_fetch: {enabled: true}` keeps the base `web_fetch.allowed_domains`. Scalars and lists replace the base value wholesale (a profile's `fallback_models` or `mcp_servers` replaces the base list; `hooks: {stop: []}` drops the base stop hooks). A value of a different kind replaces the base value, so a profile can use the `snapshots` mapping over a base `snapshots: true`. An explicit `null` in a profile is an error; it can't delete a base key.
- **Validation**: the merged result goes through every existing check, and errors name the file and the profile. Every profile is checked on every run, selected or not: profile names may use only letters, digits, `_` and `-`; each profile must be a mapping; profiles can't be nested; and an unknown key inside any profile is an error naming the file, the profile and the key. These are usage errors (exit 2) before any model request, as are `--profile` naming an undefined profile (the error lists the defined ones) and `--profile` with no config file in use.
- **Without `--profile`**, `profiles:` is validated and otherwise ignored; runs behave exactly as if it weren't there. There is no default profile.
- **With `-c`**, profiles come only from that file; `./tinyagent.yaml` isn't read, as usual for `-c`. The untrusted keys a profile sets in `./tinyagent.yaml` are ignored without `--trust-config`, like the base ones.
- **Discovery**: `tinyagent --list-profiles [-c <file>]` prints the profile names in the selected config file (or `./tinyagent.yaml` in `--cwd` without `-c`), sorted, one per line. It makes no model request and needs no credentials. No config file, no `profiles:` section, or an empty mapping prints nothing and exits 0; a missing or unreadable file, malformed YAML, or an invalid profile exits 2 with nothing on stdout. It can't be combined with `-t`, `--profile`, or another mode (`--check`, review, `--resume`, `--rewind`, `--acp`, `--transcript`, `--completion`, `--init-config`, `--update`).
- **Recording**: the profile *name* (never its settings) is recorded as `profile` in the trajectory (`-o`) and in `run.started`, and `-v` prints one `config profile:` line. Without `--profile` neither has the field. `--check --profile <name>` checks and reports the merged configuration.
- **Resume**: `--resume` accepts `--profile` for the settings it re-reads from the current setup (credentials, providers, limits, fallbacks). The saved model still wins, and the original run's profile is not re-applied automatically; the resumed trajectory records the profile given to the resuming run. `--rewind` reads no config and rejects `--profile` (exit 2); `--update` and `--version` ignore it.

Configs with `profiles` fail to load on tinyagent versions without this feature, since older versions reject the unknown key.

### Local MCP tools

MCP is disabled unless `mcp_servers` is explicitly configured. Each configured executable is trusted code with its own host and network permissions; this is tool preauthorization, not a sandbox or an interactive approval prompt. Configuration is read only from the explicitly selected `-c` file, or from `./tinyagent.yaml` with `--trust-config`, never from model output. Tinyagent launches the command directly (no shell), with `--cwd` as its working directory and a minimal environment (`PATH`, plus the Windows process variables when present) and only the named forwarded variables. Missing forwarded variables fail before launch. Do not forward secrets unless the server needs them. Read-only runs never start MCP servers.

The Go MCP SDK v1.8.0 supports protocol revisions 2026-07-28, 2025-11-25, 2025-06-18, 2025-03-26, and 2024-11-05 over stdio. Tinyagent discovers the complete paginated catalog once per run, with a 15-second default discovery timeout, 100-page, 256-selected-tool, 64-KiB-per-schema, and 1-MiB-total-schema caps, and exposes only configured names. Tool names are sent to providers as `<alias>__<original-name>` and must fit provider-compatible naming rules. The accepted input schema subset supports object, array, string, number, integer, boolean and null types (a single type or an array such as `["string", "null"]`), nested properties/items, required, enum, const, numeric bounds and `multipleOf`, length, `pattern`, `format`, item and property counts, `uniqueItems`, annotations (`title`, `description`, `default`, `examples`), and boolean `additionalProperties`. A top-level `$schema` declaration is dropped. Other keywords, including `anyOf`/`oneOf`/`allOf` and references, are rejected, and a selected tool with such a schema stops startup. Each call defaults to a 60-second timeout; a timed-out call returns an error to the model and the server stays available, while any other call failure disables that server for the rest of the run. Text content is joined with newlines, structured output is appended as JSON, unsupported content is marked as omitted, tool errors remain visible, and model-visible output is capped at 256 KiB before the normal output cap is applied. Stdio protocol data is kept separate from server stderr.

#### MCP resources

A server can also share read-only context as [MCP resources](https://modelcontextprotocol.io/specification/2025-11-25/server/resources) (documentation pages, schemas, tickets). They are off unless the server has a `resources` allowlist of URI patterns. Each pattern is an exact URI or a prefix ending in `*` (`docs://guides/*` matches every URI starting with `docs://guides/`); `*` anywhere else, a bare `*`, an empty string or a duplicate exits 2 before any model request. There is no "all resources" wildcard. A server needs a nonempty `tools` or `resources` list, so a resource-only server is allowed. A server with `resources` must declare the MCP `resources` capability, or startup exits 2 naming the server. Resource lists are not fetched at startup; the allowlist is checked on every call. URIs are compared as literal strings, without normalization or decoding, and the server must still validate them.

When at least one server has `resources`, the model gets two read-only tools:

- `list_mcp_resources` (optional `server`, optional `cursor` from a previous result, which requires `server`) lists each allowlisted resource as one line with `server`, `uri`, `name`, and `mimeType`, `size` and a description (cut to 200 characters) when present, then the resource templates whose literal prefix (up to the first `{`) can match a pattern, marked `template`. Filtered-out entries are only counted (`[N resources hidden by the allowlist on server docs]`). A listing fetches at most 100 pages per server; a longer one ends with a note giving the cursor to continue.
- `read_mcp_resource` (`server` and `uri`, both required) calls `resources/read` for an allowlisted URI. A URI outside the allowlist returns `error: resource not allowed for server <alias>: <uri>` without contacting the server. Each content item gets a `[resource: <uri>, <mime>]` header line; text follows in order, and binary blobs become `[omitted MCP resource blob: <mime>, <n> bytes]`. With `--images`, PNG, JPEG, GIF and WebP blobs within `max_image_bytes` pass through like MCP tool images.

Both tools use the server's `call_timeout_seconds` and the same result handling as MCP tools: a 256 KiB cap before the normal output cap (so with tool output files on, a larger resource is saved as the capped 256 KiB text, not in full), `redact_secrets`, tool output files, `pre_tool`/`post_tool` hooks (matched by the names `list_mcp_resources` and `read_mcp_resource`), `tool.call`/`tool.result` events and trajectories. Server errors (such as resource not found), interactive input requests and timeouts are returned to the model as tool errors and the server stays available; a transport failure disables it, as for tools. `-v` prints one line per read (server, URI, bytes, time), the stderr startup line counts resource patterns, `--check` shows each server's tool and resource pattern counts, and `run.started` lists each server's patterns as `mcp_resources` (never contents). Resource content is untrusted input that may carry prompt injection, and it goes to the model provider and into trajectories. Read-only runs, review runs and subagents don't get these tools. MCP prompts, resource subscriptions and list-change notifications, attaching resources from the command line, and following `resource_link` items in tool results are not supported. Older tinyagent versions reject a config file containing the `resources` key.

MCP tool calls and results are stored in trajectories along with the external tool names. Trajectories from MCP-enabled runs cannot be resumed. Resuming a built-in-only checkpoint works with `mcp_servers` configured; the servers are not started and the run keeps the built-in tools. Trajectories and server stderr may contain sensitive data supplied by tools or the server; [`redact_secrets`](permissions.md#redacting-secrets-from-tool-output) masks known secret values in tool results, but not other sensitive data or server stderr. On shutdown tinyagent closes MCP sessions and asks their processes to exit, then the SDK applies its bounded termination. Unix process descendants and Windows child-process cleanup have platform-specific behavior; tinyagent does not promise process-tree cleanup on Windows.

### Command tools

`command_tools` gives the model your project's own actions (run the tests, regenerate code, check a migration) as named, typed tools, without writing an MCP server. Each entry has a `name` (`^[A-Za-z][A-Za-z0-9_-]{0,63}$`, not a built-in tool, MCP `<alias>__<tool>` or other command tool name), a `description`, optional `parameters` (a JSON Schema with `type: object` at the root, in the same subset as `--output-schema`; omitted means no arguments), an `argv` template, and optional `timeout_seconds` and `dir` (see the [Config](#config) example). Like `mcp_servers` and `hooks`, definitions are read only from `-c`, or from `./tinyagent.yaml` with `--trust-config`. Every definition is checked at startup, and a bad one (name collision, empty `argv`, a placeholder in `argv[0]`, a placeholder for an undeclared or object-typed property, an unsupported schema keyword, a `dir` outside `--cwd`) exits 2 before the first model request, naming the tool and key. `--check` validates them too and prints how many are configured.

When the model calls a command tool, its arguments are checked against `parameters` first; if they fail, the model gets an error listing each problem and nothing runs. Then the `argv` template is filled in: a literal element passes through unchanged, and an element that is exactly `{{name}}` is replaced by that argument. A string becomes one element verbatim, a number or boolean its JSON form, an array of scalars one element per item, and a missing (or null) optional argument drops the element. Placeholders inside a larger string (`--pkg={{p}}`) are rejected. The command runs **without a shell**, so `;`, `$(...)`, backticks, quotes and newlines in an argument arrive as plain characters in one argv element.

Command tools run where `bash` runs and under the same controls: the [execution environment](sandbox.md#execution-environments) (`argv[0]` is looked up on the container's `PATH` there), [`--sandbox`](sandbox.md#sandboxing-commands), the [command environment](sandbox.md#command-environment) policy (`argv[0]` is looked up on the `PATH` it passes, so with `inherit: none` and no `PATH` use an absolute path), the working directory (`--cwd`, or `dir`), the timeout (which kills the process group on Unix), and `max_output_chars` and [tool output files](long-runs.md#tool-output-files). The result has the `bash` format (`<returncode>` and `<output>`, or the timeout message); a non-zero exit is a normal result, and an executable that isn't found is an error result. The run continues either way.

They are offered on every turn of a [`tinyagent chat`](chat.md) or `--acp` session as in a headless run. They are not offered with `--read-only` (a forged call is rejected like any other read-only denial), and subagents never get them. [Hooks](permissions.md#tool-hooks) match them by name, with `arguments` as the decoded JSON the model sent. `-v` prints one stderr line per call with the final argv, `--json` and `--log-db` record the usual `tool.call` and `tool.result` events, `run.started` lists their names as `command_tools`, and trajectories record each definition (`name`, `argv` template, `schema_digest`). With `--resume`, the current config's command tools apply; if they differ from the checkpoint's, stderr and the model are told, and a call to one that no longer exists gets an error result.

**Security:** command tools are trusted definitions that run with your privileges, like hooks and MCP servers. Schema checks limit what the model can pass; they don't make an unsafe `argv` safe: `[bash, -c, "{{script}}"]` gives back arbitrary execution. An argument starting with `-` is passed through as is and can be read as an option (for example `--output=/etc/passwd`), so constrain every placeholder with a `pattern`, `enum` or similar, as in the `^\./` pattern above. Older tinyagent versions reject a config file containing the `command_tools` key.

## Model roles

The `models:` section picks a model per role (`main`, `auxiliary`, `delegated`) and per task; every key is optional and nothing changes when it is unset. How roles resolve, [per-role fallback chains](models.md#per-role-fallback-chains) and [usage by role](models.md#usage-by-role) are described in [Models](models.md#model-roles); `models.delegated.routes` is described in [Task-based routing](tasks.md#task-based-routing).

| Key | Meaning |
|---|---|
| `models.auxiliary.model` | Default model for helper calls (same as `--aux-model`) |
| `models.auxiliary.tasks.compaction`, `models.auxiliary.tasks.review` | Per-task helper models |
| `models.delegated.model` | Default model for `delegate` and `worker` children (same as `--delegate-model`) |
| `models.delegated.tasks.delegate`, `models.delegated.tasks.worker` | Per-task child models; aliases `subagents.model` and `subagents.workers.model` |
| `models.<role>.fallback_models` | The role's own fallback chain, at most 3 entries |
| `models.delegated.routes` | Task-based routes, each with `model` and `match` and/or `type` |
