# Permissions, hooks, lockdown and file access

### Permissions

`permissions` is an opt-in, declarative tool permission policy: `allow` / `ask` / `deny` rules that match a tool call by its name and one primary argument (a shell command, a file path, or a URL host), applied before any tool runs. It fills the gap between full autonomy and `--read-only` without changing the default: a config without a `permissions` key behaves exactly as before.

```yaml
permissions:
  default: allow            # allow (default) | ask | deny, when no rule matches
  non_interactive: deny     # what an ask becomes with nobody to answer: deny (default) | allow
  rules:
    - tool: "bash|start_process"
      command: "go test *"
      decision: allow
    - tool: "bash|start_process"
      command: "git push *"
      decision: ask
    - tool: bash
      command: "curl *"
      decision: deny
      message: "use web_fetch for allowlisted URLs"
    - tool: "write_file|edit_file|apply_patch"
      path: "migrations/**"
      decision: deny
    - tool: web_fetch
      host: "*.internal.example.com"
      decision: ask
    - tool: "github__*"
      decision: ask
```

- `tool` is required: a `|`-separated list of tool-name globs (`*` wildcard), covering built-ins, MCP `<alias>__<tool>` names and [`command_tools`](config.md#command-tools) names.
- At most one argument matcher per rule: `command` (a `*` wildcard over the command text, for `bash`, `start_process` and a command tool's resolved argv), `path` (gitignore-style, relative to `--cwd`, in the [`file_access`](#restricting-file-access) syntax; for `apply_patch`, the rule matches when any path in the patch matches), or `host` (the [`sandbox_allowed_domains`](sandbox.md#sandboxing-commands) syntax, for `web_fetch`). A matcher that doesn't apply to the named tool (for example `path` on `bash`) is a config error.
- `decision` is required: `allow`, `ask`, or `deny`. `message` is optional and shown to the model on deny.
- Precedence is deterministic: **deny > ask > allow > default**.
- Compound shell commands are split on unquoted `&&`, `||`, `;`, `|` and newlines before matching. A `deny`/`ask` rule matches when it matches **any** segment; an `allow` rule allows only when **every** segment is matched by some allow rule; and a command containing `$(...)`, backticks or `eval` never matches an allow rule.
- `update_plan`, `ask_user` and `submit_result` are exempt: they are never prompted or denied, even under `tool: "*"`, `decision: deny`.

An `ask` is answered through the channel the run has:

- **Interactive terminal** (`tinyagent -t ...`, not `--json`, not `--acp`): tinyagent prints the tool, the matched rule and the primary argument (after secret redaction) to stderr and reads one answer from the controlling terminal (`/dev/tty`, never stdin, so `-t -` keeps working): `y` allows once, `a` allows that exact tool and argument for the rest of the run, and `n` (optionally with a reason) denies. Ctrl-C ends the run as `Interrupted`; the `--time-limit` firing mid-prompt ends it as `TimeLimitExceeded`.
- **ACP** (`--acp`): tinyagent sends `session/request_permission` with the call's `toolCallId` and the `allow_once`, `allow_always` and `reject_once` options; the editor's Approve/Reject buttons answer it. A `cancelled` outcome cancels the turn like `session/cancel`.
- **Non-interactive** (no terminal, `--json`, piped CI/GitHub Action runs): an `ask` resolves to `permissions.non_interactive` (`deny` by default), so nobody can answer but the run continues.

A denied call does not run and no `pre_tool` hook runs for it; the model gets `error: denied by permission rule <index>: <message>` (or `... by user: <reason>`), and the run continues. An allowed call then runs its [`pre_tool` hooks](#tool-hooks) as usual, which can still deny it — but a hook can never cause a policy-denied call to run. Rules come only from `-c`, a trusted `./tinyagent.yaml` (`--trust-config`) or a profile, never from the repository, with one exception: [project rules](ratchet.md) with a `deny` table in `.tinyagent/ratchet.toml` are appended as `deny` rules (they can only block calls, never allow them; `--no-ratchet` skips them). A profile's `rules` replace the base list. Validation errors (unknown keys, an empty `tool`, an invalid decision, two matchers in one rule, a matcher/tool mismatch) exit 2 before any model request, naming the file, profile and rule index.

Every decision is recorded: a `permission` event goes to `--json`, `--log-db` and `--otel` (with `tool_call_id`, `tool_name`, `decision`, `source` (`rule`, `default`, `user`, `session` or `non_interactive`), the rule index, and the channel for asks), `run.started` and trajectories record a `permissions` summary, `-v` prints one stderr line per non-default decision, and `--check` prints the rule count, default and resolved non-interactive behavior.

**This is a guardrail, not a security boundary.** Command-text matching cannot stop a determined model from running something equivalent (for example `Bash(rm *)` doesn't stop `/bin/rm` or `bash -c 'rm ...'`). For real containment keep using [`--sandbox`](sandbox.md#sandboxing-commands), [`sandbox_allowed_domains`](sandbox.md#sandboxing-commands) and `--env container`. Older tinyagent versions reject a config containing `permissions`.

In [`tinyagent chat`](chat.md), `ask` rules prompt on the terminal before the tool runs, as in a normal terminal run.

### Tool hooks

Hooks are disabled unless `hooks` is configured. A `pre_tool` hook runs before a matching tool call and can deny it with a reason the model sees; a `post_tool` hook runs after it and can append feedback, such as formatter or linter output, to the result the model sees. Like MCP servers, hooks are trusted local executables running with your privileges: they are read only from `-c` or a trusted `./tinyagent.yaml` (`--trust-config`), never from the repository, launched directly (no shell) with an absolute `command`, `--cwd` as the working directory, and the same minimal environment (`PATH`, the Windows process variables when present, and only the named `env` variables, which must be set at startup). Provider API keys are not passed unless named. Hooks are a guardrail and automation point, not a sandbox: they see a call's parsed arguments, not what a shell command actually does.

Each hook gets one JSON object on stdin: `schema_version` (`1`), `event` (`pre_tool` or `post_tool`), `tool_name`, `tool_call_id`, `arguments` (decoded JSON, or the raw string if malformed), canonical `root`, `read_only`, and `step`; `post_tool` also gets `output` (the model-visible result after normal truncation) and `is_error`, plus `output_file` when [tool output files](long-runs.md#tool-output-files) saved the full result. The input can contain secrets from tool arguments and output.

| Event | Exit 0 | Exit 2 | Other exit, failure to start, timeout, or invalid stdout JSON |
|---|---|---|---|
| `pre_tool` | Allow, unless stdout is `{"decision":"deny","reason":"..."}` | Deny; the reason is stderr | Apply `on_error`: deny by default (the reason names the hook and the failure), or allow with a warning |
| `post_tool` | Optional stdout `{"feedback":"..."}` is appended to the result | stderr is appended to the result | Warning on stderr; the result is unchanged |

A denied call is not executed; the model gets the error result `error: denied by pre_tool hook <command>: <reason>` and the run continues. Matching hooks run one at a time in config order: the first `pre_tool` deny stops the rest, and every matching `post_tool` hook runs, each appending its feedback under `[post_tool hook <command>]` (capped at 8 KiB per hook; the combined result still honors `max_output_chars`). Read-only denials and unknown tools are rejected before hooks, so hooks never run for them and cannot widen authority; with `--read-only`, hooks still run for `read_file`, `list_files`, and `grep`. MCP tools are matched by their exposed `<alias>__<tool>` name, and [command tools](config.md#command-tools) by their configured name. Ctrl-C or the wall-time limit kills a running hook (its process group on Unix; only the process on Windows). `-v` prints one stderr line per hook run, `--json` emits a `hook` event, and trajectories record the model-visible results, including denial reasons and feedback. Resumed runs use the hooks in the current config. Hooks do not run for `update_plan`, which only changes the run's plan.

Hooks match tool names, so a hook on `write_file|edit_file` does not see edits made with the opt-in `apply_patch` tool; use `match: "write_file|edit_file|apply_patch"` to cover all three. An `apply_patch` hook gets `arguments` as `{"input": "<patch text>"}`.

Two examples: a `post_tool` hook on `write_file|edit_file` that runs `gofmt -l` on the edited path and prints `{"feedback":"..."}` when it lists files, and a `pre_tool` hook on `write_file|edit_file` that exits 2 with `edit schema.sql and run make gen instead` when `arguments.path` is under `migrations/`.

#### Stop hooks

A `stop` hook runs when the run is about to end successfully (`Submitted` or `Completed`, including a valid `--output-schema` result) and can send the model back to work instead, for example when `go test ./...` still fails. Stop hooks never run for limits, interrupts, errors, `RepeatedFormatError`, or `InvalidOutput`, nor for an `--output-schema` result that failed validation. They use the same launch rules, environment, and timeouts as tool hooks; `match` and `on_error` are rejected for them.

Each stop hook gets one JSON object on stdin: `schema_version` (`1`), `event` (`stop`), `status` (`Submitted` or `Completed`), `submission`, `structured_output` (only with `--output-schema`), canonical `root`, `read_only`, `step`, `continuations` (how many times stop hooks already sent the model back), and `stop_hook_active` (`true` when `continuations` is above zero, so a hook can avoid looping).

| Exit 0 | Exit 2 | Other exit, failure to start, timeout, invalid stdout JSON, or unknown decision |
|---|---|---|
| Accept, unless stdout is `{"decision":"continue","reason":"..."}` (`block` and `deny` are accepted as aliases) | Continue; the reason is stderr | Warning on stderr and a `warning` event; treated as accept |

All stop hooks run in config order. If any asks to continue, the model gets a user message that starts with a fixed notice followed by each hook's reason under `[stop hook <command>]` (capped at 8 KiB per hook), and the run goes on as normal steps, so step, cost, and time limits still apply and win. If the submitting message also contained tool calls, those calls are not run; each gets an error result saying so. After `max_stop_continuations` continuations (default 5, range 1..50), a further continue ends the run as `VerificationFailed` with the hooks' combined reasons in the summary line and in `stop_reason` (`--json` result and `-o` trajectory); `stop_continuations` records the count. If Ctrl-C or the wall-time limit fires while a stop hook runs, the hook is killed and the run ends as `Interrupted` or `TimeLimitExceeded`, never `Submitted`. A run that a stop hook sent back to work cannot be resumed with `--resume` yet, and `VerificationFailed` runs cannot be resumed either. With no stop hooks configured, runs behave exactly as before.

Example: a stop hook script `/usr/local/bin/verify-tests` that runs `go test ./...` in the working directory, and, when it fails, prints the failing output to stderr and exits 2. The model then sees `[stop hook /usr/local/bin/verify-tests]` followed by the test failures and keeps working until the tests pass or the cap is reached.

### Untrusted-content lockdown

Lockdown (experimental, in progress; see [#171](https://github.com/vicgupta/tinyagent/issues/171)) limits what a prompt-injected run can send out. It is off by default; turn it on with `--lockdown` or `untrusted_content.enabled: true` (`--lockdown=false` overrides the config). Like `mcp_servers` and `memory`, `untrusted_content` is read only from `-c`, a trusted `./tinyagent.yaml` (`--trust-config`) or a profile, never from the repository.

```yaml
untrusted_content:
  enabled: true
  sources: [web_fetch, mcp, review]   # optional; leave it out to use every source
  lockdown:
    web_fetch_allowed_domains: [pkg.go.dev]   # web_fetch hosts still readable after taint
    allowed_domains: [proxy.golang.org]       # hosts sandboxed commands still reach after taint
    # container_proxy: true                   # container environment: reach allowed_domains through a lockdown proxy after taint
mcp_servers:
  - alias: docs
    # ... command, tools ...
    untrusted: false          # this server's results don't taint the run
    lockdown_allowed: true    # its tools and resource reads stay callable after taint
```

- **Taint.** A run becomes tainted when content from an untrusted source enters the model's context: a `web_fetch` result, an MCP tool result or resource read, or a review diff (`--review`, `--review-base`, `--review-commit`). `--untrusted-task` marks the task text itself as untrusted, so the run starts tainted; it needs lockdown on and doesn't turn it on (exit 2 otherwise). Taint is decided by source, never by content. `sources` chooses which of `web_fetch`, `mcp`, `review` and `acp_client_mcp` taint; an empty list, an unknown name or a duplicate exits 2. `--untrusted-task` and a resumed tainted checkpoint always taint.
- **After taint.** For the rest of the run, `bash`, `start_process` and [command tools](config.md#command-tools) are refused unless commands are sandboxed (see the next item), `web_fetch` may only read hosts in `web_fetch_allowed_domains` (same syntax as [`sandbox_allowed_domains`](sandbox.md#network-allowlist); each host must also be in `web_fetch.allowed_domains`, and a bare `*` exits 2), and MCP tools and resource reads are refused unless their server has `lockdown_allowed: true`. A refused call is a tool error that names the first taint source, and the tool result that first taints the run carries a one-line notice. File tools, edits, plans and the submission keep working. [Workers](long-runs.md#workers) share the parent's state, so their commands are refused too; [subagents](long-runs.md#subagents) are read-only and stay available.
- **Sandboxed commands.** Under [`--sandbox`](sandbox.md#sandboxing-commands) without `sandbox_network`, commands keep running after taint. With network denied (no `sandbox_allowed_domains`) nothing changes for them. Behind the [network allowlist](sandbox.md#network-allowlist) proxy, the first taint narrows the proxy to hosts in both `sandbox_allowed_domains` and `untrusted_content.lockdown.allowed_domains` (same syntax; empty, the default, refuses all their network, and a bare `*` exits 2) and closes the connections opened before; a refused connection names `untrusted_content.lockdown.allowed_domains`. Background processes go through the same proxy, so they are left running. With `sandbox_network`, commands keep direct network until the first taint, which takes it away: later commands run with network denied, or, when `untrusted_content.lockdown.allowed_domains` is set, reach only those hosts through a proxy tinyagent starts for this (refusals name that key). Background processes started before the taint keep their direct network, so they are stopped. If the host can't deny network (or, with lockdown domains, create the network namespace), commands are refused after taint instead, and lockdown says so at startup. In the [container environment](sandbox.md#execution-environments), commands keep running after taint: the first taint disconnects the container from every runtime network (`<runtime> network disconnect`) and checks none is left, so later commands and the processes already running there have no network, and background processes are left running. A container started with `--network none` in `run_args` has nothing to remove. `untrusted_content.lockdown.allowed_domains` has no effect there unless you also set `untrusted_content.lockdown.container_proxy: true` (off by default; it needs `allowed_domains`, or the config exits 2). Then tinyagent creates an `--internal` runtime network (`tinyagent-lockdown-…`, labeled `tinyagent=1`) at startup and runs an allowlist proxy on that network's gateway address on the host; at the first taint the disconnected container joins that network, and later commands get `HTTP_PROXY`, `HTTPS_PROXY` and `ALL_PROXY` pointing at the proxy, which only reaches the `allowed_domains` hosts (refusals are `sandbox.network_denied` events naming `untrusted_content.lockdown.allowed_domains`). The internal network has no route out, but services on the host that listen on all interfaces are reachable from it, which is why this is opt-in. If the network or proxy can't be set up (for example rootless podman, whose network gateway isn't on the host), or joining it fails at taint, commands keep running with no network, and lockdown says so. The network is removed when the run ends. Network modes that can't be disconnected (`host`, another container's, rootless `slirp4netns` or `pasta`) keep commands refused after taint, and lockdown says so at startup; if disconnecting fails at taint, commands are refused for the rest of the run. `--check` predicts this without starting a container: it reads `--network` (or `--net`) from `container.run_args`, asks `podman info` whether podman runs rootless when they set none (docker's default is a bridge network), and adds the mode to its `lockdown:` line, for example `; container network bridge (runtime default)`, showing `commands refused` for a mode that can't be disconnected. Unsandboxed local runs still refuse commands after taint.
- **MCP servers.** `untrusted: false` stops a server's results from tainting the run, and `lockdown_allowed: true` keeps its tools callable after taint. Both default to the strict behavior, and a call that can't be tied to one configured server counts as untrusted and is refused.
- **Resume.** Taint carries over `--resume` and follow-up runs with its sources and denials. A tainted checkpoint resumed with lockdown off exits 2; start a fresh run instead.
- **Observability.** On start, one line such as `lockdown: on (sources: web_fetch, mcp, review, untrusted task; after taint: commands refused, web_fetch → 1 domains, mcp tools refused)` goes to stderr (`commands sandboxed, network denied` or `commands sandboxed, network → 1 domain (proxy.golang.org)` when commands are confined, with `(direct network until taint)` after `commands sandboxed` under `sandbox_network`, where `run.started` also gets `network_before_taint: "direct"`; in the container environment `commands in container (network removed at taint), network denied`, and `run.started` gets `environment: "container"` in its command policy), and `--check` prints the same settings on a `lockdown:` line. `--json` and `--log-db` add `lockdown` to `run.started`, emit [`taint` and `lockdown.denied`](json-output.md#machine-readable-output) events and add `tainted` and `lockdown_denials` to `result`. The summary line notes the taint, for example `, tainted by web_fetch at step 3 (1 call refused)`. `-o` trajectories and checkpoints record `lockdown_enabled`, `tainted`, `taint_sources` and `lockdown_denials`, and [OpenTelemetry](json-output.md#opentelemetry-export) spans carry `tinyagent.lockdown.enabled` and `tinyagent.lockdown.tainted`, and the root span adds the after-taint command policy as `tinyagent.lockdown.commands` (`refused` or `sandboxed`), `tinyagent.lockdown.command_network` (`denied` or `proxy`) and `tinyagent.lockdown.command_allowed_domains` (the number of `untrusted_content.lockdown.allowed_domains` patterns, not the hosts). Records hold source and tool names, ids, steps and hosts, never fetched content, URLs or MCP results.
- **ACP sessions.** Lockdown works with `--acp`. Each session has one lockdown state, so once a prompt taints the session, later prompts in it stay tainted. A result from an MCP server the client supplied (`acp.client_mcp_servers`) taints as `acp_client_mcp`; those servers have no `mcp_servers` entry, so their tools are refused after taint. With `--untrusted-task` every session starts tainted.

## Redacting secrets from tool output

`redact_secrets` (config only, from `-c`, a trusted `./tinyagent.yaml` (`--trust-config`) or a profile; never from the repository) replaces the exact values of secrets tinyagent knows about with `[REDACTED]` in tool output, **before** the output enters the model's context. It is off by default; `--redact-secrets` / `--redact-secrets=false` overrides `enabled` for one run.

```yaml
redact_secrets:
  enabled: true
  env: [DATABASE_URL, "DEPLOY_*"]   # extra variable names or globs whose values are secrets
  allow: [PUBLIC_KEY]               # secret-named variables not to mask
  patterns: false                   # true also masks token shapes (sk-..., gh?_..., Bearer ..., xox?-...)
```

- **Known values**, collected once at startup: tinyagent's environment variables named `*_KEY`, `*_TOKEN`, `*_SECRET` or `*_PASSWORD` (the `exclude_secrets` rule), `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `OPENROUTER_API_KEY`; MCP server and hook `env` values; named providers' `api_key_env` and `env_headers` values; `command_env.set` values; the variables matched by `env`; and the `codex` provider's tokens once loaded (including ones refreshed during the run). Variables matched by `allow` are left out. Values shorter than 6 characters are skipped, as GitHub Actions advises, since they would make output unreadable. Each value is also matched in its JSON-escaped form, longest first.
- **Covered:** `bash`, `start_process`, `read_process`, `stop_process`, the `background_processes` tails saved at run end, MCP tool results, `web_fetch`, `post_tool` hook feedback and `stop` hook reasons. Masking happens where the result is created, so the provider request, `-o` trajectory, `--json` events, `--log-db`, [tool output files](long-runs.md#tool-output-files), `post_tool` hook input and `--transcript` all get the masked text. It runs before truncation and before a result is saved to a file, so a truncated result never shows half a secret. Background process output that ends in the start of a secret is held back until the rest arrives or the process exits, so a secret split across reads is still masked whole.
- **Not covered:** the built-in file tools (`read_file`, `grep`, `list_files`, `lsp`, `view_image`, `skill`) and edit results, because their output feeds `edit_file`, `write_file` and `apply_patch`, and a placeholder written back would corrupt files. Use [`file_access.deny_read`](#restricting-file-access) to keep secret files away from them. Also not covered: the task, `AGENTS.md`, skills, `--image` files, the live terminal echo of `bash` `stream: true`, and subagents (which only have file tools).
- **Exact matches only.** Like GitHub Actions log masking, this finds a secret only as the exact string (or its JSON-escaped form). Transformed values (Base64, URL-encoded, split or reassembled), credentials stored only in files, and secrets tinyagent doesn't know about are not caught. `patterns: true` adds the token shapes `--log-db` uses, at the risk of masking test fixtures and docs. This is defense in depth, not a guarantee: a prompt-injected model can still try to exfiltrate secrets through commands, so combine it with [`command_env`](sandbox.md#command-environment) and [`--sandbox`](sandbox.md#sandboxing-commands).
- **Observability**, by variable name and count only, never values: one stderr line such as `redact_secrets: 14 values from 12 variables`; `-v` lists the masked names, the names skipped as too short and `env` entries that matched nothing. `tool.result` events have `redactions` when a result was masked, and `run.started` and `-o` trajectories record `redact_secrets` (`enabled`, `patterns`, `names`), so variable names do appear in those files.
- **Validation:** unknown keys, an empty name or a malformed glob in `env` or `allow`, the same entry in both, and a non-boolean `enabled` or `patterns` exit 2 before any provider request. An `env` name that is unset is not an error.
- **Resume:** the current invocation's setting applies to new results only. Earlier, unredacted results in the checkpoint are not rewritten, and with the setting off a resumed run behaves as before.

## Restricting file access

`file_access` (config only, from `-c` or a trusted `./tinyagent.yaml` (`--trust-config`); no other file inside `--cwd` is read) denies the built-in file tools access to matching paths. It is off by default.

```yaml
file_access:
  deny_read: [".env", "*.pem", "secrets/", "**/id_rsa"]   # also write-denied
  deny_write: [go.sum, "/vendor/"]                        # readable, not writable
```

- **Patterns** are gitignore-style and relative to `--cwd`: `*`, `?` and `[...]` match within one path segment, `**` matches any number of segments, a leading `/` (or any `/` before the end) anchors the pattern to `--cwd`, a trailing `/` matches directories only, and a pattern without a slash matches a name at any depth. A rule on a directory covers everything below it. Empty patterns, `.`/`..` segments, bad glob syntax and `!` negation (reserved) are config errors (exit 2). Absolute and `~` paths are not supported. Matching ignores case on macOS and Windows.
- **Reads:** `read_file`, `view_image`, and a `list_files` or `grep` aimed at a denied path return an error naming the rule, such as `.env is denied by file_access.deny_read rule ".env"`. Walks (`list_files`, `grep`) skip denied files and directories without opening them and end with a neutral count such as `(2 paths hidden by file_access rules)`, which names none of them. The count includes denied files only when they would have matched the pattern or `include`; a denied directory counts once.
- **Writes:** `write_file`, `edit_file` and `apply_patch` refuse paths matching `deny_write` or `deny_read`. `apply_patch` checks every operation (including `Move to` destinations) before writing, so a patch that touches one denied path changes nothing.
- **Symlinks:** a path is checked as named and through its resolved target (for a new file, through its deepest existing directory), so `innocent.txt -> .env` or a symlinked directory pointing into `secrets/` is denied too. Walks don't follow symlinked directories.
- With `-v`, one stderr line gives the rule counts. `-o` trajectories and `--json` `run.started` record `file_access` (`deny_read`, `deny_write`). With `--resume`, the current invocation's rules apply. A `deny_read` rule that hides the [tool output](long-runs.md#tool-output-files) directory fails at startup, since the model is told to read it. AGENTS.md and skills loading are not affected.
- **Not covered:** `bash`, `start_process`, hooks, MCP servers and execution environments can still read and write anything you can; combine with `--read-only`, [`--sandbox`](sandbox.md#sandboxing-commands) or a [command environment](sandbox.md#command-environment) policy as needed. Checks happen before each operation, so a file swapped for a symlink in between (a race) is not caught. This narrows what the model's file tools can touch; it is not a security boundary.

## Additional directories

By default the file tools reach only `--cwd`. `--add-dir-read <path>` and `--add-dir-write <path>` (both repeatable), or `additional_dirs` in the config, add up to 10 existing directories outside it, for example a sibling repo to read or a shared output folder to write:

```yaml
additional_dirs:
  - {path: /home/me/src/shared-lib, mode: read}    # read_file, list_files, grep, view_image
  - {path: /home/me/out, mode: write}              # also write_file, edit_file, apply_patch
```

- **Modes:** every entry needs an explicit `mode`, `read` or `write`. Read directories refuse writes (including through symlinks that point into them) with an error naming the directory and its mode; an `apply_patch` that touches one changes nothing. `--read-only` makes every additional directory read-only.
- **Paths:** config paths must be absolute; flag paths may be relative to `--cwd`. Each must exist and be a directory, and may not be the filesystem root, `--cwd`, inside or containing `--cwd`, or overlap another entry; any of these exits 2 before the first model request. Any `--add-dir-*` flag replaces the config list. The model reaches additional directories with absolute paths; relative paths still resolve against `--cwd`.
- **Visibility:** the system prompt lists the directories and modes, `-v` prints one `additional dir:` line each, and `-o` trajectories and `--json` `run.started` record `additional_dirs`. With `--resume`, the current invocation's directories apply; if they differ from the checkpoint's, stderr says so and the model is told.
- **Other features:** [`file_access`](#restricting-file-access) patterns apply inside each directory relative to it. [Subagents](long-runs.md#subagents) can read them. With [`--sandbox`](sandbox.md#sandboxing-commands), write directories are added to the writable roots; with the [`container`](sandbox.md#execution-environments) environment, each is mounted at the same path (read ones `:ro`). [Snapshots](undo.md#rewind-workspace-changes) cover only `--cwd`, so stderr warns that `--rewind` won't restore changes made in additional directories.
