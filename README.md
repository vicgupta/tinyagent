# tinyagent

```bash
curl -fsSL https://raw.githubusercontent.com/vicgupta/tinyagent/main/install.sh | sh
```

A minimal coding agent in a single static Go binary. It gives an LLM nine tools (`read_file`, `write_file`, `edit_file`, `list_files`, `grep`, `bash`, and `start_process`, `read_process`, `stop_process` for background commands), plus the opt-in `update_plan`, `skill`, `view_image`, `apply_patch`, `delegate`, `web_fetch`, and `lsp` tools, and keeps a linear message history until the model submits.

## Install

### macOS and Linux

```bash
curl -fsSL https://raw.githubusercontent.com/vicgupta/tinyagent/main/install.sh | sh
```

The script downloads the latest release for your OS and CPU, checks it against `SHA256SUMS`, and installs it to `/usr/local/bin` if you can write there, otherwise to `~/.local/bin`. No `sudo` needed. Two optional settings:

```bash
# Install a specific version
curl -fsSL https://raw.githubusercontent.com/vicgupta/tinyagent/main/install.sh | TINYAGENT_VERSION=v0.1.1 sh

# Install somewhere else
curl -fsSL https://raw.githubusercontent.com/vicgupta/tinyagent/main/install.sh | TINYAGENT_INSTALL_DIR=~/bin sh
```

### Windows

Download `tinyagent-windows-amd64.exe` (or `-arm64.exe`) from the [latest release](https://github.com/vicgupta/tinyagent/releases/latest), rename it to `tinyagent.exe`, and put it in a folder on your `PATH`. The `bash` tool needs `bash` on your `PATH`, e.g. from Git for Windows.

### With Go

```bash
go install github.com/vicgupta/tinyagent/cmd/tinyagent@latest
```

### Manual download

Binaries for every platform, plus `SHA256SUMS`, are on the [releases page](https://github.com/vicgupta/tinyagent/releases). Verify with `shasum -a 256 -c SHA256SUMS --ignore-missing`. On macOS, a binary downloaded with a browser is quarantined; clear it once with `xattr -d com.apple.quarantine tinyagent`.

### Updating

```bash
tinyagent --update
```

This replaces the binary in place with the latest release, after checking its checksum. If it's installed in a directory you can't write to, run it with `sudo`.

### Shell completion (Bash)

`tinyagent --completion bash` prints a Bash script that completes flag names (`--sandbox-<Tab>`, `--tool-out<Tab>`, short aliases such as `-t`). Load it for the current session:

```bash
source <(tinyagent --completion bash)
```

To keep it, add that line to `~/.bashrc`. Only Bash is supported; other shell names exit 2. Words that don't start with `-` keep Bash's normal filename completion, and pressing Tab never runs tinyagent: the flag list is fixed when the script is generated, so re-source it after updating. The command needs no task, credentials or config.

### Build from source

```bash
git clone https://github.com/vicgupta/tinyagent && cd tinyagent
make build    # ./tinyagent
make test
make dist     # linux/darwin/windows × amd64/arm64 in dist/
```

## Usage

```bash
export OPENAI_API_KEY=...
tinyagent -t "fix the failing test in pkg/foo" --cwd ~/src/project -o run.json -v
tinyagent -m anthropic/claude-sonnet-4-5 -t "..."
tinyagent -m openrouter/qwen/qwen3-coder -t "..."
tinyagent -m ollama/llama3.1 -t "..."
tinyagent -m codex/gpt-5.6-luna -t "..."   # ChatGPT subscription, see below
cat task.md | tinyagent -t -               # read a long or multi-line task from stdin
tinyagent --read-only --cwd ~/src/project -t "Review error handling in pkg/api"
cat review-task.md | tinyagent --read-only -t - -o review.json
```

### ChatGPT subscription (Codex)

The `codex` provider uses your ChatGPT subscription instead of an API key. Sign in once with the [Codex CLI](https://github.com/openai/codex) (`codex login`, choose ChatGPT). tinyagent reads the tokens it saves in `$CODEX_HOME/auth.json` (default `~/.codex/auth.json`), refreshes them when they expire and writes them back, so Codex stays signed in too. Requests go to the Codex backend's Responses API. Usage counts against your plan's limits rather than costing money, so cost shows as $0 and `--cost-limit` has no effect. Only models your plan offers in Codex work (the ones the Codex CLI's model picker shows); others fail with "model is not supported".

## Flags

| Flag | Default | |
|---|---|---|
| `-h, --help` | | Print usage to stdout and exit |
| `-t, --task` | — | Task prompt (required unless `--resume`; with `--resume` of a finished run, the follow-up task); `-` reads it from stdin |
| `--resume` | — | Continue a validated unfinished trajectory; requires explicit matching `--cwd` and a new `-o` path |
| `-m, --model` | `$AGENT_MODEL`, else config `model`, else `openai/gpt-4o` | `<provider>/<model>`; no `/` means openai. Providers: openai, openrouter, anthropic, ollama, codex, plus any defined under [`providers`](#named-providers) |
| `--reasoning-effort` | config `reasoning_effort`, else unset | Reasoning effort sent to the provider: `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max`; see [Reasoning](#reasoning) |
| `--fallback-model <spec>` | config `fallback_models`, else none | Model to switch to when the current one keeps failing with `429`, `5xx` or network errors after retries; repeatable, tried in order, at most 3, and replaces `fallback_models`; see [Model fallback](#model-fallback) |
| `--no-fallback` | off | Don't fall back to other models (clears `fallback_models`); conflicts with `--fallback-model` |
| `--openai-api` | config `openai_api`, else `auto` | Transport for the `openai` provider: `auto`, `responses`, or `chat`; see [OpenAI transport](#openai-transport) |
| `--init-config` | | Write a commented starter config to the user config path (or `-c PATH`), refuse overwrites, and exit |
| `-c, --config` | user config, else built-in | YAML config file; see [Config](#config) |
| `--profile` | none | Apply one named profile from the config file's `profiles:` section over its base settings; see [Profiles](#profiles) |
| `--cwd` | `.` | Root directory; tools can't reach outside it |
| `--worktree[=name]` | off | Run in a new Git worktree at `<repo>/.tinyagent/worktrees/<name>` on branch `tinyagent/<name>`, created from `HEAD` of the repository containing `--cwd` and kept afterwards; the name is generated when omitted. Can't be combined with `--resume` or `--rewind`. See [Worktrees](#worktrees) |
| `--step-limit` | 0 (unlimited) | Max model calls |
| `--cost-limit` | 0 (unlimited) | Max USD |
| `--time-limit` | 0 (unlimited) | Max wall-clock seconds; overrides `wall_time_seconds` |
| `--max-format-errors` | config `max_format_errors`, else 3 | Stop with `RepeatedFormatError` after this many consecutive steps whose tool calls are all malformed; must be >= 1. See [Malformed tool calls](#malformed-tool-calls) |
| `--max-output-tokens` | config `max_output_tokens`, else 0 | Per-request output-token budget sent to the provider; 0 keeps the provider default (8192 `max_tokens` on Anthropic). Not supported by `codex`. Unrelated to `max_output_chars`, which caps tool output. See [Output truncation](#output-truncation) |
| `--no-compact` | off | Never compact the history; see [Long runs](#long-runs) |
| `--plan` | config `plan.enabled`, else off | Give the model the `update_plan` tool; see [Plan tool](#plan-tool) |
| `--apply-patch` | config `apply_patch`, else off | Give the model the `apply_patch` tool for multi-file, multi-hunk edits, checked before any write; `--apply-patch=false` disables it. See [apply_patch](#apply_patch) |
| `--subagents` | config `subagents.enabled`, else off | Give the model the `delegate` tool, which hands a task to a read-only subagent with a fresh context and returns only its report; `--subagents=false` disables it. See [Subagents](#subagents) |
| `--web-fetch` | config `web_fetch.enabled`, else off | Give the model the `web_fetch` tool, which reads pages from allowlisted hosts as text; needs a nonempty allowlist. `--web-fetch=false` disables it. See [Web fetch](#web-fetch) |
| `--lsp` | config `lsp.enabled`, else off | Start the language servers configured in `lsp.servers` (user config or `-c` only): append compiler diagnostics to `write_file`, `edit_file` and `apply_patch` results and add the read-only `lsp` tool. Needs at least one server; `--lsp=false` disables it. See [Language servers (LSP)](#language-servers-lsp) |
| `--web-fetch-allow-domain <host>` | config `web_fetch.allowed_domains`, else none | Hosts `web_fetch` may read. Repeatable; replaces the config list; requires `--web-fetch` (or `web_fetch.enabled: true`). See [Web fetch](#web-fetch) |
| `--tool-output-files` | config `tool_output_files.enabled`, else off | Save the full text of tool results over `max_output_chars` to a per-run file the model can page with `read_file` and `grep`; `--tool-output-files=false` disables it. See [Tool output files](#tool-output-files) |
| `--images` | config `images.enabled`, else off | Let vision-capable models see images: the `view_image` tool and MCP image pass-through; see [Images](#images) |
| `--plan-file` | — | Mirror the current plan to this Markdown file after each checkpoint save (requires the plan tool) |
| `--skills` | config `skills`, else off | Discover [Agent Skills](#agent-skills) and offer the `skill` tool |
| `--no-skills` | — | Disable skills even if the config enables them (`--no-skills=false` enables) |
| `--read-only` | config `read_only`, else off | Permit only `read_file`, `list_files`, and `grep`; blocks edits, shell, and process tools. `--read-only=false` explicitly disables it. |
| `--add-dir-read` | config `additional_dirs`, else none | Let the file tools read (not write) an existing directory outside `--cwd`; repeatable. See [Additional directories](#additional-directories) |
| `--add-dir-write` | config `additional_dirs`, else none | Let the file tools read and write an existing directory outside `--cwd`; repeatable. See [Additional directories](#additional-directories) |
| `--sandbox` | config `sandbox`, else off | Confine `bash`/`start_process` at the OS level: writes only under `--cwd`, a temp dir and `sandbox_writable_paths`, network off. See [Sandboxing commands](#sandboxing-commands) |
| `--sandbox-network` | config `sandbox_network`, else off | With `--sandbox`, allow network access |
| `--sandbox-allow-domain <host>` | config `sandbox_allowed_domains`, else none | With `--sandbox`, let commands reach only the listed hosts over HTTP(S) through a filtering proxy. Repeatable; replaces the config list. See [Network allowlist](#network-allowlist) |
| `--env` | config `environment`, else `local` | Where `bash`/`start_process` run: `local` (on the host) or `container` (Docker/Podman). See [Execution environments](#execution-environments) |
| `--command-env` | config `command_env.inherit`, else `all` | Environment `bash`/`start_process` inherit: `all`, `core` or `none`. See [Command environment](#command-environment) |
| `-o, --output` | — | Trajectory JSON, rewritten after every step |
| `--snapshots` | config `snapshots`, else off | Snapshot the workspace before every tool batch that can change files, so `--rewind` can restore it. Requires `-o`. See [Rewind workspace changes](#rewind-workspace-changes) |
| `--rewind <trajectory>` | | Restore `--cwd` to a snapshot recorded in that trajectory, with `--to start\|<step>\|<snapshot-id>` (default `start`) and `--dry-run` to preview. Makes no model request |
| `-v, --verbose` | off | Print tool calls and their outputs |
| `--json` | off | Write machine-readable run events as JSONL to stdout |
| `--output-schema` | | End the run with a JSON result validated against this JSON Schema file (printed to stdout) |
| `--log-db[=path]` | off | Also record run events in a local SQLite database (default `<cwd>/.tinyagent/tinyagent.db`); see [Event log database](#event-log-database) |
| `--no-stream` | off | Wait for each full response instead of streaming it |
| `--no-agents-md` | off | Don't append the nearest `AGENTS.md` (at or above `<cwd>`, up to the repo root; capped at 32KB) to the system prompt |
| `--scoped-agents-md` | off | Also deliver `AGENTS.md` files in directories below `<cwd>`, appended to the first file tool result that touches a path there; see [Project instructions](#project-instructions-agentsmd). Can't be combined with `--no-agents-md` |
| `-V, --version` | | Print the version and exit |
| `--update` | | Replace this binary with the latest GitHub release (checksum-verified) and exit |
| `--check` | | Print the resolved config file, model, provider key status, fallbacks, cwd and limits, then exit without a model request (exit 2 on a setup error) |
| `--completion bash` | | Print a Bash flag-name completion script and exit (`source <(tinyagent --completion bash)`); see [Shell completion](#shell-completion-bash) |

### Machine-readable output

`--json` makes stdout a UTF-8 JSONL event stream, with one complete JSON object and newline per event. It can be combined with `--no-stream`, `--read-only`, `--resume`, and `-o` (which still writes the trajectory JSON to its file). In this mode, streamed assistant text and live bash output are represented by events, and `--verbose` has no effect because tool calls and results are already events; the human summary and warnings remain on stderr. Setup failures before a run starts may leave stdout empty.

Every event has a `type`. Event names and fields in schema version 1 are:

| Type | Fields |
|---|---|
| `run.started` | `schema_version` (currently `1`), `model`, canonical `root`, `read_only`, `resumed`, `follow_up` (`true`, only for a follow-up run), `reasoning_effort` (only when set), `fallback_models` (only when a [fallback chain](#model-fallback) is configured), `openai_api` (the resolved `responses` or `chat`, only for the `openai` provider), `profile` (the name only; only with [`--profile`](#profiles)), `command_env` (`inherit`, `exclude_secrets`, sorted `passed` names; only with a [command environment](#command-environment) policy), `file_access` (`deny_read`, `deny_write`; only with [file access](#restricting-file-access) rules), `web_fetch` (`allowed_domains`; only with [web fetch](#web-fetch) on), `lsp` (`servers`, the configured names; only with [language servers](#language-servers-lsp) on), `scoped_agents_md_enabled` and `scoped_agents_md` (scoped files already in context, `[]` for a fresh run; only with [`--scoped-agents-md`](#project-instructions-agentsmd) on), `additional_dirs` (`path`, `mode`; only with [additional directories](#additional-directories)), `worktree` (`path`, `branch`, `base_commit`, `repo_root`; only with [`--worktree`](#worktrees)) |
| `assistant.message` | `text` (complete assistant message) |
| `tool.call` | `id`, `name`, `arguments` (decoded JSON value, or a string if malformed) |
| `tool.result` | `id`, `name`, `output` (same configured truncation as tool output), `is_error`; with [tool output files](#tool-output-files), a saved result also has `output_file` (path relative to `--cwd`) and `output_bytes` |
| `hook` | `event` (`pre_tool`, `post_tool`, or `stop`), `tool_call_id`, `tool_name`, `command`, `decision` (`allow`, `deny`, `feedback`, `continue`, or `error`), `duration_ms`; tool hooks are emitted between the call's `tool.call` and `tool.result`, and `stop` events have no tool fields |
| `plan.updated` | `step`, `version`, `items` (`id`, `text`, `status`), `note`, `diff` |
| `checkpoint.saved` | `step`, `path`, `pending` (tool calls without results yet) |
| `resume.interrupted_call` | `id`, `name` of a call that was in flight when the previous process stopped |
| `resume.process_lost` | `id`, `pid`, `cmd` of a background process from the previous invocation |
| `compaction` | `step`, `reason`, `stage`, `tokens_before`, `tokens_after`, `elided_messages`, `summarized_messages` |
| `workspace.snapshot` | `id`, `step` (0 = baseline), `kind` (`baseline`, `step`, `final`, or `pre-rewind`), `time`, `files`, `bytes`, `skipped_ignored`, `skipped_large`, `large_paths` (up to 20, when any), `store`, `duration_ms`; only with [snapshots](#rewind-workspace-changes) on. Paths and counts only, never file contents |
| `rewind.plan` / `rewind.result` | `--rewind --json` only: the target snapshot, each `{op, path}` change and counts; then `restored`, `not_restored`, `pre_rewind` and `error` (on a partial restore) |
| `warning` / `error` | `message` |
| `sandbox.network_denied` | `host`, `port`, `reason`; the sandbox allowlist proxy refused a request from a command (see [Network allowlist](#network-allowlist)) |
| `web_fetch` | `url`, `final_url`, `status`, `content_type`, `bytes`, `truncated`, `duration_ms`, and `denied_reason` (for a refused URL) or `error` (for a failed fetch); one per fetch attempt, emitted before its `tool.result` (see [Web fetch](#web-fetch)) |
| `lsp.server` | `name`, `state` (`started`, `unavailable`, `crashed` or `stopped`), `reason` (for `unavailable` and `crashed`); one per language-server state change (see [Language servers (LSP)](#language-servers-lsp)) |
| `instructions.loaded` | `path`, `scope_dir` (both relative to `--cwd`), `bytes` (file size), `truncated`, `tool`, `tool_call_id`, `step`; a scoped `AGENTS.md` was appended to that call's result, emitted before its `tool.result` (only with [`--scoped-agents-md`](#project-instructions-agentsmd)) |
| `instructions.skipped` | `path`, `scope_dir`, `reason` (`file_access`, `outside_root`, `modified`, `read_error` or `cap`), `tool`, `tool_call_id`, `step`, and `error` for `read_error`; a scoped `AGENTS.md` that applied was not delivered |
| `provider.retry` | `attempt` (the upcoming attempt number), `reason` (`http_<status>` or `network`), `backoff_ms`; only emitted when a model request is retried |
| `model.fallback` | `step`, `from`, `to`, `reason` (`http_<status>` or `network`), `attempts`; the run switched to the next [fallback model](#model-fallback) after `from` used up its retries |
| `result` | `status`, `submission`, cumulative `steps`, `structured_output` (only with `--output-schema` and status `Submitted`), `input_tokens`, `output_tokens`, `cache_read_tokens`, `cache_write_tokens`, `cost_usd`, invocation `duration_ms` |

When the run starts, the final event is `result`, including for model errors, interruptions, and limit stops. Exit codes are unchanged. `--resume` reports cumulative trajectory accounting; `duration_ms` covers only the current invocation. Event streams can contain prompts, code, and tool output, so handle them with the same care as trajectory files. Credentials and environment dumps are not included.

Consumers should check `schema_version` on `run.started`. Event names and field meanings are stable within a schema version; an incompatible change will increment the version. New optional fields and event types may be added without changing it. The JSONL stream is intended for scripting and CI integrations:

```sh
tinyagent --json -t "fix the failing test" --cwd . \
  | jq -c 'select(.type == "result") | {status, submission, cost_usd}'
```

tinyagent takes no positional arguments: quote the task and pass it with `-t`. A stray word such as `tinyagent "fix the tests"` or `tinyagent -t "..." typo --step-limit 5` is an error (exit 2), rather than silently dropping the flags after it.

The limit flags must be >= 0, and `--cost-limit` must also be finite. A typo such as `--step-limit -1` or `--cost-limit NaN` is an error (exit 2) instead of quietly running without the cap. An explicit 0 means unlimited and overrides the config file's limit.

Environment: `OPENAI_API_KEY`, `OPENAI_BASE_URL` (any OpenAI-compatible endpoint), `ANTHROPIC_API_KEY`, `ANTHROPIC_BASE_URL`, `OPENROUTER_API_KEY`, `OLLAMA_HOST`, `CODEX_HOME`.

### Structured output

`--output-schema schema.json` makes the run end with one JSON object that conforms to a JSON Schema, for scripts and CI:

```sh
tinyagent -t "review this diff" --output-schema review.schema.json > review.json
```

The schema file (at most 64 KiB) must be a JSON object with `"type": "object"` at the root and is checked before any provider call; a bad schema exits 2. The model is given a `submit_result` tool whose parameters are the schema, and the run finishes only when it calls that tool, alone in its message, with arguments that validate. Invalid arguments, mixing `submit_result` with other tool calls, the completion marker, or a turn with no tool call are answered with the validation errors or a reminder; after 3 consecutive invalid attempts the run ends with status `InvalidOutput`. Hooks never run for `submit_result`.

On success the status is `Submitted`, stdout contains only the compact JSON object and a newline, and the summary, streamed text, and `-v` output go to stderr. With `--json`, the object is the `structured_output` field of the `result` event instead. Any other outcome exits 1 with nothing on stdout. Trajectories (`-o`) record `structured_output`, `output_schema`, and `output_schema_digest`; `submission` holds the compact JSON string. Runs started with `--output-schema` cannot be resumed, and `--resume` with `--output-schema` exits 2.

Supported keywords (the same subset as MCP tool schemas): `type`, `properties`, `required`, `additionalProperties: false`, `items`, `enum`, `const`, `minimum`, `maximum`, `exclusiveMinimum`, `exclusiveMaximum`, `multipleOf`, `minLength`, `maxLength`, `pattern` (Go regexp syntax), `minItems`, `maxItems`, `uniqueItems`, `minProperties`, `maxProperties`. `format` is accepted but not enforced. Providers also see the schema, but tinyagent validates locally, so results never depend on provider-side strict modes.
### Event log database

`--log-db` (or `log_db.enabled: true` in config) records every event the `--json` stream would contain in a local SQLite file, whether or not `--json` is also on. It is off by default; nothing is created unless enabled. The database lives at `<cwd>/.tinyagent/tinyagent.db` unless `--log-db=path` or `log_db.path` says otherwise (a flag path is relative to the shell's directory, a config path to `--cwd`; `~/` is expanded). Add `.tinyagent/` to your `.gitignore`. The file is created with mode 0600, uses WAL mode with a busy timeout so concurrent runs can share it, and needs no cgo (pure-Go `modernc.org/sqlite`).

Tables: `runs` (one row per invocation: model, provider, cwd, status, steps, token and cost totals, duration, version), `events` (`run_id`, `seq`, `step`, `ts`, `type`, JSON `payload`), `tool_calls` (tool name, arguments, result summary, duration, `is_error`), and `schema_migrations`. Timestamps are Unix milliseconds.

```sh
sqlite3 .tinyagent/tinyagent.db \
  "SELECT tool, count(*), avg(duration_ms) FROM tool_calls GROUP BY tool"
```

At the default `level: metadata` no prompt, message, argument, or output bodies are stored: those fields are replaced with their lengths, and the task is stored only as a SHA-256 hash and length. `level: full` stores bodies, after redacting values of environment variables named `*_KEY`, `*_TOKEN`, `*_SECRET`, or `*_PASSWORD`, secrets set in MCP or hook `env`, and common token shapes; payloads over `max_payload_bytes` are truncated. Old runs are pruned when a run starts (`max_days`, `max_runs`; `0` keeps everything). Database errors never fail a run: tinyagent prints one warning and continues without the log. The trajectory JSON (`-o`) is unchanged and remains the resume format.

### Worktrees

`--worktree` runs a task in a fresh Git worktree, so it never touches your checkout and several runs can work on the same repository at once:

```sh
tinyagent --worktree=fix-login -t "fix the login redirect" --cwd ~/src/project -o run.json
tinyagent --worktree -t "add tests for pkg/api" --cwd ~/src/project/pkg/api
```

- **What it creates.** A worktree at `<repo>/.tinyagent/worktrees/<name>` on a new branch `tinyagent/<name>`, from the current `HEAD` commit of the repository containing `--cwd`. The run's root becomes the matching path inside the worktree, so `--cwd ~/src/project/pkg/api` runs in `.tinyagent/worktrees/<name>/pkg/api`. A `.gitignore` containing `*` keeps `.tinyagent/worktrees` out of `git status`. The location isn't configurable.
- **Names.** `--worktree=<name>` uses 1 to 64 letters, digits, `.`, `_` and `-`, not starting with `.` or `-`. Bare `--worktree` generates `run-YYYYMMDD-HHMMSS-<6 hex>`. An existing worktree directory or `tinyagent/<name>` branch is an error rather than being reused.
- **Uncommitted changes aren't carried over.** The worktree starts from the last commit; if your checkout has uncommitted or untracked changes, tinyagent warns once and continues. Commit first if the task needs them.
- **Setup output and errors.** Setup prints `worktree: <path> (branch tinyagent/<name> from <commit>)`, and the end-of-run summary prints `worktree kept: <path> (branch ...)`. `--cwd` outside a Git work tree, a repository with no commits, `git` missing from `PATH`, an invalid or taken name, a `--cwd` subdirectory missing at `HEAD`, or combining with `--resume`/`--rewind` exits 2 before any model request and leaves nothing behind. Ctrl-C during setup leaves either no worktree or a complete one.
- **Hooks.** Repository hooks (such as `post-checkout`) don't run while the worktree is created. Filters and Git LFS behave as you configured them.
- **Keeping and cleaning up.** The worktree and branch are kept after every run, successful or not; tinyagent never commits, merges or pushes. Review it with `git -C <path> diff`, then remove it with `git worktree remove <path>` and `git branch -D tinyagent/<name>`.
- **Sandbox and container.** Git metadata lives in the main repository's `.git` directory, outside the worktree. With `--sandbox` it isn't writable, so `git commit` inside the worktree fails unless you add that directory to `sandbox_writable_paths` (which also lets commands change hooks and config); with `--env container` it isn't mounted, so `git` commands in the container fail.
- **Resuming and rewinding.** Trajectories and `run.started` record `worktree`. To resume, follow up or rewind, pass the worktree path as `--cwd` (without `--worktree`).

### Resume an unfinished task

```sh
# Checkpoint before and after every tool call; this run stops at 20 cumulative model steps.
tinyagent -t "fix the failing test" --cwd ~/src/project --step-limit 20 -o run.json

# Inspect the workspace (for example, git diff) and rerun relevant checks.
# Continue the same task with a ceiling of 40 total steps.
tinyagent --resume run.json --cwd ~/src/project --step-limit 40 -o continued.json
```

When a run with `-o` stops at a step, cost or time limit, is interrupted, or hits `OutputTruncated`, the summary on stderr is followed by a ready-to-paste command with the resolved `--cwd`, the limit that was hit doubled (no limit flag after an interruption or truncation), and a new `-o` path (`-2`, then `-3`, ... before the extension):

```
StepLimitExceeded after 20 steps in 4m12s (48,213 input / 3,100 output tokens, $0.41)
resume: tinyagent --resume run.json --cwd /home/me/src/project --step-limit 40 -o run-2.json
```

It isn't printed with `--json`, without `-o`, or for `--output-schema` runs.

`--resume <trajectory.json>` is noninteractive and uses the saved task and model. Do not pass `--task` for an unfinished checkpoint (it is only for [follow-ups](#continue-a-finished-run-with-a-follow-up-task) of finished runs); an explicitly different `--model` is rejected. The saved model wins over `$AGENT_MODEL` and the config's default model. Credentials and provider endpoints come from the current normal setup, never the checkpoint. Paths to checkpoints are relative to the invoking shell's directory, independently of `--cwd`.

An explicit `--cwd` must resolve to the recorded canonical root (directory symlinks are resolved). For a [`--worktree`](#worktrees) run, that is the path inside the worktree. The effective system prompt must match the saved prompt, including `{{cwd}}`/`{{os}}` substitutions and the recorded AGENTS.md loading mode. Current config is used to reconstruct it without appending AGENTS.md twice. Changed instructions, a conflicting `--no-agents-md`, or changed tool definitions/compatibility require a fresh task. In particular, a task that edits its effective AGENTS.md cannot resume under changed instructions.

Only new-format, unfinished checkpoints are eligible: an in-progress save (including one left by a crash or `kill -9`) or a stop caused by a step/cost/time limit, interruption, or error. Successful `Submitted`/`Completed` runs are not resumed this way; continue them with a follow-up task instead (see below). Legacy logs, unsupported versions/state, and malformed or partial tool turns are refused before provider setup. Checkpoint loading is limited to regular files of at most **64 MiB**. Larger trajectories remain audit logs.

With `-o`, the checkpoint is written before tools run (recording the calls as `pending_calls`) and again after each tool result. If the process dies mid-turn, `--resume` gives every call without a result the error result `[interrupted: tool did not complete before the agent stopped; it may have partially run — verify state before retrying]` and emits `resume.interrupted_call`. **Interrupted calls are never re-executed automatically**: a half-run `bash` or edit may have had side effects, so the model decides what to inspect or retry. Background processes started with `start_process` are recorded with their PID; they do not survive into a new invocation, so resume emits `resume.process_lost` and tells the model which ones are gone.

Compacted runs resume too. The trajectory always keeps the full history; the model's compacted view is rebuilt from the last `compactions` record (its message map, elisions, and summary) plus the messages after it, so the resumed request is the same one the original process would have sent. `--no-compact` is no longer needed to keep a run resumable.

A distinct `-o`/`--output` is required, and any existing destination is refused, including symlink and hard-link aliases of the source. The input stays unchanged. The validated starting state is saved to the new destination before the first request; completed tool turns and termination then replace it using a private temporary file. Initial publication requires filesystem hard-link support to publish atomically without replacing another file. A save failure in resume mode stops further work, prints an error, and exits nonzero, preserving the last complete checkpoint. Ordinary fresh-run save failures retain their existing warning behavior. Each save writes a private temporary file, fsyncs it, renames it over the checkpoint, and fsyncs the directory (where the platform supports it), so a crash or power loss leaves either the previous or the new complete checkpoint, never a partial one.

Steps, cost, token counts, and cache totals remain cumulative. Step/cost limits are cumulative ceilings: if already exhausted, no model request is made; increase the ceiling explicitly (or set it to 0 for unlimited). Runtime limits, tool timeouts, output settings, and compaction settings use normal config/flag precedence. Wall time and the stderr elapsed duration restart for each invocation; stopped time is not charged. Normal streaming, summaries, and success/failure exit codes still apply. The next action is a new model query, which incurs ordinary context/token costs; loading a checkpoint never dispatches historical tool calls.

New JSON keeps the existing top-level trajectory fields and adds versioned metadata: `schema_version` (currently 2; version 1 files still load), `root`, `system_prompt`, `skip_agents_md`, `tools`, and `tool_signature`. Version 2 adds `pending_calls`, `processes`, `plan_enabled`, `plan`, and `plan_history`; runs with skills enabled also record `skills_enabled` and `skills` (name, scope, and path of each discovered skill). `resume` records the input path, previous status/submission, cumulative step, and continuation-message index, plus `follow_up: true` for a follow-up boundary; nested `previous` entries retain earlier resume boundaries. These audit paths are not read as configuration. Metadata excludes credentials and environment dumps. Checkpoints still contain prompts, code, and tool outputs: treat them as private, trusted local execution inputs. New checkpoint and temporary files use owner-only permissions (0600) where supported.

A checkpoint restores **conversation state**, not files (to restore files, record the run with `--snapshots` and use [`--rewind`](#rewind-workspace-changes)), Git state, shell variables, live processes, or external services. The model receives a continuation notice and stderr warns to inspect and revalidate the current workspace. Commands may have produced unrecorded side effects after the last save, especially during a hard crash in a tool batch. Historical process IDs cannot reattach to children from another invocation. There is no rollback or exactly-once execution guarantee; inspect current state before resuming.

### Rewind workspace changes

With `--snapshots` (or `snapshots: true` in the config), tinyagent records the workspace so you can put files back the way they were before the agent changed them. It is off by default and needs `-o`, because the trajectory file is the index of the snapshots.

```sh
tinyagent -t "refactor the parser" --cwd . --snapshots -o run.json
tinyagent --rewind run.json --cwd . --dry-run          # preview: M/A/D lines and a count
tinyagent --rewind run.json --cwd .                     # back to the start of the run
tinyagent --rewind run.json --cwd . --to 4              # the state before step 4's tool batch
```

**When snapshots are taken.** A `baseline` after setup and before the first model request; a `step` snapshot before each tool batch that can change files (`write_file`, `edit_file`, `apply_patch`, `bash`, `start_process`, `stop_process`, and any MCP tool; batches of only read-only tools take none); and a `final` one when the run ends, whatever its status. Each is listed in the trajectory's `snapshots` array (`snapshots_enabled` is `true`), emitted as a `workspace.snapshot` event in `--json` and `--log-db`, and printed as one line with `-v`. With `--read-only` nothing can change files, so no snapshots are taken and stderr says so once.

**What is in scope.** Regular files and symlinks (stored as links, not followed) under `--cwd`, with their mode bits. Left out, and never modified or deleted by a rewind:

- `.git/`, the `.tinyagent/` directory (the store and the event log), and the `-o` trajectory and its temp files;
- paths ignored by `.gitignore` files and `.git/info/exclude`, when `--cwd` is inside a Git repository (your global excludes file is not read);
- files larger than `snapshots.max_file_bytes` (default 2 MiB). The snapshot records count them and list up to 20; a rewind warns that they were "not restored".

**Rewind.** `--rewind <trajectory> --cwd <root>` needs `--cwd` given explicitly, resolving to the trajectory's root as for `--resume`. For a [`--worktree`](#worktrees) run, pass the worktree path. `--to` is `start` (the default: the baseline), a step number, or a snapshot id. A step number means the state before that step's tool batch; if that step has no snapshot (it only read files), the latest snapshot at or before it is used and the output says which. The plan prints one line per path, `M` (content or mode restored), `A` (created later, will be removed) and `D` (deleted later, will be recreated), then a count; `--dry-run` stops there. Otherwise tinyagent first takes a `pre-rewind` snapshot, records it in the trajectory file, and prints its id with the command that undoes the rewind (`--rewind run.json --cwd . --to <pre-rewind-id>`); then it restores each path and removes directories that only exist because of later changes. It makes no model request and needs no API key.

A rewind exits 2 before writing anything when `--cwd` is missing or doesn't match, `--to` names no snapshot, the trajectory has no snapshots, or the store is missing or corrupt. A rewind isn't atomic across files: if a write fails it stops, lists what was and wasn't restored and the `pre-rewind` id, and exits 1.

A rewind doesn't touch Git state (HEAD, the index, branches, stashes), so uncommitted changes you made before the run come back as they were. It restores files only: not processes, services, databases outside `--cwd`, or anything a command did elsewhere. With `--env container`, `--cwd` is mounted into the container, so files the container writes there are covered.

**Resume.** `--resume` keeps the source trajectory's snapshots in the new trajectory, so either file can be rewound, including to a step of the original run. Whether the resumed run takes new snapshots depends on this invocation's `--snapshots`/config.

**Storage.** Snapshots live in `<cwd>/.tinyagent/snapshots/`: file contents are stored once each by SHA-256, plus one small manifest per snapshot. Directories are created `0700` and files `0600`, and the store has its own `.gitignore` so it is never committed. It holds full copies of in-scope files, secrets included, so treat it like trajectory files. Nothing is pruned automatically; deleting `.tinyagent/snapshots` is safe and only makes old trajectories un-rewindable. Each snapshot walks the tree, which can take a moment on large trees; `duration_ms` in the event shows how long.

### Continue a finished run with a follow-up task

A run that finished successfully (`Submitted` or `Completed`) can be continued with a new instruction by passing `-t` with `--resume`. The model sees the whole previous conversation, so it keeps its earlier findings without re-reading the code:

```sh
# Stage 1: review and report.
tinyagent -t "review internal/agent for data races; report them" --cwd . -o review.json

# Stage 2: act on the findings in the same conversation.
tinyagent --resume review.json -t "fix the races you found" --cwd . -o fix.json

# Or read the follow-up from stdin.
cat next.md | tinyagent --resume fix.json -t - --cwd . -o next.json
```

- A finished checkpoint requires a non-empty follow-up; `--resume finished.json` without `-t` exits 2 and says so. An unfinished checkpoint still refuses `-t` (exit 2): continue it with plain `--resume`.
- Every other resume rule applies unchanged: the same model, root, system prompt, tools and loading mode, a distinct new `-o`, the unchanged source file, and every refusal before provider setup. Runs with `--output-schema` or MCP tools, runs continued by `stop` hooks, and `VerificationFailed` runs are not eligible.
- The request is the saved history (or the rebuilt compacted view) followed by one user message: a notice that the previous invocation finished and the workspace may have changed, then the follow-up text. No historical tool call is re-executed. The trajectory's top-level `task` stays the original task; the follow-up text is recorded in that message.
- Steps, cost, and tokens stay cumulative, so `--step-limit` and `--cost-limit` count the earlier runs too. If the ceiling is already used up, no request is made and the run ends `StepLimitExceeded` or `CostLimitExceeded`; raise the ceiling for the follow-up.
- Follow-ups chain: a follow-up's finished result can take another follow-up, and a follow-up stopped by a limit continues with plain `--resume`. Each boundary is recorded in `resume` with `follow_up: true`, and in `--json` `run.started` carries `resumed: true` and `follow_up: true`.
- Checkpoints with a follow-up boundary use the existing schema version; older tinyagent binaries refuse them with an unknown-field error rather than misreading them.

### Project instructions (AGENTS.md)

Unless `--no-agents-md` is set, tinyagent looks for `AGENTS.md` starting at `--cwd` and walking up through its parents, stopping at the repository root (the nearest directory containing `.git`). The nearest file wins, and only that one is added to the system prompt. Without a `.git` above `--cwd`, only `<cwd>/AGENTS.md` is checked. The lookup never goes above the repository root, and `-v` prints which file was loaded. This only changes the prompt: tools are still confined to `--cwd`.

#### Scoped AGENTS.md in subdirectories

Monorepos often keep instructions next to the code they cover, such as `services/api/AGENTS.md`. With `--scoped-agents-md` (or `scoped_agents_md: true`), tinyagent also delivers those files, lazily: the first time a successful built-in file tool call touches a path below `--cwd`, every `AGENTS.md` in a directory between `--cwd` (exclusive) and that path is appended to the call's result, outermost first. The startup file above stays in the system prompt and is never sent again. It is off by default; with it off, prompts, tool results, and checkpoints are unchanged.

```
[AGENTS.md for services/api/ (812 bytes): instructions for files under services/api/; they take precedence over broader project instructions there]
...file contents...
[end AGENTS.md for services/api/]
```

- **Triggers:** `read_file`, `write_file`, `edit_file`, `view_image`, `lsp`, every path of an `apply_patch` (including move targets), and the directory argument of `list_files` and `grep` (so `grep` from `--cwd` itself triggers nothing). `bash`, background processes, MCP tools, and failed or denied calls never trigger delivery.
- **Once per context:** a file is sent once while its block is still in the model's context. If [compaction](#long-runs) elides or summarizes that result, the file is sent again on the next access; it is not pinned. Subagents start with nothing delivered, and `--resume` doesn't resend files the checkpoint already has in context.
- **Limits:** each file is capped at 32 KiB (cut on a UTF-8 boundary, with `[AGENTS.md truncated]`), and a run delivers at most 64 KiB of distinct scoped files; past that, the result gets a one-line notice instead and stderr a warning. Blocks are added after `max_output_chars` truncation and hook feedback, so they are never cut off, and `post_tool` hooks see them.
- **Safety:** files denied by [`file_access`](#restricting-file-access) and symlinks resolving outside `--cwd` are skipped (`-v` prints a line). A file modified after the run started (by the model or anything else) is not delivered, so the model can't inject instructions into its own context. Empty, unreadable, or non-UTF-8 files are skipped with a warning; the run continues.
- **Records:** `-v` prints each loaded file, `--json` emits `instructions.loaded` and `instructions.skipped`, and `-o` trajectories record `scoped_agents_md_enabled`, `scoped_agents_md` (files in context) and `scoped_agents_md_loads`. `--resume` keeps the checkpoint's setting; a conflicting `--scoped-agents-md` exits 2. `--no-agents-md` turns scoped delivery off too, and combining the two flags exits 2.

**Trust:** like the startup file, a scoped `AGENTS.md` is prompt input that can steer the agent, so only enable this for repositories you trust. Configs with `scoped_agents_md` fail to load on older tinyagent versions, which reject unknown keys.

### Agent Skills

With `--skills` (or `skills: true`), tinyagent discovers [Agent Skills](https://agentskills.io): folders containing a `SKILL.md` with YAML frontmatter (`name` and `description`) and Markdown instructions, optionally with `scripts/`, `references/`, or `assets/`. Skills are off by default.

- **Locations:** `.agents/skills/<name>/SKILL.md` in `--cwd` and each parent up to the repository root (same walk as AGENTS.md), then `~/.agents/skills/<name>/SKILL.md`. Symlinked skill folders are followed. `.claude/skills` is not scanned; symlink those skills into `.agents/skills` to use them.
- **Precedence:** on a name clash the nearest project directory wins, and project skills beat user skills; each shadowed skill produces a warning naming both paths.
- **Validation:** a skill with no description or unparseable frontmatter is skipped with a warning. Name problems (wrong format, mismatch with its folder) only warn. A `SKILL.md` over 1 MiB is skipped.
- **Progressive disclosure:** only the names and descriptions go into the system prompt (a "Skills" section, capped at 8,000 characters: descriptions are shortened first, then skills are omitted with a warning). The `skill` tool loads one skill's instructions (frontmatter stripped) wrapped in `<skill_content name="...">`, with its directory and up to 100 bundled file paths. `{"name": ..., "file": "references/x.md"}` reads one bundled text file (1 MiB cap; no absolute paths, `..`, or symlinks that leave the skill folder; binaries refused). Results also honor `max_output_chars`. Loading a skill that is still in context returns a short note instead of a second copy.
- **Other tools are unchanged:** `read_file` and friends stay confined to `--cwd`, so user skills are reachable only through the `skill` tool. Run bundled scripts with `bash` as usual.
- **Read-only and hooks:** the `skill` tool only reads, so it is available with `--read-only`. `pre_tool`/`post_tool` hooks match it as `skill`.
- **Compaction:** loaded skill instructions are never elided by stage-1 compaction. When old turns are summarized, the latest instructions of each loaded skill are re-attached after the summary (up to 32 KiB combined; any skill left out is named so the model can reload it).
- **Resume:** the checkpoint records whether skills were enabled and which were found. `--resume` keeps that setting (passing a conflicting `--skills`/`--no-skills` is an error) and refuses to continue if the skills catalog changed. Checkpoints from before this feature resume with skills off.
- With no skills found, the prompt and tool list are exactly as without `--skills`. `-v` prints each loaded skill and its path; warnings go to stderr and, with `--json`, as `warning` events.

**Trust:** skill instructions are prompt input. A project skill from a repository you don't trust can steer the agent like a malicious AGENTS.md, and bundled scripts run with your permissions if the agent invokes them. Skill text sent to the model goes to your model provider like any other prompt, and the trajectory records loaded skill content.

## Config

Run `tinyagent --init-config` to create a commented starter config at the default path below, or `tinyagent --init-config -c PATH` to choose a path; it creates parent directories, writes with mode 0600, refuses existing files (exit 2), and makes no model request. It cannot be combined with `-t`, `--resume`, `--rewind`, or `--update`.

Without `-c`, tinyagent loads a per-user config file if one exists, so settings you always want don't need a flag:

| OS | Default path |
|---|---|
| Linux and other Unix | `$XDG_CONFIG_HOME/tinyagent/config.yaml` (usually `~/.config/tinyagent/config.yaml`) |
| macOS | `~/Library/Application Support/tinyagent/config.yaml` |
| Windows | `%AppData%\tinyagent\config.yaml` |

The lookup order is: `-c PATH` if given (a missing or unreadable file is an error, with no fallback); otherwise the user config above, if it exists; otherwise the built-in defaults. A user config that exists but can't be read or parsed is an error naming the file, not skipped.

Every key is optional, but an unrecognized key (a typo like `step_limt`) is an error naming the file and the key, rather than silently ignored. Command-line flags override the file.

Set `read_only: true` to make repository analysis the default. `--read-only` overrides the config value, including `--read-only=false`. In this mode, executor dispatch allows only `read_file`, `list_files`, and `grep`; writes, edits, shell commands, and background process tools are denied even if task text or repository instructions request them. Denials are returned to the model as tool errors. The restriction is model tool authority, not an OS sandbox (for that, see `--sandbox` under Sandboxing commands): an explicit `-o` trajectory destination can be written by the CLI, provider requests still send their context to the selected service, and the Codex provider may refresh its credential file, and the opt-in [`web_fetch`](#web-fetch) tool, which only reads allowlisted URLs, stays available when enabled, as does the read-only [`lsp`](#language-servers-lsp) tool (its servers are still started and write logs under `.tinyagent/lsp`). `--update` is an administrative action outside task-tool policy. Readable secrets (unless hidden with [`file_access`](#restricting-file-access) rules), symlinks that point outside `--cwd`, existing lexical path behavior, and AGENTS.md discovery retain their existing limitations; read-only mode does not make an untrusted repository safe.

`model` saves a default model, so `tinyagent -t "fix the failing test"` needs no `-m`. The model is chosen in this order: `-m`/`--model` if given (even `-m openai/gpt-4o`), then a nonempty `$AGENT_MODEL`, then a nonempty `model` in the config, then `openai/gpt-4o`.

```yaml
model: ollama/llama3.1    # same <provider>/<model> syntax as -m
system_prompt: |          # {{cwd}} and {{os}} are substituted
  You are a coding agent...
read_only: true            # expose only read_file, list_files, and grep
sandbox: false             # OS-confine bash/start_process; see Sandboxing commands
# sandbox_allowed_domains: [proxy.golang.org]  # with sandbox, the only hosts commands may reach
environment: local         # or container; see Execution environments
command_env:               # omitted by default (inherit everything); see Command environment
  inherit: core            # all, core or none
  exclude_secrets: true
file_access:               # omitted by default (no rules); see Restricting file access
  deny_read: [".env", "*.pem", "secrets/"]
  deny_write: [go.sum, "vendor/"]
additional_dirs:           # omitted by default (only --cwd); see Additional directories
  - {path: /home/me/src/shared-lib, mode: read}
step_limit: 50
cost_limit: 2.0
wall_time_seconds: 1800
max_format_errors: 3      # consecutive malformed-call steps before RepeatedFormatError (>= 1)
bash_timeout_seconds: 120
max_output_chars: 30000   # tool output is truncated, keeping head and tail
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
subagents:
  enabled: false          # offer the delegate tool (same as --subagents)
  model: ""               # child model, e.g. openai/gpt-5-mini; empty uses the parent model
  max_steps: 30           # step cap per delegated task; 0 means the default (30)
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
reasoning_effort: medium  # optional; see Reasoning; --reasoning-effort wins
fallback_models: []       # optional, at most 3; see Model fallback; --fallback-model replaces it
openai_api: auto          # openai provider transport: auto, responses, or chat; --openai-api wins
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
    env: [TEAM_DOCS_TOKEN]   # names only; tinyagent copies their current values to the child
    discovery_timeout_seconds: 15
    call_timeout_seconds: 60
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
profiles:                    # omitted by default; named overlays selected with --profile, see Profiles below
  review:
    model: openai/gpt-5-mini
    read_only: true
```

### Profiles

`profiles` holds named overlays in the same config file tinyagent already loads (the user config, or the `-c` file). `--profile <name>` applies one of them over the file's base settings for that run, so you can switch between setups such as a cheap read-only review, CI with a sandbox and stop hooks, or a local model with one flag. A profile only lists the settings that differ from the base, and it can set anything the file can, including config-only settings like `command_env`, `file_access`, `hooks`, `mcp_servers`, `lsp` and `providers`.

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
- **With `-c`**, profiles come only from that file; the user config isn't read, as usual for `-c`.
- **Recording**: the profile *name* (never its settings) is recorded as `profile` in the trajectory (`-o`) and in `run.started`, and `-v` prints one `config profile:` line. Without `--profile` neither has the field. `--check --profile <name>` checks and reports the merged configuration.
- **Resume**: `--resume` accepts `--profile` for the settings it re-reads from the current setup (credentials, providers, limits, fallbacks). The saved model still wins, and the original run's profile is not re-applied automatically; the resumed trajectory records the profile given to the resuming run. `--rewind` reads no config and rejects `--profile` (exit 2); `--update` and `--version` ignore it.

Configs with `profiles` fail to load on tinyagent versions without this feature, since older versions reject the unknown key.

### Named providers

`providers` defines extra endpoints you can select with `-m <id>/<model>`, such as an internal gateway, a self-hosted server or another OpenAI-compatible service, without setting `OPENAI_BASE_URL`. Everything after the first `/` is sent to the endpoint as the model name, so `-m gateway/vendor/model-x` sends `vendor/model-x`.

```yaml
providers:
  - id: gateway
    api: chat                  # chat (POST <base_url>/chat/completions), responses (POST <base_url>/responses), or anthropic (POST <base_url>/v1/messages)
    base_url: https://llm.internal.example.com/v1
    api_key_env: GATEWAY_API_KEY   # optional; sent as Authorization: Bearer (x-api-key for anthropic)
    headers: {X-Team: core}        # optional static, non-secret headers
    env_headers: {X-Tenant: GATEWAY_TENANT}   # optional headers whose values come from environment variables
    models:                        # optional per-model metadata
      vendor/model-x:
        input_price_per_mtok: 2
        output_price_per_mtok: 8
        cache_read_multiplier: 0.1 # optional; price of cached input relative to input
        context_window: 128000     # used for compaction
```

- `id` must be unique, contain no `/`, and not be a built-in provider (`openai`, `openrouter`, `anthropic`, `ollama`, `codex`); built-in names always mean the built-in provider. `api` must be `chat`, `responses` or `anthropic`, and `base_url` an absolute `http` or `https` URL without credentials. Prices must be non-negative and `context_window` positive. A config that breaks these rules is rejected before any request, naming the file and the `providers[i]` entry, with exit status 2. A misspelled key is rejected like any other unknown config key.
- Secrets stay in the environment. `api_key_env` and `env_headers` name variables; tinyagent reads their values at startup and exits 2, before any request, if one is unset or empty. Static `headers` may not carry credentials (`Authorization`, `x-api-key`, `api-key`). Key and header values never appear in stderr, `-v`, `--json`, the trajectory or the event log database, which also redacts them if they show up in tool output. Without `api_key_env`, no key is sent.
- Metadata matches the model name exactly. Top-level `input_price_per_mtok`, `output_price_per_mtok` and `context_window` still win over provider metadata. A model with no price behaves like an unknown built-in model: it costs $0 and, with a positive `--cost-limit`, prints the "no pricing available" warning. A `cache_read_multiplier` without prices refines the built-in price for that model name, if there is one.
- `-v` prints `provider: <id> (api <api>, base_url <url>)`. A plain `http` base URL to a host other than localhost or a loopback address prints a warning, because requests and any key travel unencrypted. `run.started` in `--json` and the trajectory record `provider_api` and `provider_base_url` (never headers or keys); built-in providers leave them out.
- `--resume` of a trajectory that used a named provider needs the same provider defined with the same `api` and `base_url`; otherwise it stops before any request and tells you what changed.
- `openai_api`, `--openai-api` and `OPENAI_BASE_URL` apply only to the built-in `openai` provider. Configs with `providers` fail to load on tinyagent versions without this feature, since older versions reject the unknown key.

### OpenAI transport

The `openai` provider talks to OpenAI over the Responses API (`POST /v1/responses`) or Chat Completions (`POST /v1/chat/completions`). `openai_api` (config) or `--openai-api` (flag, which wins) picks one:

| Value | Transport |
|---|---|
| `auto` (default) | Responses when `OPENAI_BASE_URL` is unset or is `https://api.openai.com/v1` (a trailing `/` is ignored); Chat Completions for any other base URL, so OpenAI-compatible servers keep working |
| `responses` | Always the Responses API, at `OPENAI_BASE_URL` if set |
| `chat` | Always Chat Completions, as before this setting existed |

Newer OpenAI models only support tool calls together with reasoning on the Responses API. On Responses, tinyagent sends `store: false` (nothing is kept server-side), a `prompt_cache_key` stable for the run, flat function tools, and `reasoning: {effort}` when an effort is set. Reasoning items that come back with `encrypted_content` are kept in the assistant message (and the trajectory) and sent back before that turn's tool calls, so the model keeps its reasoning across tool calls. Cached input tokens (`input_tokens_details.cached_tokens`) are billed at the cache-read price. Streaming, `--no-stream`, retries, compaction, `--output-schema`, and `--resume` work the same on either transport.

The setting only applies to the `openai` provider; `openrouter`, `ollama`, and the others ignore it. An unknown value exits 2 before any request, and `-v` prints the resolved transport. The resolved transport is recorded as `openai_api` in trajectories and in `run.started`, and `--resume` keeps it, since reasoning items only replay on the transport that produced them (a different `--openai-api` is refused, like `--model`); a checkpoint from before this setting resolves like a new run. If a Chat Completions request fails with a 400 that asks for the Responses API, tinyagent suggests `openai_api: responses`.

### Reasoning

Reasoning models return state that must be sent back during tool use. tinyagent keeps it for the run and replays it unchanged:

- `anthropic`: when a response contains `thinking` or `redacted_thinking` blocks (thinking is on by default on current Claude models), all of its content blocks, including signatures, are sent back complete and in their original order in the same assistant message. Streaming and `--no-stream` behave the same.
- `codex`: Responses API `reasoning` items that carry `encrypted_content` are sent back in `input` right before the function calls and text of the same response. Items without `encrypted_content` are dropped, because with `store: false` they cannot be referenced.

`reasoning_effort` (config) or `--reasoning-effort` (flag, which wins) chooses how much the model reasons. When it is unset, nothing is added to requests and the model's default applies. When set, it maps to:

| Provider | Request field |
|---|---|
| `codex` | `reasoning.effort` |
| `openai` | `reasoning.effort` on the Responses API; `reasoning_effort` on Chat Completions (see [OpenAI transport](#openai-transport)) |
| `openrouter` | `reasoning.effort` |
| `anthropic` | `output_config.effort` (thinking is not turned on explicitly) |
| `ollama` | not supported; exits 2 |

An unknown value exits 2 before any request. Which values a model accepts varies; a provider rejection is reported like any other model error and is not retried. The effort is recorded as `reasoning_effort` in trajectories and is kept on `--resume` (a different `--reasoning-effort` is refused, like `--model`). Higher effort means more output tokens, which providers bill and which count toward `--cost-limit`; on Anthropic, thinking also counts toward the request's `max_tokens` (8192 unless `--max-output-tokens` is set). Changing the effort invalidates provider prompt caches.

Reasoning state is stored in each assistant message's `extra` (`anthropic_content`, and `codex_reasoning` for Responses API reasoning items from both `codex` and `openai`) in trajectories and checkpoints, so `--resume` replays it. It may include thinking text and encrypted reasoning blobs; treat trajectory files as private. Reasoning state is not shown in the terminal or in `--json` events.

### Model fallback

A fallback chain lets a run survive a provider outage. It is off by default. List up to 3 models in `fallback_models` (config) or with repeated `--fallback-model` flags, which replace the config list; `--no-fallback` clears it:

```yaml
model: openai/gpt-5
fallback_models: [anthropic/claude-sonnet-4-5, ollama/qwen3-coder]
```

When a model request still fails after its retries with `429`, a `5xx` status or a network error, tinyagent switches to the next model in the list, resends the same conversation, and stays on that model for the rest of the invocation. Every other error (`400`, `401`, `403`, `404`, context overflow, a failure in the middle of a streamed response, an interrupted run) is reported as before and does not switch models. When the last model also fails this way, the run ends with `Error` and a message naming every model tried and why.

Each switch prints `model fallback: <from> -> <to> (<reason>) at step <n>` on stderr, emits a [`model.fallback`](#machine-readable-output) event, and is recorded in trajectories under `model_switches` (`step`, `from`, `to`, `reason`), along with `fallback_models` and `active_model` (the model serving the run when it was saved). `model` stays the primary model, and with a chain configured each assistant message records the model that produced it in `extra.model`. The final summary line notes the switch, for example `, 1 model fallback (now anthropic/claude-sonnet-4-5)`.

Every entry is checked before the first request: an unknown provider, a missing API key, an entry equal to the primary model, a duplicate, or more than 3 entries exits 2. `reasoning_effort` and `max_output_tokens` apply to a fallback only when it uses the same provider as the primary. Provider-specific reasoning state (Anthropic thinking blocks, encrypted Responses reasoning) is never sent to a different provider. Prices come from each model's own pricing, and `--cost-limit` warns about every unpriced model in the chain.

**Your conversation goes to the fallback provider.** After a switch, the whole history so far (task, files the model read, tool output) is sent to the next provider in the list, so only list providers you are willing to share that data with.

`--resume` uses the chain from the current config and flags, not the saved one, and the resumed run starts on the primary model. Subagents (`delegate`) don't fall back. Older tinyagent versions reject a config file containing the `fallback_models` key.

### Local MCP tools

MCP is disabled unless `mcp_servers` is explicitly configured. Each configured executable is trusted code with its own host and network permissions; this is tool preauthorization, not a sandbox or an interactive approval prompt. Configuration is read only from the user config or the explicitly selected `-c` file, never from the repository or model output. Tinyagent launches the command directly (no shell), with `--cwd` as its working directory and a minimal environment (`PATH`, plus the Windows process variables when present) and only the named forwarded variables. Missing forwarded variables fail before launch. Do not forward secrets unless the server needs them. Read-only runs never start MCP servers.

The Go MCP SDK v1.8.0 supports protocol revisions 2026-07-28, 2025-11-25, 2025-06-18, 2025-03-26, and 2024-11-05 over stdio. Tinyagent discovers the complete paginated catalog once per run, with a 15-second default discovery timeout, 100-page, 256-selected-tool, 64-KiB-per-schema, and 1-MiB-total-schema caps, and exposes only configured names. Tool names are sent to providers as `<alias>__<original-name>` and must fit provider-compatible naming rules. The accepted input schema subset supports object, array, string, number, integer, boolean and null types (a single type or an array such as `["string", "null"]`), nested properties/items, required, enum, const, numeric bounds and `multipleOf`, length, `pattern`, `format`, item and property counts, `uniqueItems`, annotations (`title`, `description`, `default`, `examples`), and boolean `additionalProperties`. A top-level `$schema` declaration is dropped. Other keywords, including `anyOf`/`oneOf`/`allOf` and references, are rejected, and a selected tool with such a schema stops startup. Each call defaults to a 60-second timeout; a timed-out call returns an error to the model and the server stays available, while any other call failure disables that server for the rest of the run. Text content is joined with newlines, structured output is appended as JSON, unsupported content is marked as omitted, tool errors remain visible, and model-visible output is capped at 256 KiB before the normal output cap is applied. Stdio protocol data is kept separate from server stderr.

MCP tool calls and results are stored in trajectories along with the external tool names. Trajectories from MCP-enabled runs cannot be resumed. Resuming a built-in-only checkpoint works with `mcp_servers` configured; the servers are not started and the run keeps the built-in tools. Trajectories and server stderr may contain sensitive data supplied by tools or the server; no blanket redaction is provided. On shutdown tinyagent closes MCP sessions and asks their processes to exit, then the SDK applies its bounded termination. Unix process descendants and Windows child-process cleanup have platform-specific behavior; tinyagent does not promise process-tree cleanup on Windows.

### Tool hooks

Hooks are disabled unless `hooks` is configured. A `pre_tool` hook runs before a matching tool call and can deny it with a reason the model sees; a `post_tool` hook runs after it and can append feedback, such as formatter or linter output, to the result the model sees. Like MCP servers, hooks are trusted local executables running with your privileges: they are read only from the user config or the `-c` file, never from the repository, launched directly (no shell) with an absolute `command`, `--cwd` as the working directory, and the same minimal environment (`PATH`, the Windows process variables when present, and only the named `env` variables, which must be set at startup). Provider API keys are not passed unless named. Hooks are a guardrail and automation point, not a sandbox: they see a call's parsed arguments, not what a shell command actually does.

Each hook gets one JSON object on stdin: `schema_version` (`1`), `event` (`pre_tool` or `post_tool`), `tool_name`, `tool_call_id`, `arguments` (decoded JSON, or the raw string if malformed), canonical `root`, `read_only`, and `step`; `post_tool` also gets `output` (the model-visible result after normal truncation) and `is_error`, plus `output_file` when [tool output files](#tool-output-files) saved the full result. The input can contain secrets from tool arguments and output.

| Event | Exit 0 | Exit 2 | Other exit, failure to start, timeout, or invalid stdout JSON |
|---|---|---|---|
| `pre_tool` | Allow, unless stdout is `{"decision":"deny","reason":"..."}` | Deny; the reason is stderr | Apply `on_error`: deny by default (the reason names the hook and the failure), or allow with a warning |
| `post_tool` | Optional stdout `{"feedback":"..."}` is appended to the result | stderr is appended to the result | Warning on stderr; the result is unchanged |

A denied call is not executed; the model gets the error result `error: denied by pre_tool hook <command>: <reason>` and the run continues. Matching hooks run one at a time in config order: the first `pre_tool` deny stops the rest, and every matching `post_tool` hook runs, each appending its feedback under `[post_tool hook <command>]` (capped at 8 KiB per hook; the combined result still honors `max_output_chars`). Read-only denials and unknown tools are rejected before hooks, so hooks never run for them and cannot widen authority; with `--read-only`, hooks still run for `read_file`, `list_files`, and `grep`. MCP tools are matched by their exposed `<alias>__<tool>` name. Ctrl-C or the wall-time limit kills a running hook (its process group on Unix; only the process on Windows). `-v` prints one stderr line per hook run, `--json` emits a `hook` event, and trajectories record the model-visible results, including denial reasons and feedback. Resumed runs use the hooks in the current config. Hooks do not run for `update_plan`, which only changes the run's plan.

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

## Long runs

tinyagent resends the whole history on every step, so a long run can outgrow the model's context window. Before each request it estimates the request's size (the previous request's reported input tokens plus about 4 characters per token for anything added since). Above 80% of the window, it compacts:

1. Old tool outputs are replaced with a placeholder such as `[output elided: 12,400 chars]`. The system prompt, the task, and the most recent turns (about 25% of the window, and never fewer than the last 2 assistant turns with their tool results) are kept verbatim. If that brings the estimate under 50% of the window, it stops here.
2. Otherwise, one extra request asks the same model, with no tools, to summarize everything older than the recent turns. The model then sees the system prompt, the task, that summary, and the recent turns.
3. If it still doesn't fit, the run ends as `Error` with `context still exceeds the window after compaction`.

If a provider rejects a request as too long (`prompt is too long`, `context_length_exceeded`, `maximum context length`), tinyagent compacts and retries that step once; a second rejection ends the run as `Error`. This works even for models whose window isn't known.

The window comes from a small built-in table (`internal/model/context.go`), a named provider's per-model `context_window` (see [Named providers](#named-providers)), or top-level `context_window` in the config, which wins. Without either, only the retry-on-error path runs. Each compaction prints one line to stderr, such as `compacted context: ~182,000 → ~31,000 tokens (summarized 41 messages)`. The summary request's tokens and cost count toward the run's totals and `--cost-limit`, but not `--step-limit`. With `-o`, the trajectory keeps every original message, and `compactions` records each one: the step, the estimates before and after, the stage, the summary, and which messages the model was sent next.

Summaries lose detail. Put instructions that must hold for the whole run in `AGENTS.md`, which is part of the system prompt and never compacted. Scoped `AGENTS.md` files from [`--scoped-agents-md`](#scoped-agentsmd-in-subdirectories) live in tool results instead, so they can be compacted away; they are sent again the next time the agent touches their directory. `--no-compact` turns all of this off.

### Plan tool

`--plan` (or `plan: {enabled: true}`) adds an `update_plan` tool, off by default. The model sends the whole list each time: up to 50 items, each `{id, text, status}` with status `pending`, `in_progress`, `done`, or `blocked`, at most one `in_progress`, plus an optional `note`. The plan lives in the trajectory (`plan`, with every change in `plan_history`), not in the message history: before each request a `[Current plan]` block is pinned after the system prompt, so it survives compaction (the summarizer also sees it) and `--resume`. Each update emits `plan.updated`, and `--plan-file plan.md` mirrors the current plan as a Markdown checklist after each save (the checkpoint stays the source of truth). The tool is available with `--read-only`, since it touches nothing in the workspace.

A checkpoint records whether the plan tool was enabled, and `--resume` keeps that setting: passing `--plan` to resume a run that started without it is an error, and the config's `plan.enabled` is ignored on resume. `--plan-file` requires the plan tool.

### Images

`--images` (or `images: {enabled: true}`) lets vision-capable models see images. It is off by default; with it off, requests, tools, and checkpoints are unchanged.

- **`view_image`** takes a `path` inside `--cwd` and sends the file to the model. PNG, JPEG, GIF, and WebP are supported, detected from the file's bytes (not its extension); SVG, other formats, directories, paths outside the workspace, and files larger than `max_image_bytes` (default 5 MiB) are errors. It is available with `--read-only`, since it only reads.
- **MCP image pass-through**: image content in an allowed MCP tool's result is sent to the model (up to 8 images per result, each within `max_image_bytes`). Unsupported, oversize, or extra images are replaced by an `[omitted MCP image: ...]` marker. With images off, MCP images are still `[omitted unsupported MCP content]`.

Per provider: `anthropic` sends images as image blocks inside the tool result, and `codex` as `input_image` items in the function call output. The Chat Completions API (`openai`, `openrouter`, `ollama`) cannot carry images in tool results, so those providers get a text note saying the image was not shown; tinyagent does not inject images as a separate user message. (A Responses API transport for `openai`, #80, would allow it.)

Image data never appears in text channels: the model's tool-result text, `--json` events, verbose output, hook input, and the event log see a placeholder like `[image: image/png, 48,213 bytes, sha256:1a2b3c4d5e6f]`. Privacy: the image bytes are sent to the model provider and stored base64 in the trajectory/checkpoint file, so treat those files like the images themselves. For compaction each image counts as about 1,600 tokens, and old images are elided (`[image elided: <path>, N bytes]`) like old tool output; the summarizer never sees image data.

A checkpoint records whether images were enabled, and `--resume` keeps that setting: passing `--images` to resume a run that started without it (or the reverse) is an error, and the config's `images.enabled` is ignored on resume.

### Tool output files

Tool results longer than `max_output_chars` are cut to a head and tail with a `... [N characters elided] ...` marker, and the middle is lost. `--tool-output-files` (or `tool_output_files.enabled: true`) keeps it: the full result text is saved to a file and the model-visible result becomes a shorter head/tail preview ending in one notice, for example:

```
[full output: 184,213 bytes, 2,941 lines, saved to .tinyagent/tool-output/20261025T140307.123Z-a1b2c3/call_abc.txt; use read_file with offset/limit or grep on that path]
```

The preview plus the notice never exceeds `max_output_chars`, and it is cut on UTF-8 boundaries. The model can then `read_file` the path with `offset`/`limit` or `grep` it. It is off by default; with it off, results, events, and checkpoints are unchanged and no directory is created.

- **What is saved:** the exact full text the tool returned (for `bash`, including the `<returncode>`/`<output>` wrapper, so the command's line N is line N+2 of the file), for built-in tools, allowed MCP tools (whose results are already capped at 256 KiB), and error results. Results within the limit are never saved. `read_file` results are not copied, since the file is already on disk: a truncated `read_file` result ends with `[file has N lines; output was truncated, use read_file on <path> with offset/limit to read the rest]` instead. `grep` and `list_files` calls on the output directory itself are not saved again.
- **Where:** one directory per run, `<dir>/<run id>/<tool call id>.txt`, where `dir` is `tool_output_files.dir` (default `.tinyagent/tool-output`, relative to `--cwd`) and the run id is a UTC timestamp plus a random suffix (`20261025T140307.123Z-a1b2c3`). `dir` must be inside `--cwd` and not `--cwd` itself; otherwise startup fails before any model request with an error naming `tool_output_files.dir`. The run directory is created (mode 0700) on the first save, and each file is written atomically with mode 0600. Files are written in `--read-only` mode too, since they are tinyagent's own output, not a workspace edit.
- **Exclusions:** recursive `grep` and `list_files` walks skip the output directory, so saved copies never show up as duplicate matches; a `grep` or `list_files` whose `path` is inside the directory searches it normally. `bash` commands are not filtered.
- **Retention:** at run start, the oldest run directories are deleted so at most `tool_output_files.max_runs` (default 20; 0 keeps all) earlier runs remain. Only directories named like run ids are touched.
- **Errors:** if a file can't be written (for example, `.tinyagent` is not a writable directory), the result falls back to the normal truncation, stderr gets one warning for the run (also a `warning` event with `--json`), and the run continues.
- **Recording:** with `--json`, a saved result's `tool.result` event adds `output_file` and `output_bytes`; `post_tool` hooks get `output_file`; with `-o`, the trajectory lists `tool_output_files` entries (`tool_call_id`, `output_file`, `output_bytes`). The event log stores the path and size as metadata. The setting is not pinned in checkpoints: `--resume` can turn it on or off.

Privacy: saved files can contain anything a command printed, including secrets, just like trajectories and the event log. Add `.tinyagent/` to your `.gitignore`.

### apply_patch

`--apply-patch` (or `apply_patch: true`) adds an `apply_patch` tool that applies one patch in the `*** Begin Patch` ("V4A") format [documented by OpenAI](https://developers.openai.com/api/docs/guides/tools-apply-patch). It is aimed at models trained on that format (the OpenAI GPT-4.1 and GPT-5 families, including `codex/*`) and works with any model that follows the tool description. It is off by default; with it off, requests, tools, and checkpoints are unchanged. `edit_file` and `write_file` stay available, and the tool is never offered with `--read-only` (a call to it gets the read-only denial).

The tool takes one string argument, `input`:

```
*** Begin Patch
*** Add File: docs/notes.txt
+first line
*** Update File: src/app.go
*** Move to: src/main.go
@@ func main() {
 	fmt.Println("hi")
-	return
+	os.Exit(0)
 }
*** Delete File: old.txt
*** End Patch
```

- `*** Add File:` is followed by the file's lines, each prefixed with `+`. `*** Delete File:` takes no body. `*** Update File:` is optionally followed by `*** Move to:` (a rename), then hunks of context (` `), removed (`-`), and added (`+`) lines.
- A hunk starts with `@@` or `@@ <line>`: the search for the hunk moves past the first line matching `<line>` (after the previous hunk), and several `@@` lines may be stacked to narrow it down. `*** End of File` marks a hunk at the end of the file. There are no line numbers.
- Context is matched exactly, then ignoring trailing whitespace, then ignoring surrounding whitespace; a looser match is noted in the result. Context lines keep the file's own text.
- Paths follow the same rules as the other file tools: relative to `--cwd`, or absolute inside it; anything outside is rejected. A shell heredoc wrapper (`apply_patch <<"EOF" ... EOF`) is stripped.

The whole patch is parsed and checked before anything is written: update and delete targets must exist, be regular files, and not be binary; add targets and move destinations must not exist; a path may appear in only one operation; every hunk must match; and the patch must be at most 1 MiB. If any check fails, nothing is written and the error names the operation (by number and path), the reason, and for context failures the lines that were not found. Otherwise the operations are applied in order and the result is `ok` followed by one line per operation: `A <path>`, `M <path> (<n> hunks)`, `D <path>`, or `R <old> -> <new>`. Updated files keep their permission bits, their line endings (CRLF or LF), and a missing trailing newline (a hunk marked `*** End of File` leaves the file ending in a newline); new files are written 0644 with parent directories created.

Writes are not transactional: if writing fails part-way (for example, a permission error), work stops and the error lists the operations that were applied, which are not rolled back, and those that were not. Like `edit_file`, the tool is not covered by `--sandbox`. A missing or non-string `input` is a [malformed tool call](#malformed-tool-calls); a patch that does not apply is an ordinary tool error.

A checkpoint records whether `apply_patch` was enabled (`apply_patch_enabled`), and `--resume` keeps that setting: passing a conflicting `--apply-patch` exits 2, and the config's `apply_patch` is ignored on resume.

### Subagents

`--subagents` (or `subagents.enabled: true`) adds a `delegate` tool. A call such as `{"task": "find every caller of LoadConfig and say which ones ignore its error", "label": "callers"}` starts a child agent with a fresh context: the same system prompt and `AGENTS.md`, plus a notice that it is a subagent that must answer first and then give evidence with `file:line` references. The child runs to completion, and the parent gets back only its final report followed by a footer such as `[delegate d1: Submitted, 4 steps, 9200 in / 410 out tokens, $0.0123]`. It is off by default; with it off, requests, tools, and checkpoints are unchanged.

- **Read-only.** The child has only `read_file`, `list_files`, and `grep`, whatever the parent has. It gets no `write_file`, `edit_file`, `apply_patch`, `bash`, process tools, MCP tools, `skill`, `view_image`, `update_plan`, or `delegate`, and calls to them are refused. Paths follow the parent's rules (`--cwd` and `file_access`). The tool is also offered with `--read-only`.
- **Limits.** Each child stops after `subagents.max_steps` model steps (default 30); hitting the cap, or any other unfinished status, returns a tool error such as `error: delegate d1 did not finish: StepLimit` rather than a report. Child steps don't count toward `--step-limit`; they're reported separately as `subagent_steps`. Child tokens and cost do count toward the run's totals and `--cost-limit`, and the child only gets what's left of the budget, so a delegated task can end the whole run as `CostLimit`. `--time-limit` covers children too. Reports longer than `max_output_chars` are truncated like any tool output. Several `delegate` calls in one turn run one after another.
- **Model.** `subagents.model` picks a different (typically cheaper) model; empty uses the parent's. `reasoning_effort` and `max_output_tokens` carry over only when the child uses the same provider as the parent. An unknown provider, or a missing key for a different provider, exits 2 before any request.
- **Tokens.** Delegation saves the parent's context, not tokens overall: the child resends its own history each step, so a task the parent could answer with one `grep` is cheaper done directly. Use it for searches that would otherwise pull many large files into the parent's history.
- **Observability.** `--json` and `--log-db` add `subagent.started` (`id`, `label`, `model`, `tool_call_id`, `parent_step`) and `subagent.finished` (`id`, `status`, `steps`, tokens, `cost_usd`) events, and the child's own events carry `"subagent": "d1"`. `-v` prints a line when each child starts and finishes; the child's model output is not streamed. Hooks run for child tool calls with `subagent` set in their input, and `pre_tool`/`post_tool` hooks also see the `delegate` call itself; stop hooks run only for the parent. With `-o`, the trajectory records `subagents_enabled`, `subagent_steps`, and a `subagents` array with each child's task, status, usage, report, and full message history.
- **Resume.** A checkpoint records whether subagents were enabled, and `--resume` keeps that setting: a conflicting `--subagents` exits 2. A run interrupted mid-delegation resumes with that `delegate` call marked interrupted; the child is not resumed.

Not in this version: children that can write or run commands, parallel children, nested delegation, and streaming the child's output.

### Web fetch

`--web-fetch` (or `web_fetch.enabled: true`) adds a `web_fetch` tool that reads one URL and returns it as text, for documentation, changelogs and API references. It is off by default; with it off, requests, tools, and checkpoints are unchanged. It is separate from command networking: it runs inside tinyagent, not through `bash`, so enabling it gives commands no network access, and `--sandbox`, `--sandbox-network` and `sandbox_allowed_domains` neither restrict nor widen it.

```yaml
web_fetch:
  enabled: true
  allowed_domains: [pkg.go.dev, "*.python.org"]
  max_bytes: 2097152      # 2 MiB
  timeout_seconds: 20     # 1 to 120
```

- **Allowlist.** `allowed_domains` (or `--web-fetch-allow-domain <host>`, repeatable, which replaces the config list) uses the same syntax as [`sandbox_allowed_domains`](#network-allowlist): `example.com`, `*.example.com` (subdomains only), and an optional `:port` (without one, ports 80 and 443). A bare `*`, an empty list while enabled, `timeout_seconds` outside 1 to 120, or `max_bytes` below 1 exits 2 before any model request, as does `--web-fetch-allow-domain` without enabling the tool. On start, one line such as `web_fetch: 2 domains (pkg.go.dev, *.python.org)` goes to stderr.
- **What it fetches.** `http` and `https` URLs only, with no user name or password in them, by `GET` with `User-Agent: tinyagent/<version>` and no cookies or credentials. The host is checked before any connection; loopback, private, link-local (including the `169.254.169.254` metadata address) and other non-public addresses are refused unless that literal address is listed, and the connection goes to the address that was checked. Up to 5 redirects are followed, each checked the same way. `HTTP_PROXY` and similar variables are ignored.
- **What it returns.** A header line such as `[web_fetch https://pkg.go.dev/fmt → 200 text/html, 81,234 bytes, 412 lines]` (noting truncation and a redirected final URL), then the body: HTML is converted to readable text (headings, lists, code blocks and `[text](url)` links kept; scripts, styles and navigation dropped), and plain text, Markdown, JSON and XML are returned as is. Other content types, non-2xx statuses, timeouts and refused URLs are tool errors. At most `max_bytes` of the response is read, and the result is then subject to `max_output_chars` and [tool output files](#tool-output-files) like any other tool output. Page content is untrusted input to the model.
- **Where it is offered.** It only reads, so it is offered with `--read-only`. [Subagents](#subagents) never get it. `pre_tool`/`post_tool` [hooks](#tool-hooks) match it as `web_fetch` and see `arguments` as `{"url": "..."}`; a denied call makes no request.
- **Observability.** `-v` prints one line per fetch (URL, status, type, bytes, time, or the refusal reason). `--json` and `--log-db` emit a `web_fetch` event per attempt and list `allowed_domains` in `run.started`; `-o` trajectories record `web_fetch_enabled` and `web_fetch.allowed_domains`.
- **Resume.** A checkpoint records whether `web_fetch` was enabled, and `--resume` keeps that setting: passing a conflicting `--web-fetch` exits 2, and the config's `web_fetch.enabled` is ignored on resume. The allowlist and limits come from the current invocation.

### Language servers (LSP)

`--lsp` (or `lsp.enabled: true`) starts language servers you configure, so the model sees compiler errors right after it edits a file instead of on its next build, and can jump to definitions and references. It is off by default; with it off, requests, tools, and checkpoints are unchanged and no server starts.

```yaml
lsp:
  enabled: true
  diagnostics_after_edit: true  # default
  diagnostics_wait_ms: 3000     # 100 to 30000
  max_diagnostics: 20           # per file, 1 to 200
  severity: error               # or warning
  servers:
    - name: go                  # letters, digits, - and _
      command: gopls
      extensions: [.go]
      env: [HOME, GOPATH, GOFLAGS]   # host variables to forward; each must be set
    - name: ts
      command: typescript-language-server
      args: [--stdio]
      extensions: [.ts, .tsx]
      initialization_options: {}
      startup_timeout_seconds: 15    # start and initialize, 1 to 300
```

- **Trust.** Servers are executables you choose, like [MCP servers](#local-mcp-tools): they are read only from the user config or `-c`, never from a repository file, and launched directly (no shell) in `--cwd` with a minimal environment: `PATH` (on Windows also `SystemRoot`, `WINDIR`, `USERPROFILE`, `TEMP`, `TMP`) plus the variables named in `env`. Many servers need more (gopls wants `HOME` or `GOPATH`/`GOCACHE`); forward them by name. A named variable that is unset, an empty `servers` list while enabled, a duplicate name or extension, or a value out of range exits 2 before any model request. Servers run on the host, so `--lsp` with `--sandbox` or `--env container` exits 2.
- **Lifecycle.** A server starts the first time a file with one of its extensions is edited or queried, one process per server per run. A server that can't start (missing binary, failed `initialize`, timeout) is marked unavailable for the run with one stderr warning; the run continues. A server that crashes is restarted once, then marked unavailable. On every exit (submit, limits, errors, interrupt) tinyagent sends `shutdown`/`exit` and then kills the server's process group. Each server's stderr goes to a log under `.tinyagent/lsp` in `--cwd` (1 MiB per server, the newest 50 kept).
- **Diagnostics after edits.** After a successful `write_file`, `edit_file` or `apply_patch` (with `diagnostics_after_edit`), tinyagent syncs each changed file with its server and waits up to `diagnostics_wait_ms` for diagnostics, then appends them to the tool result:

  ```text
  ok
  [lsp go: 2 errors in internal/a.go]
  internal/a.go:12:5: undefined: fooBar
  internal/a.go:30:2: missing return
  ```

  Positions are 1-based lines and columns (columns in characters). Errors only by default; `severity: warning` adds warnings (prefixed `warning:`). Beyond `max_diagnostics` a line such as `[lsp go: omitted 7 more diagnostics in a.go]` follows, and if the server is slow, `[lsp go: diagnostics not ready after 3000 ms for a.go]`. A clean file adds nothing. Notes are appended before `post_tool` [hooks](#tool-hooks) run, so hooks, `tool.result` events, [tool output files](#tool-output-files) and trajectories all see them. They report every current error in the file, not only new ones, and a server that hasn't finished indexing may report false positives; treat them as hints and confirm with a build.
- **The `lsp` tool.** `{"operation": "diagnostics" | "definition" | "references", "path": "...", "line": n, "column": n}` (1-based; `line` and `column` only for `definition` and `references`). `diagnostics` returns the file's current diagnostics, or `[lsp go: no errors in a.go]`. `definition` and `references` return up to 100 `path:line:col: <source line>` locations, sorted and deduplicated; locations outside `--cwd` and its [additional directories](#additional-directories), or denied by [file access](#restricting-file-access) rules, are left out and counted. `path` follows the same rules as `read_file`, and a file with no configured server is a tool error. The tool only reads, so it is offered with `--read-only` (where edits, and so post-edit diagnostics, can't happen); [subagents](#subagents) never get it. Hooks match it as `lsp`.
- **Privacy and cost.** Servers read the files you edit or query (and whatever else they index under `--cwd`); some servers download modules or toolchains or use a lot of memory on large repositories. Files over 4 MiB are not sent.
- **Observability.** `--json` and `--log-db` emit an `lsp.server` event per state change and list `servers` under `lsp` in `run.started`; `-v` prints `lsp: go started` and similar lines; on start, one line such as `lsp: go (.go); ts (.ts, .tsx)` goes to stderr. `-o` trajectories record `lsp_enabled` and `lsp_servers`.
- **Resume.** A checkpoint records whether `lsp` was enabled, and `--resume` keeps that setting: passing a conflicting `--lsp` (including `--lsp=false`) exits 2, and the config's `lsp.enabled` is ignored on resume. Servers and limits come from the current invocation.

## Exit status

The run ends when the model writes `COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT` (`Submitted`), stops calling tools (`Completed`), or hits a limit (`StepLimitExceeded`, `CostLimitExceeded`, `TimeLimitExceeded`), Ctrl-C (`Interrupted`), a non-retryable API error (`Error`), too many consecutive malformed tool-call steps (`RepeatedFormatError`, see below), or, with `--output-schema`, 3 consecutive invalid results (`InvalidOutput`), or stop hooks still rejecting the result after `max_stop_continuations` (`VerificationFailed`, see [Stop hooks](#stop-hooks)), or too many consecutive responses cut off at the output-token limit (`OutputTruncated`, see [Output truncation](#output-truncation)). The process exits 0 only for `Submitted` and `Completed`, 3 for `OutputTruncated`, and 1 otherwise (2 is reserved for usage errors).

Every run ends with one summary line on stderr, so stdout keeps only the model's text:

```text
Submitted after 12 steps in 1m42s (48,213 input / 3,104 output tokens, $0.23)
```

Cost is shown only when it's above zero, and the token/cost part is left out if the run ended before any model response. When some input was read from the prompt cache, the line also shows how much, e.g. `(48,213 input / 3,104 output tokens, 41,900 cached, $0.09)`. With `-o`, the trajectory JSON also records `input_tokens` and `output_tokens` totals, plus `cache_read_tokens` and `cache_write_tokens` when they're non-zero.

### Malformed tool calls

A tool call is a *format error* when its arguments aren't valid JSON or not a JSON object, its tool name isn't offered, or its arguments miss a field the tool's schema marks `required` or give a field the wrong JSON type (integers may still be sent as numeric strings). The tool isn't run; the model gets a short correction instead, such as `format error: read_file is missing required field "path". Call one of: read_file, ... with arguments matching its schema.`, and the run continues. Errors from a well-formed call that runs (a command exiting 1, a missing file) are not format errors, and in `--read-only` mode a non-read-only built-in keeps its read-only denial.

A step whose tool calls are all format errors counts toward a consecutive limit; any step with at least one well-formed call resets it. After `--max-format-errors` (config `max_format_errors`, default 3) such steps in a row, the run ends with `RepeatedFormatError`, an empty submission, and a non-zero exit. Format-error steps are ordinary model calls, so they count toward `--step-limit` and `--cost-limit`, which are checked first. A response with no tool calls still ends the run as `Completed`.

The summary line shows the count (`..., 3 format errors: <last reason>`). With `--json`, a malformed call's `tool.result` event has `"format_error": true` and the `result` event has `format_errors`. With `-o`, the trajectory records `format_errors`, `consecutive_format_errors`, `format_error_messages` (indexes of the correction messages) and `last_format_error`, and keeps the original arguments. The consecutive count is restored on `--resume`, and a `RepeatedFormatError` checkpoint can be resumed.

### Output truncation

When a response stops because it hit the output-token limit (Anthropic `stop_reason` `max_tokens` or `model_context_window_exceeded`, Chat Completions `finish_reason` `length`, Responses/Codex `incomplete` with reason `max_output_tokens`), tinyagent treats it as cut off rather than finished. None of its tool calls run, not even ones that look complete, and no hooks run for them; each gets the result `error: [not run: your response was cut off at the output-token limit ...]`, and the model is asked to continue in smaller pieces (for example, write a large file in several edits). A cut-off response never ends the run as `Submitted` or `Completed`, and does not count as a format error or an invalid structured result. Invalid partial tool-call JSON is replaced by `{}` when the conversation is sent back to the provider.

After `max_truncations` (default 3) cut-off responses in a row, the run ends with `OutputTruncated` and exits 3; any complete response resets the count. Raise the budget with `--max-output-tokens` (config `max_output_tokens`; sent as `max_tokens` on Anthropic and Ollama, `max_completion_tokens` on OpenAI/OpenRouter Chat Completions, `max_output_tokens` on the OpenAI Responses transport; `codex` exits 2). The budget may be changed on `--resume`, so an `OutputTruncated` checkpoint can be resumed with a larger one.

A warning goes to stderr, the summary line shows `..., N truncated responses`, `--json` (and `--log-db`) emit a `model.truncated` event with `step`, `reason`, `max_output_tokens`, `tool_calls_dropped` and `consecutive`, the dropped calls' `tool.result` events have `"truncated": true`, and the `result` event and trajectory record `truncations` (trajectories also `consecutive_truncations` and `max_output_tokens`).

## Troubleshooting

When a run fails on a common setup problem, tinyagent prints a `hint:` line under the error. The error itself is kept, and hints name environment variables without printing their values.

To check your setup before a run, use `tinyagent --check` with the same `-m`, `-c`, `--cwd`, fallback and limit flags you'd pass to a run. It sends no model request and writes nothing. It prints which config file, model, provider, fallbacks, working directory and limits a run would use, and where each came from, then `ok` (exit 0). A problem a run would hit, such as an unknown provider, a bad config key or a missing API key, is printed the same way a run prints it, with its `hint:`, and exits 2. Key values are never printed, only whether the variable is set.

```
$ tinyagent --check -m anthropic/claude-sonnet-4
config:    none (built-in defaults)
model:     anthropic/claude-sonnet-4 (from -m)
provider:  anthropic, ANTHROPIC_API_KEY is set
fallback:  none
cwd:       /home/me/project (default)
limits:    steps unlimited, cost unlimited, time unlimited
ok
```

| Symptom | Fix |
|---|---|
| `HTTP 401` or `403` from openai, openrouter or anthropic | Set the key: `export OPENAI_API_KEY=...` (or `OPENROUTER_API_KEY`, `ANTHROPIC_API_KEY`). If it's already set, check that it's current. |
| `HTTP 404` from openai, openrouter or anthropic | Check the model name after the `/`. |
| `connection refused` with `ollama/...` | Start Ollama with `ollama serve`, or set `OLLAMA_HOST` to where it runs. |
| `HTTP 404` with `ollama/...` | Pull the model: `ollama pull <model>`. |
| `every model in the fallback chain failed` | Each listed model used up its retries; the message names each one and why. Check the providers' status pages, or add a model from another provider to `fallback_models`. |
| `codex: no login` | Run `codex login` and choose ChatGPT. |
| `model is not supported` with `codex/...` | Use a model the Codex CLI's model picker shows for your plan. |

## Sandboxing commands

`--sandbox` (or `sandbox: true`) confines every `bash` and `start_process` command, and all processes they spawn, at the OS level. Commands can write only inside `--cwd`, a per-run temporary directory (exported as `TMPDIR`, removed when the run ends), the directories in `sandbox_writable_paths` and any `write` [additional directories](#additional-directories). Reads stay unrestricted so toolchains work. Outbound network is denied unless you pass `--sandbox-network` (`sandbox_network: true`). Flags override the config, including `--sandbox=false`. It is off by default.

```yaml
sandbox: true
sandbox_network: false
sandbox_writable_paths:        # absolute paths to existing directories
  - /home/me/.cache/go-build
```

Toolchains that cache outside the repo (Go, npm, pip) fail under the sandbox with `Permission denied` until their cache directories are listed in `sandbox_writable_paths`.

- **Linux:** Landlock, applied by a re-executed helper to the command only (tinyagent itself is unrestricted). Needs Linux 6.2+ (Landlock ABI 3) to confine writes, and 6.7+ (ABI 4) to deny TCP. Landlock cannot restrict UDP before ABI 10 and does not cover Unix sockets, so "network denied" means TCP connect and bind. WSL2 counts as Linux.
- **macOS:** Seatbelt through `/usr/bin/sandbox-exec` (deprecated by Apple but still shipped). This backend has not been tested in CI.
- **Windows:** not supported.
- **Fail closed:** if the host cannot enforce the requested policy, tinyagent exits before the first model request and says what is missing. It never runs commands unsandboxed.
- On start, one `sandbox:` line (backend, writable roots, network) goes to stderr, and `-o` trajectories record it as `sandbox`. With `--resume`, the current invocation's settings apply. With `--read-only` there are no command tools, so nothing is sandboxed.
- **Not covered:** [`web_fetch`](#web-fetch), which has its own allowlist and is unaffected by `--sandbox`, the in-process file tools (`read_file`, `write_file`, ... and their symlink behavior; see [Restricting file access](#restricting-file-access) for rules on them), MCP servers, and provider requests. Commands can still read anything you can, including credentials, and inherit your environment variables (API keys included) unless you set a [command environment](#command-environment) policy. With `--sandbox-network` they can send it to any host; prefer a [network allowlist](#network-allowlist). The sandbox reduces risk; it does not make untrusted code safe.

### Network allowlist

`sandbox_allowed_domains` (or `--sandbox-allow-domain <host>`, repeatable, which replaces the config list) lets sandboxed commands reach only the hosts you list, over HTTP and HTTPS. tinyagent runs a small proxy outside the sandbox that checks each request's hostname, and sets `HTTP_PROXY`, `HTTPS_PROXY`, `ALL_PROXY` (and their lowercase forms) to it, with `NO_PROXY` empty. The OS sandbox lets commands connect only to that proxy. It requires `--sandbox` and can't be combined with `--sandbox-network` (exit 2 otherwise). Without the key or flag, nothing changes.

```yaml
sandbox: true
sandbox_allowed_domains:
  - proxy.golang.org       # Go modules
  - sum.golang.org
  - registry.npmjs.org     # npm
  - pypi.org               # pip
  - files.pythonhosted.org
sandbox_writable_paths:    # their caches, see above
  - /home/me/go/pkg/mod
```

- **Entries:** an exact hostname (`api.example.com`), `*.example.com` for its subdomains only (not `example.com` itself), or an IP literal (`10.0.0.5`, `[::1]`). Each may end with `:port`; otherwise ports 80 and 443 are allowed. Matching ignores case and a trailing dot. A bare `*` is a config error.
- **Private addresses:** the proxy resolves an allowed name itself and refuses to connect to loopback, private, link-local (including the `169.254.169.254` cloud metadata address) and other non-public addresses, unless that address is listed as a literal for the port.
- **Refusals:** the command gets HTTP `403` with an `X-Proxy-Error: blocked-by-allowlist` header and a body such as `tinyagent sandbox: host not in sandbox_allowed_domains: evil.example (port 443)` (for HTTPS, clients report the proxy's 403 on `CONNECT`). Each refusal is a `sandbox.network_denied` event with `--json`, is listed under `sandbox_network_denials` in `-o` trajectories, and is printed to stderr with `-v`. Allowed requests are not logged.
- **Only proxy-aware clients work:** curl, Go, npm, pip, git over HTTPS and most HTTP libraries honor the proxy variables. Programs that ignore them, and anything that isn't HTTP(S) (SSH, raw TCP, UDP, DNS lookups), cannot connect. Proxy variables in your own environment are replaced, and the proxy connects directly rather than through an upstream proxy.
- **Linux:** each command runs in its own unprivileged user and network namespace, which has only loopback. tinyagent forwards a loopback port there to the proxy's Unix socket (in a private temporary directory removed when the run ends), and Landlock additionally allows TCP connections only to that port. This needs Landlock ABI 4 and unprivileged user namespaces. Ubuntu 23.10+ restricts these with AppArmor (`kernel.apparmor_restrict_unprivileged_userns=1`), and other distributions may disable them (`kernel.unprivileged_userns_clone`, `user.max_user_namespaces`); there, tinyagent exits before the first model request with a message naming the cause. Commands appear as your own uid, but other users and groups map to `nobody`. Unix sockets on the filesystem (for example a Docker socket) are still reachable, as without the allowlist.
- **macOS:** the proxy listens on a loopback port and the Seatbelt profile allows outbound connections only to that port on localhost.
- **Exfiltration:** an allowlist limits where data can go, not what is sent. Commands can upload anything they can read to an allowed host, and broad entries (`github.com`, `*.amazonaws.com`, CDN wildcards) allow uploads to other users' content and domain fronting, since TLS isn't inspected. List only the hosts a task needs.
- The startup line shows e.g. `network: allowlist (2 domains: proxy.golang.org, sum.golang.org)`, and the trajectory's `sandbox` record includes `allowed_domains`. The proxy stops when the run ends, however it ends; background processes from `start_process` use it until then.


## Command environment

By default `bash` and `start_process` commands inherit tinyagent's whole environment, including the provider API keys tinyagent itself needs, so a prompt-injected `env` or `echo $OPENAI_API_KEY` can read them. `command_env` (config only, from the user config or `-c`) controls what they get instead:

```yaml
command_env:
  inherit: core                       # all (default) | core | none
  exclude_secrets: true               # default false
  exclude: ["AWS_*", "NPM_TOKEN"]     # names or * ? [ ] globs
  pass: [GOFLAGS, GOPATH, GITHUB_TOKEN]   # names or globs copied from tinyagent's environment
  set:                                # literal string values
    CI: "true"
```

`--command-env all|core|none` overrides `inherit` for one run. The environment is built once per run, in this order:

1. **Base** from `inherit`: `all` is every variable; `core` keeps `PATH`, `HOME`, `USER`, `LOGNAME`, `SHELL`, `LANG`, `LC_*`, `TERM`, `TZ`, `TMPDIR`, plus on Windows `SystemRoot`, `WINDIR`, `USERPROFILE`, `TEMP`, `TMP`, `PATHEXT`, `COMSPEC`; `none` is empty.
2. **`exclude_secrets`** removes names ending in `_KEY`, `_TOKEN`, `_SECRET` or `_PASSWORD` (the same rule `--log-db` redaction uses) and always `OPENAI_API_KEY`, `ANTHROPIC_API_KEY` and `OPENROUTER_API_KEY`.
3. **`exclude`** removes matching names.
4. **`pass`** adds matching names back from tinyagent's environment, even ones removed above. Entries that match nothing are skipped (`-v` lists them).
5. **`set`** applies literal values.
6. With `--sandbox`, the sandbox's `TMPDIR` is applied last.

Name matching is case-insensitive on Windows and case-sensitive elsewhere. tinyagent's own environment is unchanged, so providers, MCP servers and hooks behave as before; every process a command spawns inherits the filtered environment, with or without `--sandbox`. An invalid `inherit`, an empty or `=`-containing name, a malformed glob, a non-string `set` value or an unknown key exits 2 before any provider request.

When a policy is set (or `--command-env` is passed), one line such as `command env: core, secrets excluded; 14 passed, 37 withheld` goes to stderr; `-v` also lists the passed and withheld names. `-o` trajectories and `--json` `run.started` record `command_env` (mode, `exclude_secrets`, passed names). Values are never printed or recorded; `--log-db` also redacts `set` values. With `--resume`, the current invocation's policy applies. `--read-only` runs have no command tools, so the policy has no effect there (it is still validated). The `container` environment forwards no host variables and ignores the policy.

For CI on untrusted input (issue bodies, PR diffs), use `inherit: core` with `exclude_secrets: true`; for reproducible runs, `inherit: none` with `set`. `core` and `none` can break toolchains that need other variables (`GOPATH`, `JAVA_HOME`, proxy settings): check the `-v` lists and add them to `pass`.

This is not a sandbox. It does not protect credentials stored in files (`~/.aws/credentials`, `.env`, the Codex auth file), which commands can still read ([`file_access`](#restricting-file-access) rules hide them from the file tools only), and it does not redact secrets from tool output, trajectories or `--json`. `pass` and `set` deliberately give commands the values you name.

## Restricting file access

`file_access` (config only, from the user config or `-c`; a file inside `--cwd` is never read) denies the built-in file tools access to matching paths. It is off by default.

```yaml
file_access:
  deny_read: [".env", "*.pem", "secrets/", "**/id_rsa"]   # also write-denied
  deny_write: [go.sum, "/vendor/"]                        # readable, not writable
```

- **Patterns** are gitignore-style and relative to `--cwd`: `*`, `?` and `[...]` match within one path segment, `**` matches any number of segments, a leading `/` (or any `/` before the end) anchors the pattern to `--cwd`, a trailing `/` matches directories only, and a pattern without a slash matches a name at any depth. A rule on a directory covers everything below it. Empty patterns, `.`/`..` segments, bad glob syntax and `!` negation (reserved) are config errors (exit 2). Absolute and `~` paths are not supported. Matching ignores case on macOS and Windows.
- **Reads:** `read_file`, `view_image`, and a `list_files` or `grep` aimed at a denied path return an error naming the rule, such as `.env is denied by file_access.deny_read rule ".env"`. Walks (`list_files`, `grep`) skip denied files and directories without opening them and end with a neutral count such as `(2 paths hidden by file_access rules)`, which names none of them. The count includes denied files only when they would have matched the pattern or `include`; a denied directory counts once.
- **Writes:** `write_file`, `edit_file` and `apply_patch` refuse paths matching `deny_write` or `deny_read`. `apply_patch` checks every operation (including `Move to` destinations) before writing, so a patch that touches one denied path changes nothing.
- **Symlinks:** a path is checked as named and through its resolved target (for a new file, through its deepest existing directory), so `innocent.txt -> .env` or a symlinked directory pointing into `secrets/` is denied too. Walks don't follow symlinked directories.
- With `-v`, one stderr line gives the rule counts. `-o` trajectories and `--json` `run.started` record `file_access` (`deny_read`, `deny_write`). With `--resume`, the current invocation's rules apply. A `deny_read` rule that hides the [tool output](#tool-output-files) directory fails at startup, since the model is told to read it. AGENTS.md and skills loading are not affected.
- **Not covered:** `bash`, `start_process`, hooks, MCP servers and execution environments can still read and write anything you can; combine with `--read-only`, [`--sandbox`](#sandboxing-commands) or a [command environment](#command-environment) policy as needed. Checks happen before each operation, so a file swapped for a symlink in between (a race) is not caught. This narrows what the model's file tools can touch; it is not a security boundary.

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
- **Other features:** [`file_access`](#restricting-file-access) patterns apply inside each directory relative to it. [Subagents](#subagents) can read them. With [`--sandbox`](#sandboxing-commands), write directories are added to the writable roots; with the [`container`](#execution-environments) environment, each is mounted at the same path (read ones `:ro`). [Snapshots](#rewind-workspace-changes) cover only `--cwd`, so stderr warns that `--rewind` won't restore changes made in additional directories.

## Execution environments

The environment decides where `bash` and `start_process` commands run. Choose it with `--env local|container` or `environment:` in the config; the flag wins, and the default is `local`.

- **`local`** (default) runs `bash -c` on the host, exactly as before. `--sandbox` is a property of this environment (see [Sandboxing commands](#sandboxing-commands)).
- **`container`** runs each command with `<runtime> exec ... bash -c <command>` in one container started for the run:

```yaml
environment: container
container:
  runtime: docker          # or podman; default docker
  image: golang:1.23       # required; must provide bash
  run_args: [--network, none]   # optional extra `run` arguments, added after tinyagent's defaults
```

Before the first model request tinyagent runs `<runtime> run -d --rm --init --label tinyagent=1 -v <cwd>:<cwd> -w <cwd> --user <uid>:<gid> -e HOME=/tmp <run_args> <image> sleep infinity` and prints one line to stderr, e.g. `environment: container (docker, image=golang:1.23); mount: /home/me/repo`. `-o` trajectories record it as `environment` (name, runtime, image).

- **Mount model:** only `--cwd` (plus any [additional directories](#additional-directories), read ones `:ro`) is shared, at the same path, so the in-process file tools on the host and commands in the container see the same files. Commands run as your uid:gid so files they create are yours on the host (for rootless Podman add `run_args: [--userns=keep-id]`). `$HOME` is `/tmp` inside the container; writes outside `--cwd` stay in the container. Later `run_args` such as `--user` or `-e HOME=...` override these defaults.
- **What runs where:** only `bash` and `start_process` commands run in the container. File tools, MCP servers, provider requests and the trajectory stay on the host. No host environment variables are forwarded (pass them with `run_args: [-e, NAME]` if needed). The network is the runtime's default unless `run_args` say otherwise.
- **Timeouts and `stop_process`** kill the command's processes inside the container (each command is tagged with a `TINYAGENT_EXEC` variable that its children inherit), not only the host-side `exec` client. Output format, return codes, the process limit and buffers are unchanged.
- **Cleanup:** the container is removed when the run ends: normally, on error, at a limit, or on Ctrl-C. `docker ps -a --filter label=tinyagent` lists any left behind by a killed tinyagent.
- **Fail closed:** a missing runtime, an unreachable daemon or an image that won't start exits with an error before any provider request. tinyagent never falls back to running commands on the host.
- `--sandbox` can't be combined with `--env container` (exit 2); use `run_args` such as `--network none` or `--read-only` to restrict the container. `--read-only` runs start no container. With `--resume`, the current invocation's environment applies. Not supported on Windows.

## Notes

- `read_file` takes optional `offset` (1-based start line) and `limit` (max lines) to read part of a large file. A partial read ends with `[lines A-B of N]`; with neither set, it returns the whole file as before. With `--tool-output-files`, over-limit tool results are saved to files the model reads this way; see [Tool output files](#tool-output-files). Binary files are rejected; use `bash` (for example, `file` or `xxd | head`) to inspect them.
- `edit_file` replaces exactly one occurrence of `old_str` and fails if it occurs zero or several times. Pass `replace_all: true` to replace every occurrence (for a rename); it still fails if there are none, and returns `ok (N replacements)`.
- `grep` uses Go regular expressions by default. Set `fixed_strings: true` for literal text, e.g. `{"pattern":"func(","path":".","include":"*.go","fixed_strings":true}` finds literal `func(` in Go files without regex escaping.
- `grep` accepts `ignore_case: true` for case-insensitive content searches, e.g. `{"pattern":"error","path":".","include":"*.log","ignore_case":true}` matches `error`, `Error`, and `ERROR` in `.log` files. The default is case-sensitive; inline regex flags such as `(?i)` still work. The `include` glob stays case-sensitive.
- `grep` accepts `context_lines` from 0 to 20 to include that many surrounding lines on each side, e.g. `{"pattern":"connection failed","path":"internal","include":"*.go","fixed_strings":true,"context_lines":1}`. Matches use `file:line: content`; context uses `file-line- content`, and `--` separates excerpts. The default is 0 and preserves match-only output. Context excerpts merge when they overlap or touch; positive-context results stop at 500 matches or 500 source lines and say when output may be incomplete. Use `read_file` with `offset` and `limit` for a larger or targeted excerpt.
- Each `bash` call runs in a new process; `cd` and exported variables don't carry over. On timeout the whole process group is killed (Unix). The model can pass an optional `timeout` (1 to 600 seconds) on a `bash` call to override `bash_timeout_seconds` for that command.
- `bash` passes `stream: true` to also print a command's output to stdout as it is produced; the result returned to the model is the same. By default nothing is streamed.
- For servers, watch modes, and other long-running commands, `start_process` runs `bash -c <command>` in the background and returns a process id (`p1`, `p2`, ...) with its first output. `read_process` returns only output produced since the last read, optionally waiting up to `timeout` milliseconds (max 60000) for some, or just the last `tail` lines. `stop_process` kills the process and its children (the whole process group on Unix) and reports the exit status and unread output. For example: start a server, `read_process` until it logs "listening", run tests against it, then stop it.
- At most `max_background_processes` (default 4) run at once, and each keeps only its last 1 MiB of output; a read notes how many bytes were dropped. When the run ends for any reason (submission, a limit, Ctrl-C, or an error), every background process still registered is killed and its status and output tail are saved as `background_processes` in the trajectory, so nothing is left running. On Windows only the `bash` process itself is killed.
- Path sandboxing is lexical: `..` and absolute paths outside `--cwd` are rejected, but a symlink inside the root that points outside it is followed. `bash` isn't sandboxed unless you pass `--sandbox` or `--env container` (see Sandboxing commands and Execution environments); without them, only run it on code you trust.
- Cost uses a small built-in price table (`internal/model/provider.go`). Unknown models cost $0 unless you set the price in the config (top level, or per model under a [named provider](#named-providers)), so `--cost-limit` does nothing for them. With a positive cost limit, tinyagent prints a warning on stderr before the first request when the `openai`, `openrouter`, `anthropic` or named-provider model has no price, naming `input_price_per_mtok` and `output_price_per_mtok`. The run continues as normal.
- The `anthropic` provider turns on [prompt caching](https://platform.claude.com/docs/en/build-with-claude/prompt-caching) for every request, so each step reads the history the previous step already sent from the cache instead of paying full price for it again. Input tokens in the summary and trajectory include cached tokens. Cost prices cache writes at 1.25× the input price and cache reads at 0.1× (less on models where Anthropic charges less, such as 0.05× for Claude Opus 5.5 and Sonnet 5.5). `input_price_per_mtok` overrides the base price and the multipliers apply on top.
