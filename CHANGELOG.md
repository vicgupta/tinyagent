# Changelog

## [Unreleased]

### Breaking

- Project memory lives only in `./.tinyagent/memory` inside `--cwd` (SQLite default `./.tinyagent/memory/memory.sqlite`). **Hard cut:** the old `./memory` folder and the old per-user JSON store (`$XDG_STATE_HOME/tinyagent/memory`, `~/.local/state/tinyagent/memory`, or the user config directory on macOS and Windows) are no longer read or imported. If the new store doesn't exist and `./memory` holds entries, one stderr `hint:` line names the new location; nothing is moved. Nothing outside `--cwd` is stat'ed, read or written for memory. Store paths that are absolute, contain `..` or resolve through a symlink outside `--cwd` are refused with a warning. `import_legacy` is still accepted but does nothing. See [Project memory](docs/memory.md#store-location) (#335).
- **BREAKING (hard cut):** `memory.toml` and plugin data are read only from inside `--cwd`. `memory.toml` is `$TINYAGENT_MEMORY_CONFIG` (now relative to `--cwd`, or absolute inside it) or `./memory.toml`; `tinyagent/memory.toml` in the user config directory is no longer read. A plugin's `PLUGIN_DATA` is now `./.tinyagent/plugin-data/<name>` instead of `<user config dir>/tinyagent/plugin-data/<name>`. Nothing is migrated: when a file or folder exists in an old location, one stderr `hint:` line names the new one. Paths that leave `--cwd` (absolute paths elsewhere, `..`, symlinks) are refused with a warning (`memory.toml`) or a config error (`PLUGIN_DATA`). The unused `config.Find` (per-user `config.yaml` lookup) is removed. See [Memory settings](docs/memory.md#project-memory) and [Plugins](docs/plugins.md) (#336).

## [v0.15.1] - 2026-10-31

### Fixes

- Interactive chat (`tinyagent chat` / `-i`): typing at the `›` prompt no longer prints a new line on every keystroke. The prompt starts with a blank line, and the line editor rewrote that newline on each redraw; it is now written once and only the prompt line is redrawn in place. Redraw also measures the prompt and input in terminal columns (escape codes stripped, CJK/emoji widths), so the cursor lands correctly after wide characters, and lines longer than the terminal wrap and redraw correctly. Regression in v0.15.0 (#346); `--color` and `NO_COLOR` are unchanged.

## [v0.15.0] - 2026-10-31

### Features

- Prettier interactive chat: replies render Markdown for the terminal (headings, bold/italic, inline and fenced code with a language label and light highlighting, nested and task lists, blockquotes, links with their URL (OSC 8 hyperlinks where the terminal supports them), rules and tables), wrapped to the terminal width and rendered block by block as they stream. New `/raw` command and `chat_markdown` config key to show raw Markdown instead. Also: a clearer banner, a `›` prompt, a thinking spinner and a one-line tool activity summary per step, readable errors with a retry hint, formatted `/help` and `/cost`, and an exit summary with the saved path and resume command. Respects `--color` and `NO_COLOR` (plain ASCII structure, no escape codes); enables VT processing on Windows. `--json` output and saved transcripts are unchanged (raw Markdown). New package `internal/termmd`, no new dependencies (#346).

### Fixes

- Tool output truncation is rune-safe: multibyte output (CJK, emoji) is no longer cut mid-character, and the elided count is in characters; one shared implementation in `internal/tools/truncate.go` (#344, closes #312).

### Documentation

- README and CONTRIBUTING list all ten base tools and current dependencies, in sync with the code (#345, closes #313).

### Other

- Internal refactor with no behavior change: provider HTTP client, retries and price table move from package globals onto an injected `model.Runtime` (#343, closes #311).

## [v0.14.0] - 2026-10-31

### Features

- OpenCode handoff provider (#341): `-m opencode/<provider>/<model>` (or `opencode/default`) runs your installed `opencode run --format json` in `--cwd` and returns its final answer, tokens and cost; `--read-only` maps to OpenCode's `plan` agent plus an inline permission config denying edits, commands and network tools. Binary from PATH or `opencode.path`; `tinyagent --check` shows the binary and its version. OpenCode's credentials are never read or copied. See [docs/models.md](docs/models.md#opencode).

### Other

- Internal refactor with no behavior change: fresh runs, `--resume` and chat/ACP sessions build their tool executor through one shared builder, so MCP, plugin and command-tool wiring can't drift between them (#340, closes #310).

## [v0.13.0] - 2026-10-31

### Features

- Interactive chat mode (#302, closes #300): `tinyagent chat` / `-i, --interactive` with a built-in line editor and Up/Down history (`<cwd>/history/chat_history`, `--no-chat-history` / `chat_history: false`), checkpoints auto-saved to `.tinyagent/chats/`, `--resume`, per-turn limits, Ctrl-C to cancel a turn, and slash commands `/help /exit /quit /cost /new /clear /save /compact`. Refuses without a TTY or with conflicting flags (exit 2).
- Issue triage (#303, closes #277): `tinyagent triage <issues.json>` / `--triage` ranks `gh issue list --json` output into a deterministic next-up list (`schema_version: 1` JSON plus a Markdown table) with reasons per entry and `stale`, `duplicate-of:#N`, `blocked` and `pull-request` flags. Offline, no model call; `--triage-out`, `--triage-stale-days`.
- Workflow observer (#305, closes #278): `--workflow-observe` turns a `Failed` or `Blocked` `--workflow` run into one proposed ratchet rule with a ready-to-run `tinyagent ratchet add` command, keeps a ledger in `.tinyagent/observer/`, and flags recurring failures; new `observer.proposed` and `observer.recurrence` events. Rules are never applied automatically.
- Contract docs in skill workflows (#315, closes #281): new `write-contract` skill and `contract` workflow; `plan-tasks` reads contract docs from `contracts_dir` and flags stale ones.
- Workflow stage attempts record `unverified` in the `-o` record and `stage.finished` event (#321), report only.

### Changes

- **Behavior change for `--workflow` (#321, closes #284):** verify-on-stop is now on by default for every workflow stage; pass `--verify-on-stop=false` to opt out. Plain interactive and headless runs are unchanged (still off by default).
- Chat and ACP sessions now offer the config's `command_tools` on every turn (#324); read-only sessions still don't.
- Checkpoints taken with workers on now resume instead of failing with "tool set has changed" (#322, closes #306).
- `--resume` compatibility checks have one owner, `internal/resume` (#333, #334, #338, part of #310). Messages and exit codes are unchanged, except that `--no-agents-md` now reads "conflicts with the checkpoint's recorded loading mode" and, with several conflicts, a different one may be reported first.

### Documentation

- The 256 KiB MCP result cap is documented for `--tool-output-files` (#316, closes #282).
- Short PR body template in `.github/pull_request_template.md` (#320, closes #283).

### Other

- Internal refactors with no behavior change: shared session setup for ACP and chat (#301, closes #299), `tools.Selection` owns the tool catalog and resume fingerprint (#322), a single `Executor.RunResult` entry point (#323, closes #307), `agent.New(Options)` validates construction invariants (#324, closes #308), and `Executor.Child(ChildSpec)` for delegate and worker executors (#325, closes #309).

## [v0.12.0] - 2026-10-31

### Breaking Changes

- Config is now per project (#294): tinyagent reads `-c <file>`, else `./tinyagent.yaml` in `--cwd`, else built-in defaults. The old user config is no longer read (hard cut); a one-line note points to `tinyagent init --migrate`, which copies it to `./tinyagent.yaml` once and never deletes it. An invalid project file is an error, with no fallback.
- Skill workflows: a stage whose check keeps failing on its merits now ends the run `Blocked` with exit code 5 instead of `Failed`/exit 1 (#289). Scripts that test for exit 1 on a failed stage should also handle 5.

### Features

- Per-project config (#294): `tinyagent init` writes a commented `./tinyagent.yaml` (`--force`, `--migrate`; `--init-config` stays as an alias). Settings that launch programs or loosen security (`mcp_servers`, `hooks`, `command_tools`, `permissions`, ...) are honored from a project file only with `--trust-config`; a `-c` file is always trusted. `--check` and `-v` show the config source: `<path> (from -c)`, `<path> (cwd)` or `none (built-in defaults)`.
- CLI handoff providers (#287): `-m claude-code/<model>` and `-m codex-exec/<model>` run the Claude Code or Codex CLI you installed and signed in to, and return its final answer. Binary from PATH or `claude_code.path` / `codex_exec.path`; credentials are never read or copied.
- Spec gate for skill workflows (#288, closes #275): `gate: spec` on a stage re-plans once, then stops a plan whose tasks lack acceptance criteria or a runnable `Verify:` command; new `stage.gate` event.
- Blocked workflow status (#289, closes #276): unsatisfiable checks end the run `Blocked` (exit 5), with `blocked: true` on `workflow.finished`.
- `tinyagent skills validate <path>...` / `--skills-validate` (#290, closes #279): check skill files, skills directories and workflow files without a model call, for CI or pre-commit.
- `verify-behavior` workflow stage (#286, closes #274): the harness runs behavioral cases from a `behavior:` file and passes the stage only on observed exit code and output.
- Color output (#295, closes #291): `--color auto|always|never` (default `auto`), honoring `NO_COLOR`; piped, redirected and `--json` output stays plain.
- `--check` summary (#296, closes #292): a setup summary table (config source, model, provider and key set/missing, sandbox, permissions, lockdown, limits), and `--check --json` with a versioned schema for CI and scripts.
- Setup hints (#297, closes #293): known setup failures (invalid config setting, unavailable sandbox, missing API key, missing `claude`/`codex` CLI) print one `hint:` line with the next action; config errors found after loading name the config file and rule. `--check --json` reports the hint in `error.hint`.

## [v0.11.1] - 2026-10-31

### Documentation

- The `--workflow` file format is now unambiguous: workflow files are YAML and skills are Markdown (#271).
- Subagents are documented as read-only researchers and workers as read-write builders; no renames in code or config (#272).

## [v0.11.0] - 2026-10-31

### Features

- Project rules from agent failures (#254, closes #249): `tinyagent ratchet add|list|remove|path` records rules in `.tinyagent/ratchet.toml` with the incident that caused them and an expiry date or review condition; active rules join the system prompt and can be enforced as permission deny rules.
- Computational checks for verify-on-stop (#255, part of #250): `verify_on_stop.checks` / repeatable `--verify-check <cmd>` run fast deterministic checks (lint, type check, tests), cheapest first, before a run that changed files may finish.
- Inferential review for verify-on-stop (#256, closes #250): `verify_on_stop.review` / `--verify-review` runs a model review last, once every check passes, and reports its findings.
- Session handoff (#257, closes #251): `--handoff` / `handoff.enabled` leaves `.tinyagent/handoff.md` with what a run did and what's next; the next fresh run reads it as data, and an optional startup command runs before the first turn.
- `--update` progress (#258): every step is reported on stderr with a `#` download bar, and failures name the step that failed.
- Brief help (#259): bare `tinyagent` and `tinyagent --help` show two examples and a Common options block; `--help --verbose` lists every flag.
- Experimental GitHub Action eval mode (#261, closes #260): `eval`, `eval-out`, `eval-min-pass-rate`, `cost-limit` and `time-limit` inputs run suites with the existing verified installer, expose report paths, gate CI via `fail-on-error`, and optionally append `report.md` to the job summary. Artifact uploads remain workflow-managed.
- Model routing (#262-#266): `models:` roles `main`, `auxiliary` (compaction, verify review) and `delegated` (`delegate`, `worker`) with `--aux-model`, `--delegate-model` and repeatable `--route task=spec`; per-role `fallback_models`; task-based `models.delegated.routes` (`match`, `type`) with a `model.route` event; `--no-routing`; and `usage_by_role` in trajectories, the `result` event, the summary line, the event log (`run_usage` table) and eval reports. `subagents.model` and `subagents.workers.model` remain as aliases.
- Bubblewrap sandbox engine (#269): `--sandbox-backend auto|landlock|bwrap` / `sandbox_backend` / `tinyagent sandbox --backend`; `auto` uses bwrap with Landlock stacked inside when bwrap works, else Landlock alone.

### Other

- Docs synced for verify-on-stop, token budgets and help changes (#253); README documentation table grouped by topic (#267); new top-level Tasks and Models pages (#268).

## [v0.10.3] - 2026-10-30

### Features

- Token budget (#246, part of #236): `--token-limit n` / config `token_limit` caps provider-reported input+output tokens for the whole run. At the limit the run stops cleanly with the new status `TokenLimitExceeded` (exit 1, resumable; the printed `--resume` command doubles the limit). Delegates and workers get only the tokens left in the parent's budget, and a worker batch shares one token gate. Off by default (0 = unlimited).
- 80% budget warning (#246): when a token or cost budget reaches 80%, one "Budget notice" user message is added before the next request and a `budget.warning` event goes to the JSON stream, log DB and OTel trace.
- Token limit everywhere (#247, closes #236): `--check` shows the token limit on the limits line, `--init-config` includes a commented `token_limit: 0`, `--eval` suite tasks accept `limits.token_limit`, and `--workflow` accepts `token_limit` in `defaults` and per stage.

### Changes

- **Behavior change for `--attempts` (#247):** `--token-limit` is now one budget shared by all attempts, like `--cost-limit` and `--time-limit`. Each attempt gets the tokens left, no new attempt starts once the budget is spent, and an attempt ending `TokenLimitExceeded` stops the run with `budget exhausted`. Previously each attempt got the full limit.
- The turn cap (`--step-limit`) stays off by default. A top-level `--token-limit` with `--eval` is still a per-trial cap.

## [v0.10.2] - 2026-10-30

### Changes

- A bare subcommand (`tinyagent sandbox`, `memory`, `planner`, `plugins`, `tinygo`) at an interactive terminal now prints the same help as `tinyagent <sub> --help` and exits 0 (#244), for every registered subcommand. Previously `sandbox`, `memory` and `planner` failed with `error: --task is required`. With stdin piped or redirected they keep the old behavior, so scripts are unaffected; actions, flags and prompts that start with a subcommand word are unchanged.

## [v0.10.1] - 2026-10-30

### Changes

- Plain `tinyagent` with no arguments at an interactive terminal now prints the same brief help as `tinyagent --help` and exits 0 (#242), instead of `error: --task is required` and the full flag list. With stdin piped or redirected it still exits 2 with that error, so scripts and CI are unaffected.

## [v0.10.0] - 2026-10-30

### Features

- Verify before stop (#235): an opt-in guard (`--verify-on-stop`, `--verify-command`, `--max-verify-reprompts`, `verify_on_stop` config) that re-prompts the model to run a verification command before finishing on an unverified code change. Runs that still end unverified are marked in the summary line (`, unverified changes`), the `--json` result, trajectories and `verify.reprompt`/`verify.unverified` events. Off by default.

### Changes

- Cleaner CLI help (#240): `tinyagent --help` shows the usage line and every subcommand with a one-line description, now including `tinygo` (previously missing), plus pointers to `--help --verbose` and `docs/subcommands.md`. `--help --verbose` groups flags into sections (model and provider, run control, output and logging, sandbox and permissions, memory and planner, plugins and TinyGo, and more), and subcommand help has aligned, wrapped columns. Tests check that help covers every subcommand and registered flag. TinyGo remains an external dependency, shipped separately by the TinyGo project and downloaded on demand; it is not part of tinyagent.

## [v0.9.0] - 2026-10-30

### Features

- Project memory (#170): `--memory` loads approved notes into the prompt and offers `remember`, `memory_search` and `memory_list`; markdown or SQLite backends, `memory.toml`, approval before entries go live, `--memory-list/-path/-approve/-forget/-help`, and ACP support. Notes are shown to the model as data, not instructions, and symlinked memory folders are refused. Off by default.
- Task planner: `--plan-first` (`plan.require_first`) requires an `update_plan` before any workspace-changing tool call; plan steps can be `failed`, and plan updates print to stdout.
- Streaming: assistant text streams as `assistant.delta` events in `--json` mode; servers that ignore streaming and return plain JSON are handled.
- `undo_file` tool: restores a file's state from before the session's first `write_file`/`edit_file`/`apply_patch` change.
- Subcommands as shortcuts for existing flags: `tinyagent sandbox`, `memory`, `planner`, `plugins` and `tinygo`, with `tinyagent help <subcommand>`. Every flag form still works.
- Untrusted-content lockdown (#171): `--lockdown` and `--untrusted-task`, configurable taint sources, per-MCP-server settings, and narrowed command and container network after taint.
- Best-of-N attempts (#172): `--attempts`, `--attempt-model`, `--attempt-check` and the `attempts` config, with events, `--check` output and GitHub Action support.
- Agent Plugins (#173): validated `plugin.json` manifests, the `plugins` config key, plugin skills and MCP servers, and `--list-plugins`.
- WebAssembly plugin modules: plugins can ship modules that run through Extism under deny-by-default permissions, exposed as `<plugin>__<export>` tools. Every version is kept in an append-only store with `tinyagent plugins rollback` and `tinyagent plugins prune`.
- Go source plugin modules built with TinyGo. **TinyGo is an external dependency, shipped separately by the TinyGo project and not part of tinyagent;** it is never embedded in the tinyagent binary or repository. tinyagent downloads a pinned TinyGo version on first use (or with `tinyagent tinygo install`), verifies its checksum, caches it locally and updates it with `tinyagent tinygo update`.

### Changes

- The README is now a short overview; the full reference lives in `docs/`.
- Shell completion completes fixed flag values (#180). Flaky worktree and command-env tests were fixed.

## [v0.8.0] - 2026-10-27

### Features

- Add an opt-in tool permission policy (`permissions` config with `default`, `non_interactive` and `rules`): declarative `allow`/`ask`/`deny` rules matched on a tool call's name and one primary argument (a `command` for `bash`/`start_process`/command tools, a `path` for the file tools in the `file_access` syntax, or a `host` for `web_fetch` in the `sandbox_allowed_domains` syntax). Precedence is `deny` > `ask` > `allow` > `default`; compound shell commands are split on unquoted `&&`/`||`/`;`/`|`/newlines, with a deny/ask matching any segment, an allow only when every segment is covered, and `$(...)`/backticks/`eval` never matching an allow. An `ask` is answered through the channel the run has: a single-line terminal prompt (`y`/`a`/`n <reason>` read from `/dev/tty`, never stdin), ACP `session/request_permission` (`allow_once`/`allow_always`/`reject_once`; `cancelled` cancels the turn), or the `non_interactive` fallback (`deny` by default) for `--json` and piped/CI runs. A denial is returned to the model as an error and skips `pre_tool` hooks; an allowed call still runs them. `update_plan`, `ask_user` and `submit_result` are exempt. Every decision emits a `permission` event (`--json`, `--log-db`, `--otel`), `run.started` and trajectories record a `permissions` summary, `-v` prints one line per non-default decision, and `--check` prints the rule count, default and resolved non-interactive behavior. Rules come only from the user config, `-c` or a profile (never the repository); a profile's `rules` replace the base list; and invalid settings exit 2 naming the file, profile and rule index. Off by default with no change to configs without `permissions`; older tinyagent versions reject a config file containing the `permissions` key.
- Add opt-in worker subagents (`--workers` / `subagents.workers.enabled`; first part of #169, partial and off by default). A new `worker` tool runs a child agent with write tools in its own Git worktree (`.tinyagent/worktrees/<run>-wN` on branch `tinyagent/<run>-wN`) from a snapshot of the working tree, commits its result, and reports status, branch, commit and diff stat; `apply_worker` applies a finished worker's change atomically to the working tree (conflicts change nothing). Config: `model`, `max_steps` (default 60), `max_concurrent` (1-4), `tools`. Workers inherit the tool permission policy, count toward `--cost-limit`, and never get `delegate`, worker tools, `ask_user`, `update_plan`, MCP or `web_fetch`. `subagent.*` events and trajectories gain `kind`, `branch`, `commit` and file counts, a new `worker.applied` event is emitted, and `--resume` rejects a conflicting `--workers` (exit 2).
- The end-of-run summary lists every worker branch the run kept (unapplied first) with `review:`, `discard:` or `clean up:` commands, and the `--json` `result` event gains `worker_branches` when any are kept.

## [v0.7.0] - 2026-10-27

### Features

- Add `--workflow file.yaml` (with `--workflow-name`), a skill-harness runner that executes a sequence of minimal Markdown skills as one automatic pipeline. Tasks are routed to a workflow or skill by intent (or `default:`), each stage runs as a fresh child tinyagent run that receives the previous stage's output, and each stage is gated by its success check with retries. A failing stage stops the run with exit 1 naming the stage and reason; invalid skill or workflow files and routing failures exit 2 before any model request. `-o` writes a workflow record with per-stage trajectories, and `--json` emits `workflow.started`, `stage.started`, `stage.finished` and `workflow.finished` events. See the README's "Skill workflows" section and `docs/workflows/`.
- Add eval mode: `tinyagent --eval suite.yaml --eval-out dir` runs a declarative `version: 1` suite of coding tasks across a models × profiles × trials matrix, each trial in its own detached worktree as a child tinyagent run, grades trials with argv checks, and reports pass rate, pass@k, cost, tokens, steps and wall time, with suite-level `--cost-limit`/`--time-limit` budgets, `--eval-keep`, and a `results.jsonl` output.

### Changes

- `tinyagent --help` now prints a brief 3-line summary; `--help --verbose` lists every flag grouped under headings, sorted, aligned and wrapped, with defaults shown, including tool flags previously missing from help.

## [v0.6.0] - 2026-10-27

### Features

- Add opt-in OpenTelemetry trace export (`--otel`, `otel` config with `enabled`, `endpoint`, `protocol`, `headers_env`, `timeout`, `service_name`, `content`, `max_attribute_bytes`). Each invocation is sent over OTLP/HTTP (`http/protobuf` or `http/json`, encoded with the standard library) as one trace: an `invoke_agent tinyagent` root span with `chat {model}`, `execute_tool {name}`, `hook {event}` and `invoke_agent delegate` child spans using the GenAI semantic conventions, and other events as span events. The standard `OTEL_EXPORTER_OTLP_*`, `OTEL_SERVICE_NAME` and `OTEL_RESOURCE_ATTRIBUTES` variables are honored but never enable export on their own; header values come only from the environment and are never printed. An inbound `TRACEPARENT` parents the root span. Content is off by default and redacted and capped when on. Transient export errors are retried within `timeout`, and any failure is one warning that never changes the exit code, stdout, `result` event or trajectory. `run.started` gains an `otel` field with `--otel`, and the `--json`/`--log-db` stream gains an additive `model.request` event (model, duration, token usage or error per model request). Bad settings, including `grpc`, exit 2. Older tinyagent versions reject a config file containing the `otel` key.
- Add Fish shell completion: `tinyagent --completion fish` prints native `complete -c tinyagent` registrations for every flag (long and short names, with filename completion), alongside the existing Bash and Zsh support. It works offline and ignores config and credentials.

### Docs

- Add an eval framework design and usage guide (`docs/eval-framework.md`).

## [v0.5.0] - 2026-10-26

### Features

- Suggest the closest flags when a flag is mistyped: an unknown flag now exits 2 with one line such as `error: unknown flag --sandbxo; did you mean --sandbox? (see tinyagent --help)` (up to three names within a small edit distance or extending what was typed, echoed with the dashes you used) instead of Go's `flag provided but not defined: -sandbxo` followed by the full usage. Other parse errors and `--help` are unchanged.
- Add an experimental `--acp` mode that speaks the Agent Client Protocol (version 1) over stdin/stdout, so Zed (`agent_servers`), JetBrains IDEs (`~/.jetbrains/acp.json`) and other ACP clients can run tinyagent as an external agent. Each `session/new` runs in the client's absolute `cwd` with the same config, profile, guardrails and flags as a headless run. Setup errors (bad `cwd`, invalid config, missing key) are JSON-RPC errors with the usual `hint:`. Later `session/prompt`s continue the conversation exactly like `--resume -t`. Assistant text, each tool call (`tool_call` → `in_progress` → `completed`/`failed`, with kind, absolute locations and result text), plans and context usage are streamed as `session/update`s. `session/cancel` stops the turn like Ctrl-C and answers `cancelled`. Stop reasons map to `end_turn`, `max_turn_requests`, `max_tokens` and `cancelled`; other statuses end the turn with a message naming them. Limits apply per prompt, `--log-db` tags each turn with the session id, and stdout carries only protocol messages. Client-supplied MCP servers are ignored (and named in the first reply) unless the new `acp.client_mcp_servers` config key is on. `--acp` with a task, resume, review, `--json` or other mode flags exits 2. Without `--acp` nothing changes; older tinyagent versions reject a config file containing the `acp` key.
- Let the model read MCP resources from configured local MCP servers. A new optional per-server `resources` allowlist (exact URIs or prefixes ending in `*`; empty, bare `*`, inner `*` and duplicate patterns exit 2) adds two read-only tools: `list_mcp_resources` (allowlisted resources and resource templates, with hidden-entry counts, a `cursor` and a 100-page cap) and `read_mcp_resource` (`resources/read` for an allowlisted URI only; text joined with per-item headers, blobs replaced by an omitted-blob marker or passed through as images with `--images`). Results get the MCP result cap, redaction, tool output files, hooks, events and trajectories. A server may now have only `resources` (no `tools`); a server with `resources` but no declared `resources` capability exits 2. `run.started` lists the patterns as `mcp_resources`, `--check` shows per-server counts, and `-v` prints one line per read. Configs without `resources` are unchanged. Older tinyagent versions reject a config file containing the `resources` key.
- Add opt-in secret redaction for tool output (`redact_secrets` config with `enabled`, `env`, `allow` and `patterns`; `--redact-secrets`). When on, the exact values of secrets tinyagent knows about (secret-named environment variables, provider keys, MCP and hook `env` values, named-provider key and header variables, `command_env.set` values, `codex` tokens, and variables listed in `env`, minus `allow`; values under 6 characters skipped) are replaced with `[REDACTED]` in `bash`, `start_process`, `read_process`, `stop_process`, run-end background process tails, MCP results, `web_fetch`, `post_tool` hook feedback and `stop` hook reasons, before truncation and tool output files, so the provider request, trajectory, `--json`, `--log-db`, `post_tool` hook input and saved files all get masked text. A secret split across `read_process` reads is still masked whole. `patterns: true` also masks token shapes. Built-in file tools are not redacted (use `file_access`). A stderr line gives value and variable counts, `-v` lists names, `tool.result` events get `redactions`, and `run.started` and trajectories record `redact_secrets` (names only). Invalid settings exit 2. Off by default with no change; older tinyagent versions reject a config file containing the `redact_secrets` key.
- Add an official composite GitHub Action (`action.yml`; `uses: vicgupta/tinyagent@vX.Y.Z`) that installs the matching release on Linux and macOS runners, checks it against `SHA256SUMS`, and runs one task or review (`task`/`task-file` on stdin, `review-base`, `review-commit`, `output-schema`, `read-only`, `sandbox`, `model`, `profile`, `config`, line-per-argv `args`). It sets `status`, `submission`, `structured-output`, `steps`, `cost-usd`, `exit-code`, `trajectory` and `events` outputs from the `result` event behind random delimiters, fails the step per `fail-on-error` (exit 2 and install failures always fail), and can append the `--transcript` to the job summary (`job-summary`). Under a non-tag ref, `version` (or `tinyagent-path`) is required; `latest` is used only when given. The README's new GitHub Actions section covers pinning and running on untrusted input. No CLI change.
- Add an opt-in `ask_user` tool (`--ask-user`, `ask_user` config with `enabled` and `max_questions`, default 3, range 1-20) for headless runs. A valid `ask_user` call (`question`, optional 2-6 `options`) alone in its message ends the run with the new status `NeedsInput` and the new **exit code 4**, records the call as pending with a `pending_question` in the trajectory, prints the question on stdout and the options plus a ready-to-paste `--resume ... -t "<answer>"` command on stderr, and adds a `question` object to the `--json`/`--log-db` `result` event. `--resume <trajectory> -t "<answer>"` delivers the answer as that call's tool result (recorded as a resume boundary with `answer: true`). `ask_user` mixed with other calls runs none of them, malformed arguments are format errors, and the per-run question budget (`questions_asked`, counted across resumes) turns further calls into errors. No hooks run for it; it is offered with `--read-only`, never to subagents; `--ask-user` without `-o` or with `--output-schema` exits 2. Without the opt-in nothing changes; older tinyagent versions refuse `NeedsInput` checkpoints and reject a config file containing the `ask_user` key.
- Add `--transcript <trajectory.json>`, which prints a saved trajectory as a Markdown transcript on stdout and exits, with no config, API key, model request or file writes: a header (status, model, steps, tokens, cost, settings), the task, one `### Step N` section per step with assistant text, pretty-printed tool calls and collapsed tool results, plan updates, compactions, model switches, subagents and resume/follow-up boundaries where they happened, and the submission or structured output. Model- and tool-sourced text is fenced so it can't render as images, links or HTML; secrets are redacted with the event-log redactor; images become placeholders; tool results and arguments are cut to 2,000 characters. It renders any trajectory tinyagent writes, including ones `--resume` refuses. Use it for audits or `>> "$GITHUB_STEP_SUMMARY"`.
- Add review mode: `--review` (staged, unstaged and untracked changes against `HEAD`), `--review-base <ref>` (`merge-base..HEAD`) or `--review-commit <rev>` (one commit against its first parent). tinyagent collects the diff itself with read-only `git` commands that can't start repository-configured programs (`--no-ext-diff`, `--no-textconv`, `core.fsmonitor=false`, filter drivers off, `GIT_OPTIONAL_LOCKS=0`), limited to `--cwd`, and sends it in the first user message with built-in review instructions (embedded `review.txt`) and optional `-t` focus instructions. The run is forced read-only. Paths hidden by `file_access.deny_read` are left out and counted, the diff text is capped at 200,000 bytes (every file stays in the summary; left-out files are named), and a clean change set prints `nothing to review` and exits 0 without a request. Git and ref errors, two targets, and combining with `--read-only=false`, `--resume`, `--rewind`, `--worktree`, `--snapshots`, `--check`, `--init-config` or `--update` exit 2 before any request. `run.started`, trajectories and the event log record a `review` object, and `-v` prints a `review:` line. Without a review flag nothing changes.
- Add a repeatable `--image <path>` that attaches PNG, JPEG, GIF or WebP files (sniffed by content, up to 10, each within `max_image_bytes`) to the task message, or to a `--resume -t` follow-up, for every built-in provider: Anthropic `image` blocks, Responses/Codex `input_image` parts, and Chat Completions `image_url` parts (`openai` chat, `openrouter`, `ollama`). The text gets one `[attached image N: name, mime, bytes]` label per image. Validation failures and `--image` without a task exit 2 before any request. `run.started` lists attachment metadata (never data), and `-v` prints one line per image. Images are stored in the checkpoint and kept through resume and compaction. The flag is independent of `--images`, and without it requests, events and checkpoints are unchanged. Older tinyagent versions refuse checkpoints containing attachments.

- Add named config profiles: a `profiles:` section in the user config or `-c` file and `--profile <name>`, which overlays one profile on the file's base settings for that run (defaults, then base, then profile, then flags). Mappings merge recursively; scalars and lists replace. Every profile is validated on every run (names, nesting, `null` values, unknown keys), the merged result goes through the existing checks with errors naming the file and profile, and all of these exit 2 before any model request, as do an undefined profile and `--profile` without a config file. The profile name is recorded as `profile` in trajectories and `run.started`, printed with `-v`, and reported by `--check`; `--resume` accepts `--profile`, and `--rewind` rejects it. Without `--profile` nothing changes. Older tinyagent versions reject a config file containing the `profiles` key.
- Add `--completion bash`, which prints a sourceable Bash script completing tinyagent's flag names (generated from the registered flags, short aliases included) and exits 0 without a task, credentials or config. Non-flag words keep normal filename completion; other shell names exit 2. Load it with `source <(tinyagent --completion bash)`.
- Add opt-in command tools (`command_tools` config, user config or `-c` only): each entry becomes a model-callable tool with a name, description, JSON Schema for its arguments and a fixed `argv` template whose `{{name}}` elements are filled with the schema-checked arguments and run without a shell, in the run's execution environment, `--sandbox` and `command_env`, with `bash`'s result format, timeout and truncation. Bad definitions exit 2 before the first request; they are never offered with `--read-only` or to subagents; `-v` prints the final argv, `run.started` lists them and trajectories record each definition (`command_tools`). Older tinyagent versions reject a config file containing the `command_tools` key.
- Print a ready-to-paste `resume: tinyagent --resume ...` line on stderr after the summary when a run with `-o` stops at a step, cost or time limit, is interrupted, or ends `OutputTruncated`. It uses the resolved `--cwd`, doubles the limit that was hit, and picks a new `-o` path (`run-2.json`, then `run-3.json`, ...). Not printed with `--json` or for `--output-schema` runs; exit codes, trajectories and events are unchanged.
- Add `--init-config` to create a small commented starter config at the user config path or `-c PATH`, with private permissions and no overwrites or model request.
- Add opt-in scoped `AGENTS.md` delivery (`--scoped-agents-md`, `scoped_agents_md` config): the first successful built-in file tool call touching a path below `--cwd` gets every `AGENTS.md` between `--cwd` and that path appended to its result (outermost first, once per context, re-sent after compaction). Files are capped at 32 KiB each and 64 KiB per run; files denied by `file_access`, symlinks leaving `--cwd`, and files modified during the run are skipped. Deliveries are reported with `-v`, as `instructions.loaded`/`instructions.skipped` events, in `run.started`, and in trajectories (`scoped_agents_md_enabled`, `scoped_agents_md`, `scoped_agents_md_loads`); `--resume` keeps the setting, and combining it with `--no-agents-md` exits 2. Older tinyagent versions reject a config file containing the `scoped_agents_md` key.
- Add `--check`, which prints the resolved config file, model, provider key status (never the key), fallback models, working directory and limits, each with its source, and exits without a model request. Setup errors a run would hit (unknown provider, bad config, missing key, bad `--cwd`) are reported with the same message and hint and exit 2. It can't be combined with `-t`, `--resume`, `--rewind` or `--worktree`.
- Add an opt-in model fallback chain (`fallback_models` config, repeatable `--fallback-model`, `--no-fallback`): when a request still fails with `429`, `5xx` or a network error after retries, tinyagent switches to the next of up to 3 listed models, resends the conversation (without another provider's reasoning state) and stays on it for the rest of the invocation. Other errors never switch models. Entries are validated before any request (exit 2), switches are reported on stderr, as `model.fallback` events and in trajectories (`fallback_models`, `model_switches`, `active_model`), and an exhausted chain names every model tried. Older tinyagent versions reject a config file containing the `fallback_models` key.
- Add opt-in language-server feedback (`--lsp`, `lsp` config): tinyagent starts the stdio language servers listed in `lsp.servers` (user config or `-c` only, minimal environment), appends their error diagnostics to `write_file`, `edit_file` and `apply_patch` results (before `post_tool` hooks), and adds a read-only `lsp` tool for `diagnostics`, `definition` and `references`. Servers start lazily, are restarted once after a crash, are marked unavailable (with a warning) when they can't start, and are always shut down with the run. `lsp.server` events, `run.started` and trajectories (`lsp_enabled`, `lsp_servers`) record them; `--resume` keeps the setting; `--sandbox` and `--env container` refuse it. Older tinyagent versions reject a config file containing the `lsp` key.
- Add opt-in `--worktree[=name]`, which runs a task in a new Git worktree at `<repo>/.tinyagent/worktrees/<name>` on branch `tinyagent/<name>`, created from `HEAD` (repository hooks skipped) and kept afterwards, so runs never touch your checkout and can run in parallel. Uncommitted changes aren't carried over (with a warning), setup failures exit 2 with nothing left behind, and `run.started` and trajectories record `worktree` (`path`, `branch`, `base_commit`, `repo_root`). Resume and rewind by passing the worktree path as `--cwd`.
- Add opt-in additional directories (`--add-dir-read`, `--add-dir-write`, `additional_dirs` config) that let the file tools reach up to 10 existing directories outside `--cwd`, each with an explicit `read` or `write` mode. Read directories refuse writes (including via symlinks and atomically for `apply_patch`), `--read-only` makes all of them read, and `file_access` rules apply inside them. They are listed in the system prompt, mounted into the `container` environment (read ones `:ro`), added to `--sandbox` writable roots when `write`, and recorded as `additional_dirs` in trajectories and `run.started`. Snapshots still cover only `--cwd`, with a warning. Older tinyagent versions reject a config file containing the `additional_dirs` key.
- Add an opt-in `web_fetch` tool (`--web-fetch`, `web_fetch` config) that reads `http`/`https` URLs on an allowlist (`web_fetch.allowed_domains` or the repeatable `--web-fetch-allow-domain`) and returns them as text, with HTML converted to readable text. Non-public addresses are refused unless listed, redirects are re-checked, and size and time are capped (`max_bytes`, `timeout_seconds`). It is separate from command networking and `--sandbox`, offered with `--read-only`, never given to subagents, reported as `web_fetch` events, and recorded in trajectories (`web_fetch_enabled`). Older tinyagent versions reject a config file containing the `web_fetch` key.
- Add opt-in file access rules (`file_access.deny_read` / `deny_write` config) with gitignore-style patterns relative to `--cwd`. `read_file`, `view_image`, `write_file`, `edit_file` and `apply_patch` refuse denied paths (checked through symlinks), `list_files` and `grep` skip them and report only a count of hidden paths, and `deny_read` paths are also write-denied. Rules are config-only, recorded in trajectories and `run.started`, and do not restrict `bash`, `start_process`, hooks or MCP servers.
- Add opt-in workspace snapshots (`--snapshots`, `snapshots` config) and `--rewind <trajectory> --cwd <dir> [--to start|<step>|<id>] [--dry-run]`, which restores files modified, created or deleted during a run. Snapshots are taken at the start, before each tool batch that can change files, and at the end, in a local content-addressed store under `.tinyagent/snapshots`; gitignored and oversized files are left alone, and every rewind first saves a `pre-rewind` snapshot so it can be undone.
- Add an opt-in command environment policy (`command_env` config, `--command-env all|core|none`) that controls which environment variables `bash` and `start_process` commands inherit: a trimmed `core` or empty base, `exclude_secrets` (secret-named variables and tinyagent's provider keys), `exclude`/`pass` globs and fixed `set` values. The default still inherits everything.
- Define named model providers in config (`providers`) with an `api` (`chat`, `responses` or `anthropic`), `base_url`, an API key read from an environment variable, extra headers, and per-model price and context-window metadata, selected with `-m <id>/<model>`. Provider config errors and missing key variables exit 2 before any request; keys and header values are never recorded; `run.started` and trajectories record `provider_api` and `provider_base_url`, and `--resume` refuses a changed provider.
- Add `--max-output-tokens` (config `max_output_tokens`) to set the per-request output-token budget for Anthropic, OpenAI, OpenRouter and Ollama.
- Add a sandbox network allowlist: `sandbox_allowed_domains` (or the repeatable `--sandbox-allow-domain`) lets `--sandbox` commands reach only the listed hosts over HTTP(S) through a tinyagent-run filtering proxy that refuses loopback, private and metadata addresses. On Linux each command runs in its own network namespace (needs unprivileged user namespaces; fails closed otherwise). Refusals are reported as `sandbox.network_denied` events and in trajectories.
- Continue a finished (`Submitted`/`Completed`) run with a follow-up task: `--resume <trajectory> -t "<follow-up>"` sends the saved conversation plus a follow-up notice and the new instruction, keeps step/cost totals cumulative, and records `follow_up` in the resume provenance and `run.started`.
- Discover Agent Skills (`SKILL.md`) from `.agents/skills` in the project and `~/.agents/skills`, list them in the system prompt, and load them on demand with a new `skill` tool (opt-in via `--skills` or `skills: true`).
- Run user-configured `pre_tool` and `post_tool` hook commands around tool calls to deny calls or append feedback.
- Add `stop` hooks that check a run before it finishes successfully and can send the model back to work, capped by `hooks.max_stop_continuations`, with the new `VerificationFailed` status.
- Add `--output-schema` to end runs with a validated JSON result on stdout (or `structured_output` in `--json`), with the new `InvalidOutput` status.
- Add an opt-in SQLite event log (`--log-db`, `log_db` config) that records runs, events, and tool calls mirroring `--json`, with metadata-only default, redaction, and retention.
- Emit a `provider.retry` event in `--json` output when a model request is retried.
- Add an opt-in `apply_patch` tool (`--apply-patch`, `apply_patch: true`) that applies multi-file, multi-hunk patches in OpenAI's `*** Begin Patch` format (add, update, rename, delete), checking every operation before any write.
- Add opt-in image support (`--images`, `images.enabled`, `max_image_bytes`): a `view_image` tool and MCP image pass-through for Anthropic and Codex, with a text placeholder for Chat Completions providers.
- Preserve Anthropic thinking blocks and Codex reasoning items across tool calls, and add a `reasoning_effort` config key and `--reasoning-effort` flag.
- Add a Responses API transport to the `openai` provider so tool calls work with reasoning on current OpenAI models, with encrypted reasoning replay and cached-token pricing. The new `openai_api` config key and `--openai-api` flag choose `auto` (default), `responses`, or `chat`.
- Add opt-in tool output files (`--tool-output-files`, `tool_output_files` config): tool results over `max_output_chars` are saved in full to a per-run file under `.tinyagent/tool-output`, and the model gets a bounded preview with a notice naming the path to page with `read_file` and `grep`. `tool.result` events, `post_tool` hook input, and trajectories record `output_file`; old runs are pruned by `tool_output_files.max_runs` (default 20).

- Add an opt-in `delegate` tool (`--subagents`, `subagents` config) that hands a task to a read-only subagent (`read_file`, `list_files`, `grep`) with a fresh context and returns only its final report plus a usage footer. Children are capped by `subagents.max_steps` (default 30), can use a different `subagents.model`, share the parent's cost and wall-time limits, run hooks with a `subagent` id, and are recorded as `subagent.started`/`subagent.finished` events and a `subagents` array in trajectories; `--resume` keeps the setting.

### Fixes

- Responses cut off at the output-token limit are no longer treated as finished: their tool calls are not run, the model is asked to continue in smaller pieces, and after `max_truncations` (default 3) in a row the run ends with the new `OutputTruncated` status (exit 3).
- The event log now redacts the values of environment variables that hooks forward by name (including the new stop hooks).

### Changed

- Configs that use the new `subagents` key fail to load on older tinyagent versions, which reject unknown keys.
- Configs that use the new `providers` key fail to load on older tinyagent versions, which reject unknown keys.
- The `openai` provider now uses the Responses API against `api.openai.com` (when `OPENAI_BASE_URL` is unset or points there). Set `openai_api: chat` to keep Chat Completions. Custom `OPENAI_BASE_URL` endpoints keep Chat Completions by default.

## v0.4.0 - 2026-10-24

This release adds controls and integrations for longer, more scriptable agent runs, along with stronger file and tool handling.

### Breaking Changes

- None.

### Features

- Resume validated unfinished runs from saved trajectory checkpoints.
- Compact long conversations automatically as they approach the model context limit.
- Add background process tools and optional live output streaming for long-running commands.
- Allow explicitly selected tools from configured local MCP servers.
- Add read-only mode and configurable default models.
- Emit opt-in JSONL run events for scripting and CI integrations.
- Add per-call bash timeouts and optional surrounding lines, literal matching, and case-insensitive search to `grep`.

### Fixes

- Reject binary files in `read_file` with a clear error.
- Reject invalid step and cost limits, and warn when a cost limit cannot be enforced for an unpriced model.

### Other

- Enforce Go formatting in CI and run tests without using the test cache.
