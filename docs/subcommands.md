# CLI reference

## Subcommands

Six subcommands are shortcuts for existing flags. They rewrite the command line into those flags before parsing, so config, environment variables and precedence work the same either way, and every `--flag` form keeps working.

| Subcommand | Same as |
|---|---|
| `tinyagent sandbox -t "..."` | `--sandbox` |
| `tinyagent sandbox network [--allow-domain host]... -t "..."` | `--sandbox --sandbox-network [--sandbox-allow-domain host]` (`--allowed-domains` is an alias) |
| `tinyagent memory -t "..."` | `--memory` |
| `tinyagent memory list` / `path` / `config` | `--memory-list` / `--memory-path` / `--memory-help` |
| `tinyagent memory approve <id\|all>` | `--memory-approve <id\|all>` |
| `tinyagent memory forget <id\|all> [--yes]` | `--memory-forget <id\|all> [--memory-yes]` |
| `tinyagent ratchet add "<rule>" --incident "..." [--expires YYYY-MM-DD] [--review "..."] [--deny-tool t] [--deny-command c] [--deny-path p] [--deny-host h]` | `--ratchet-add "<rule>" --ratchet-incident "..." [--ratchet-expires ...] [--ratchet-review ...] [--ratchet-deny-tool ...] [--ratchet-deny-command ...] [--ratchet-deny-path ...] [--ratchet-deny-host ...]` |
| `tinyagent ratchet list` / `path` / `remove <id>` | `--ratchet-list` / `--ratchet-path` / `--ratchet-remove <id>` |
| `tinyagent planner [--first] [--file plan.md] -t "..."` | `--plan [--plan-first] [--plan-file plan.md]` |
| `tinyagent plugins list` | `--list-plugins` |
| `tinyagent plugins rollback <module> <sha256>` | `--plugins-rollback <module>@<sha256>` |
| `tinyagent plugins prune [--keep N]` | `--plugins-prune [--plugins-keep N]` |
| `tinyagent tinygo install` / `update` / `status` | `--tinygo-install` / `--tinygo-update` / `--tinygo-status` |

- Any other flag (`-t`, `-m`, `--cwd`, ...) can follow. `--` followed by words is the task: `tinyagent planner --first -- fix the bug`.
- The first word counts as a subcommand only when it is exactly the name and is alone or followed by an option, `--`, a known action or `help`. So `tinyagent memory leaks in parser` is not a subcommand; it fails as before with "pass the task with -t". To start a task with one of these words, use `-t "memory ..."`.
- An unknown option exits 2 with `unknown <sub> option --x (see tinyagent <sub> --help)`.
- Help: `tinyagent <sub> --help`, `-h`, `tinyagent <sub> help` or `tinyagent help <sub>`. A bare `tinyagent <sub>` with nothing after it prints the same help. For `ratchet`, `plugins` and `tinygo` that's always so; for `sandbox`, `memory` and `planner` it's so at an interactive terminal, and with stdin piped or redirected they still run with their flags (so without `-t` they exit 2 with `--task is required`, as before). Plain `tinyagent --help` (or `tinyagent` with no arguments at a terminal; with stdin piped and no arguments it still exits 2 with `--task is required`) shows the usage line and every subcommand (`sandbox`, `memory`, `ratchet`, `planner`, `plugins`, `tinygo`) with a one-line description; `tinyagent --help --verbose` lists every flag, grouped into sections (task, model and provider, run control, verify before stop, attempts, review, output and logging, sandbox and permissions, context and tools, project rules (ratchet), memory and planner, plugins and TinyGo, rewind, workflow and eval, setup and info).
- `ratchet` records [project rules](ratchet.md) learned from agent failures.
- `plugins rollback` and `plugins prune` manage the plugin module version store; see [Plugins](plugins.md#webassembly-plugin-modules).
- `tinygo` manages the TinyGo compiler used to build Go plugin modules. **TinyGo is an external dependency, not part of tinyagent:** it is never embedded in the tinyagent binary or repository. tinyagent downloads a pinned TinyGo release on first use (or with `tinygo install`), verifies its SHA-256 checksum and caches it locally; `tinygo update` moves to the version pinned in the current build.

## Flags

| Flag | Default | |
|---|---|---|
| `-h, --help` | | Print usage to stdout and exit |
| `-t, --task` | — | Task prompt (required unless `--resume`; with `--resume` of a finished run, the follow-up task; of a `NeedsInput` run, the answer); `-` reads it from stdin |
| `--resume` | — | Continue a validated unfinished trajectory; requires explicit matching `--cwd` and a new `-o` path |
| `--ask-user` | config `ask_user.enabled`, else off | Give the model the `ask_user` tool: it may end the run with one question (status `NeedsInput`, exit 4), answered with `--resume <trajectory> -t "<answer>"`. Requires `-o`; can't be combined with `--output-schema`; `--ask-user=false` disables it. See [Asking for input](runs.md#asking-for-input) |
| `--acp` | off | Serve the [Agent Client Protocol](integrations.md#editor-integration-acp) on stdin/stdout so editors such as Zed and JetBrains IDEs can run tinyagent as an external agent (experimental). Can't be combined with `-t`, `--resume`, `--rewind`, `--transcript`, review flags, `--worktree`, `--output-schema`, `--json`, `--ask-user`, `--check`, `--init-config`, `--completion`, `-o`, `--cwd`, `--image` or `--snapshots` (exit 2) |
| `-m, --model` | `$AGENT_MODEL`, else config `model`, else `openai/gpt-4o` | `<provider>/<model>`; no `/` means openai. Providers: openai, openrouter, anthropic, ollama, codex, plus any defined under [`providers`](providers.md#named-providers) |
| `--reasoning-effort` | config `reasoning_effort`, else unset | Reasoning effort sent to the provider: `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max`; see [Reasoning](providers.md#reasoning) |
| `--fallback-model <spec>` | config `fallback_models`, else none | Model to switch to when the current one keeps failing with `429`, `5xx` or network errors after retries; repeatable, tried in order, at most 3, and replaces `fallback_models`; see [Model fallback](providers.md#model-fallback) |
| `--no-fallback` | off | Don't fall back to other models (clears `fallback_models`); conflicts with `--fallback-model` |
| `--openai-api` | config `openai_api`, else `auto` | Transport for the `openai` provider: `auto`, `responses`, or `chat`; see [OpenAI transport](providers.md#openai-transport) |
| `--init-config` | | Write a commented starter config (including `token_limit` and `verify_on_stop`) to the user config path (or `-c PATH`), refuse overwrites, and exit |
| `-c, --config` | user config, else built-in | YAML config file; see [Config](config.md#config) |
| `--profile` | none | Apply one named profile from the config file's `profiles:` section over its base settings; see [Profiles](config.md#profiles) |
| `--list-profiles` | | Print the profile names in the selected config file (`-c`) or the user config, sorted, one per line, then exit. Makes no model request and needs no credentials; see [Profiles](config.md#profiles) |
| `--list-plugins` | | Print each configured plugin with its state, skills and `mcp.json` servers, then exit 0 (all load), 1 (a plugin is rejected or a configured server is invalid) or 2 (usage or config error). Makes no model request, needs no credentials and starts nothing; see [Plugins](plugins.md#plugins) |
| `--plugins-rollback <module>@<sha256>` | | Point a stored plugin module at an older stored version, then exit; refused when that version needs wider permissions than the first load. See [Plugins](plugins.md#webassembly-plugin-modules) |
| `--plugins-prune` | | Remove stored plugin module versions under `--cwd`'s `.tinyagent/modules`, keeping each module's current version and the `--plugins-keep` newest others, then exit |
| `--plugins-keep <n>` | 5 | Versions besides current that `--plugins-prune` keeps per module |
| `--tinygo-install` | | Download the pinned TinyGo release (an external project, not part of tinyagent) used to build Go plugin modules, verify its checksum and cache it, then exit |
| `--tinygo-update` | | Install the TinyGo version pinned in this build if it isn't cached and remove other cached versions, then exit |
| `--tinygo-status` | | Print the pinned and installed TinyGo versions, path and checksum state, then exit |
| `--cwd` | `.` | Root directory; tools can't reach outside it |
| `--memory` | off | Load this project's active [project memory](memory.md#project-memory) entries into the system prompt (overrides `memory.enabled`; `--memory=false` turns it off). `--resume` keeps the checkpoint's setting; a conflicting `--memory` exits 2 |
| `--handoff` | off | [Session handoff](long-runs.md#session-handoff): load the handoff file (`handoff.path`, default `.tinyagent/handoff.md`) into the first message as data, run `handoff.startup_command` if set, and rewrite the file with the task, status and final answer when the run ends (overrides `handoff.enabled`; `--handoff=false` turns it off). Fresh runs only; a read-only run loads the file but doesn't run the startup command or rewrite the file |
| `--memory-list` | | Print the [project memory](memory.md#project-memory) entries for `--cwd` (pending first, then newest first), or a JSON array with `--json`, then exit. Makes no model request and needs no credentials |
| `--memory-approve <id\|all>` | | Approve one pending project memory entry, or every pending entry, then exit |
| `--memory-forget <id\|all>` | | Delete one project memory entry; `all` deletes every entry and requires `--memory-yes` |
| `--memory-path` | | Print the project memory store path for `--cwd`, then exit |
| `--ratchet-add <rule>` | | Record a [project rule](ratchet.md) in `<cwd>/.tinyagent/ratchet.toml`, then exit. Requires `--ratchet-incident`; without `--ratchet-expires` or `--ratchet-review` it expires in 90 days. Makes no model request and needs no credentials |
| `--ratchet-incident <text>` | — | With `--ratchet-add`: the failure that led to the rule (required) |
| `--ratchet-expires <YYYY-MM-DD>` | 90 days from today unless `--ratchet-review` | With `--ratchet-add`: from this date the rule is skipped and flagged as expired |
| `--ratchet-review <text>` | — | With `--ratchet-add`: when to reconsider the rule; with it and no `--ratchet-expires` the rule has no expiry date |
| `--ratchet-deny-tool <globs>` | by matcher | With `--ratchet-add`: enforce the rule as a permission deny rule for these tools (`\|`-separated globs); alone, denies those tools outright |
| `--ratchet-deny-command <glob>` | — | With `--ratchet-add`: enforce the rule by denying shell commands matching this glob |
| `--ratchet-deny-path <pattern>` | — | With `--ratchet-add`: enforce the rule by denying file edits to matching paths (`file_access` syntax) |
| `--ratchet-deny-host <host>` | — | With `--ratchet-add`: enforce the rule by denying `web_fetch` to this host |
| `--ratchet-list` | | Print the project rules with status (active or expired), incident and expiry or review condition, or a JSON array with `--json`, then exit |
| `--ratchet-path` | | Print the project rules file path for `--cwd`, then exit |
| `--ratchet-remove <id>` | | Delete the project rule with this id, then exit (exit 1 if there is none) |
| `--no-ratchet` | off | Don't load `.tinyagent/ratchet.toml` for this run: no rules in the prompt and no enforced deny rules |
| `--review` | off | Review the uncommitted changes (staged, unstaged and untracked) against `HEAD`: tinyagent collects the diff and runs a read-only review; `-t` adds focus instructions. See [Reviewing changes](runs.md#reviewing-changes) |
| `--review-base <ref>` | — | Review what the current branch adds on top of `<ref>` (`merge-base..HEAD`), read-only. See [Reviewing changes](runs.md#reviewing-changes) |
| `--review-commit <rev>` | — | Review the changes introduced by one commit, read-only. See [Reviewing changes](runs.md#reviewing-changes) |
| `--worktree[=name]` | off | Run in a new Git worktree at `<repo>/.tinyagent/worktrees/<name>` on branch `tinyagent/<name>`, created from `HEAD` of the repository containing `--cwd` and kept afterwards; the name is generated when omitted. Can't be combined with `--resume` or `--rewind`. See [Worktrees](runs.md#worktrees) |
| `--attempts` | 1 | Run the task up to N times (1 to 4), each in its own worktree, and keep the first attempt whose checks pass. Needs `-t` and at least one `--attempt-check` (or `attempts.checks` in the config). See [Best-of-N attempts](runs.md#best-of-n-attempts) |
| `--attempt-model` | `-m` | With `--attempts`: the model spec for the next attempt. Give it once per attempt or not at all; can't be combined with `-m` |
| `--attempt-check` | | With `--attempts`: a shell command that must exit 0 in an attempt's worktree for it to pass (repeatable; replaces `attempts.checks` from the config) |
| `--attempt-check-timeout` | 600 | With `--attempts`: seconds before one `--attempt-check` command is killed (1 to 3600) |
| `--step-limit` | 0 (unlimited) | Max model calls |
| `--cost-limit` | 0 (unlimited) | Max USD; one warning at 80%. See [Token and cost budgets](runs.md#token-and-cost-budgets) |
| `--token-limit` | config `token_limit`, else 0 (unlimited) | Max provider-reported input+output tokens for the run, delegates and workers included; at 100% the run stops with `TokenLimitExceeded` (resumable), with one warning at 80%. With `--attempts`, shared by all attempts. See [Token and cost budgets](runs.md#token-and-cost-budgets) |
| `--time-limit` | 0 (unlimited) | Max wall-clock seconds; overrides `wall_time_seconds` |
| `--verify-on-stop` | config `verify_on_stop.enabled`, else off | Don't accept a final answer while files changed since the last successful test/build command: send the model back to verify, up to `--max-verify-reprompts` times, then mark the run unverified. See [Verify before stop](verify.md#verify-before-stop) |
| `--verify-command <cmd>` | config `verify_on_stop.commands`, else common test/build commands | A `bash` command that counts as verification for `--verify-on-stop`; repeatable, replaces the config list |
| `--verify-check <cmd>` | config `verify_on_stop.checks`, else none | A computational check for `--verify-on-stop`: a fast, deterministic `bash` command (lint, type check, tests) that passes on exit 0. When the model finishes with unverified changes, tinyagent runs the checks itself in the given order (cheapest first), stops at the first failure and sends the model back with its output; all passing lets the run finish. Also counts as a verification command. Repeatable, replaces the config list. See [Computational checks](verify.md#computational-checks) |
| `--verify-review` | config `verify_on_stop.review`, else off | The inferential tier for `--verify-on-stop`: after every `--verify-check` passes at stop, ask the model once, without tools, to review the file changes the run made. Findings are report only (`verify.review` event, stderr, trajectory `verify_review`); they never re-prompt the model or change the run's status. Needs at least one check. See [Inferential review](verify.md#inferential-review) |
| `--max-verify-reprompts <n>` | config `verify_on_stop.max_reprompts`, else 2 | With `--verify-on-stop`, how many times to send the model back before stopping unverified (1 to 10) |
| `--max-format-errors` | config `max_format_errors`, else 3 | Stop with `RepeatedFormatError` after this many consecutive steps whose tool calls are all malformed; must be >= 1. See [Malformed tool calls](troubleshooting.md#malformed-tool-calls) |
| `--max-output-tokens` | config `max_output_tokens`, else 0 | Per-request output-token budget sent to the provider; 0 keeps the provider default (8192 `max_tokens` on Anthropic). Not supported by `codex`. Unrelated to `max_output_chars`, which caps tool output. See [Output truncation](troubleshooting.md#output-truncation) |
| `--no-compact` | off | Never compact the history; see [Long runs](long-runs.md#long-runs) |
| `--plan` | config `plan.enabled`, else off | Give the model the `update_plan` tool; see [Plan tool](planner.md#plan-tool) |
| `--apply-patch` | config `apply_patch`, else off | Give the model the `apply_patch` tool for multi-file, multi-hunk edits, checked before any write; `--apply-patch=false` disables it. See [apply_patch](long-runs.md#apply_patch) |
| `--subagents` | config `subagents.enabled`, else off | Give the model the `delegate` tool, which hands a task to a read-only subagent with a fresh context and returns only its report; `--subagents=false` disables it. See [Subagents](long-runs.md#subagents) |
| `--web-fetch` | config `web_fetch.enabled`, else off | Give the model the `web_fetch` tool, which reads pages from allowlisted hosts as text; needs a nonempty allowlist. `--web-fetch=false` disables it. See [Web fetch](long-runs.md#web-fetch) |
| `--lsp` | config `lsp.enabled`, else off | Start the language servers configured in `lsp.servers` (user config or `-c` only): append compiler diagnostics to `write_file`, `edit_file` and `apply_patch` results and add the read-only `lsp` tool. Needs at least one server; `--lsp=false` disables it. See [Language servers (LSP)](long-runs.md#language-servers-lsp) |
| `--web-fetch-allow-domain <host>` | config `web_fetch.allowed_domains`, else none | Hosts `web_fetch` may read. Repeatable; replaces the config list; requires `--web-fetch` (or `web_fetch.enabled: true`). See [Web fetch](long-runs.md#web-fetch) |
| `--lockdown` | config `untrusted_content.enabled`, else off | Untrusted-content lockdown: once the run reads untrusted content (a `web_fetch` or MCP result, a review diff, `--untrusted-task`), commands and MCP tools are refused, and `web_fetch` outside `untrusted_content.lockdown.web_fetch_allowed_domains`; `--lockdown=false` turns it off. See [Untrusted-content lockdown](permissions.md#untrusted-content-lockdown) |
| `--untrusted-task` | off | Treat the task text as untrusted (an issue body, a ticket): the run starts tainted. Requires lockdown (`--lockdown` or `untrusted_content.enabled`) and doesn't turn it on; without it, exit 2 |
| `--tool-output-files` | config `tool_output_files.enabled`, else off | Save the full text of tool results over `max_output_chars` to a per-run file the model can page with `read_file` and `grep`; `--tool-output-files=false` disables it. See [Tool output files](long-runs.md#tool-output-files) |
| `--images` | config `images.enabled`, else off | Let vision-capable models see images: the `view_image` tool and MCP image pass-through; see [Images](long-runs.md#images) |
| `--image <path>` | — | Attach a PNG, JPEG, GIF or WebP file to the task message (or a `--resume -t` follow-up). Repeatable, up to 10, each within `max_image_bytes`; needs `-t`; independent of `--images`. See [Attaching images to the task](long-runs.md#attaching-images-to-the-task) |
| `--plan-first` | config `plan.require_first`, else off | Require a plan (`update_plan`) before the first workspace-changing tool call; implies `--plan`. See [Plan first](planner.md#plan-first) |
| `--plan-file` | — | Mirror the current plan to this Markdown file after each checkpoint save (requires the plan tool) |
| `--skills` | config `skills`, else off | Discover [Agent Skills](project-context.md#agent-skills) and offer the `skill` tool |
| `--no-skills` | — | Disable skills even if the config enables them (`--no-skills=false` enables) |
| `--read-only` | config `read_only`, else off | Permit only `read_file`, `list_files`, and `grep`; blocks edits, shell, and process tools. `--read-only=false` explicitly disables it. |
| `--add-dir-read` | config `additional_dirs`, else none | Let the file tools read (not write) an existing directory outside `--cwd`; repeatable. See [Additional directories](permissions.md#additional-directories) |
| `--add-dir-write` | config `additional_dirs`, else none | Let the file tools read and write an existing directory outside `--cwd`; repeatable. See [Additional directories](permissions.md#additional-directories) |
| `--sandbox` | config `sandbox`, else off | Confine `bash`/`start_process` at the OS level: writes only under `--cwd`, a temp dir and `sandbox_writable_paths`, network off. See [Sandboxing commands](sandbox.md#sandboxing-commands) |
| `--sandbox-network` | config `sandbox_network`, else off | With `--sandbox`, allow network access |
| `--sandbox-allow-domain <host>` | config `sandbox_allowed_domains`, else none | With `--sandbox`, let commands reach only the listed hosts over HTTP(S) through a filtering proxy. Repeatable; replaces the config list. See [Network allowlist](sandbox.md#network-allowlist) |
| `--env` | config `environment`, else `local` | Where `bash`/`start_process` run: `local` (on the host) or `container` (Docker/Podman). See [Execution environments](sandbox.md#execution-environments) |
| `--command-env` | config `command_env.inherit`, else `all` | Environment `bash`/`start_process` inherit: `all`, `core` or `none`. See [Command environment](sandbox.md#command-environment) |
| `--redact-secrets` | config `redact_secrets.enabled`, else off | Mask known secret values as `[REDACTED]` in `bash`, process, MCP, `web_fetch` and hook output before the model sees it; `--redact-secrets=false` turns the config setting off. See [Redacting secrets from tool output](permissions.md#redacting-secrets-from-tool-output) |
| `--workers` | config `subagents.workers.enabled`, else off | Add the `worker` and `apply_worker` tools: a worker subagent edits files in its own Git worktree on branch `tinyagent/<run>-w<n>`. See [Workers](long-runs.md#workers) |
| `--workflow <file.yaml>` | | Run a skill workflow on the task. See [Skill workflows](project-context.md#skill-workflows) |
| `--workflow-name <name>` | | With `--workflow`: run this workflow or skill instead of routing by intent |
| `--eval <suite.yaml>` | | Run an eval suite and report pass rates; with `--dry-run`, print the trial matrix and a cost bound. See [eval-framework.md](eval-framework.md) |
| `--eval-out <dir>` | | With `--eval`: directory for `results.jsonl`, `report.json`, `report.md` and trajectories |
| `--eval-keep` | | With `--eval`: keep each trial's worktree |
| `--eval-min-pass-rate <rate>` | | With `--eval`: exit 1 if any configuration's pass rate is below this (0-1) |
| `-o, --output` | — | Trajectory JSON, rewritten after every step |
| `--snapshots` | config `snapshots`, else off | Snapshot the workspace before every tool batch that can change files, so `--rewind` can restore it. Requires `-o`. See [Rewind workspace changes](undo.md#rewind-workspace-changes) |
| `--rewind <trajectory>` | | Restore `--cwd` to a snapshot recorded in that trajectory, with `--to start\|<step>\|<snapshot-id>` (default `start`) and `--dry-run` to preview. Makes no model request |
| `--transcript <trajectory>` | | Print that trajectory as a redacted Markdown transcript on stdout and exit. Makes no model request and needs no API key; see [View a run as a transcript](runs.md#view-a-run-as-a-transcript) |
| `-v, --verbose` | off | Print tool calls and their outputs |
| `--json` | off | Write machine-readable run events as JSONL to stdout |
| `--output-schema` | | End the run with a JSON result validated against this JSON Schema file (printed to stdout) |
| `--log-db[=path]` | off | Also record run events in a local SQLite database (default `<cwd>/.tinyagent/tinyagent.db`); see [Event log database](json-output.md#event-log-database) |
| `--otel` | config `otel.enabled`, else off | Export the run as an OpenTelemetry trace over OTLP/HTTP; `--otel=false` turns the config setting off. `OTEL_*` variables alone never enable it. See [OpenTelemetry export](json-output.md#opentelemetry-export) |
| `--no-stream` | off | Wait for each full response instead of streaming it (a server that ignores the stream request and answers with plain JSON is also handled) |
| `--no-agents-md` | off | Don't append the nearest `AGENTS.md` (at or above `<cwd>`, up to the repo root; capped at 32KB) to the system prompt |
| `--scoped-agents-md` | off | Also deliver `AGENTS.md` files in directories below `<cwd>`, appended to the first file tool result that touches a path there; see [Project instructions](project-context.md#project-instructions-agentsmd). Can't be combined with `--no-agents-md` |
| `-V, --version` | | Print the version and exit |
| `--update` | | Replace this binary with the latest GitHub release (checksum-verified) and exit, printing each step to stderr; see [Updating](#updating) |
| `--check` | | Print the resolved config file, model, provider key status, fallbacks, cwd and limits (and `ask_user` when it is on or set by flag, and the [project memory](memory.md#project-memory) approval policy, store path and active/pending entry counts when memory is on or `--memory` is passed, a `lockdown:` line when [lockdown](permissions.md#untrusted-content-lockdown) is on or `--lockdown` is passed, and an `attempts:` line when `--attempts` flags are passed or the config has an `attempts:` section: the validated best-of-N plan with each attempt's model, the check count and timeouts (never the commands), `stop_on_pass`, `keep` and the worktree paths), then exit without a model request (exit 2 on a setup error) |
| `--completion bash\|zsh\|fish` | | Print a Bash, Zsh or Fish flag-name completion script and exit (`source <(tinyagent --completion bash)`; for Zsh, after `compinit`; for Fish, `tinyagent --completion fish \| source`); see [Shell completion](../README.md#shell-completion-bash-zsh-and-fish) |

## Updating

`tinyagent --update` prints one `update: <step>: <detail>` line on stderr per step: `current version` (version and binary path), `query latest release` (the API URL, then `found vX` or `latest is vX; already up to date`), `select asset` (`tinyagent-<os>-<arch>`), `download checksums` (`SHA256SUMS`), `download binary` (the URL, then the received size), `verify checksum` (the SHA-256 that matched), `install` (the path being replaced) and `done`. The final message (`updated vA -> vB (<path>)` or `already up to date (vA)`) goes to stdout, exit 0.

- **Download bar.** When stderr is a terminal, the binary download draws a `#` bar: with a known `Content-Length`, one redrawn line `  [####....]  42%  3.8 MiB / 9.0 MiB`; without one, one `#` per 256 KiB received, then the total. When stderr isn't a terminal (piped, CI logs), only the plain step lines are printed, with no carriage returns.
- **Failures** exit 1 and name the step: `error: update failed at <step>: <reason>`, for example `update failed at verify checksum: checksum mismatch for tinyagent-linux-amd64; not installing`. The binary is replaced only after the checksum matches.
- **`--json`.** Steps become JSONL events on stdout, `{"type":"update.step","step":"...","detail":"..."}`, followed by `{"type":"update.result","status":"ok"|"error","message":"..."}`; no bar is drawn and stderr only gets the error line on failure.

## Exit status

The run ends when the model writes `COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT` (`Submitted`), stops calling tools (`Completed`), or hits a limit (`StepLimitExceeded`, `CostLimitExceeded`, `TokenLimitExceeded`, `TimeLimitExceeded`), Ctrl-C (`Interrupted`), a non-retryable API error (`Error`), too many consecutive malformed tool-call steps (`RepeatedFormatError`, see below), or, with `--output-schema`, 3 consecutive invalid results (`InvalidOutput`), or stop hooks still rejecting the result after `max_stop_continuations` (`VerificationFailed`, see [Stop hooks](permissions.md#stop-hooks)), or too many consecutive responses cut off at the output-token limit (`OutputTruncated`, see [Output truncation](troubleshooting.md#output-truncation)), or, with `--ask-user`, a question for the user (`NeedsInput`, see [Asking for input](runs.md#asking-for-input)). The process exits 0 only for `Submitted` and `Completed`, 3 for `OutputTruncated`, 4 for `NeedsInput`, and 1 otherwise (2 is reserved for usage errors).

Every run ends with one summary line on stderr, so stdout keeps only the model's text:

```text
Submitted after 12 steps in 1m42s (48,213 input / 3,104 output tokens, $0.23)
```

Cost is shown only when it's above zero, and the token/cost part is left out if the run ended before any model response. When some input was read from the prompt cache, the line also shows how much, e.g. `(48,213 input / 3,104 output tokens, 41,900 cached, $0.09)`. With `-o`, the trajectory JSON also records `input_tokens` and `output_tokens` totals, plus `cache_read_tokens` and `cache_write_tokens` when they're non-zero.
