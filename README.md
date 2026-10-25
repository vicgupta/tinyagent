# tinyagent

```bash
curl -fsSL https://raw.githubusercontent.com/vicgupta/tinyagent/main/install.sh | sh
```

A minimal coding agent in a single static Go binary. It gives an LLM nine tools (`read_file`, `write_file`, `edit_file`, `list_files`, `grep`, `bash`, and `start_process`, `read_process`, `stop_process` for background commands), plus the opt-in `update_plan`, `skill`, and `view_image` tools, and keeps a linear message history until the model submits.

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
| `-m, --model` | `$AGENT_MODEL`, else config `model`, else `openai/gpt-4o` | `<provider>/<model>`; no `/` means openai. Providers: openai, openrouter, anthropic, ollama, codex |
| `--reasoning-effort` | config `reasoning_effort`, else unset | Reasoning effort sent to the provider: `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max`; see [Reasoning](#reasoning) |
| `--openai-api` | config `openai_api`, else `auto` | Transport for the `openai` provider: `auto`, `responses`, or `chat`; see [OpenAI transport](#openai-transport) |
| `-c, --config` | user config, else built-in | YAML config file; see [Config](#config) |
| `--cwd` | `.` | Root directory; tools can't reach outside it |
| `--step-limit` | 0 (unlimited) | Max model calls |
| `--cost-limit` | 0 (unlimited) | Max USD |
| `--time-limit` | 0 (unlimited) | Max wall-clock seconds; overrides `wall_time_seconds` |
| `--max-format-errors` | config `max_format_errors`, else 3 | Stop with `RepeatedFormatError` after this many consecutive steps whose tool calls are all malformed; must be >= 1. See [Malformed tool calls](#malformed-tool-calls) |
| `--no-compact` | off | Never compact the history; see [Long runs](#long-runs) |
| `--plan` | config `plan.enabled`, else off | Give the model the `update_plan` tool; see [Plan tool](#plan-tool) |
| `--images` | config `images.enabled`, else off | Let vision-capable models see images: the `view_image` tool and MCP image pass-through; see [Images](#images) |
| `--plan-file` | — | Mirror the current plan to this Markdown file after each checkpoint save (requires the plan tool) |
| `--skills` | config `skills`, else off | Discover [Agent Skills](#agent-skills) and offer the `skill` tool |
| `--no-skills` | — | Disable skills even if the config enables them (`--no-skills=false` enables) |
| `--read-only` | config `read_only`, else off | Permit only `read_file`, `list_files`, and `grep`; blocks edits, shell, and process tools. `--read-only=false` explicitly disables it. |
| `--sandbox` | config `sandbox`, else off | Confine `bash`/`start_process` at the OS level: writes only under `--cwd`, a temp dir and `sandbox_writable_paths`, network off. See [Sandboxing commands](#sandboxing-commands) |
| `--sandbox-network` | config `sandbox_network`, else off | With `--sandbox`, allow network access |
| `--env` | config `environment`, else `local` | Where `bash`/`start_process` run: `local` (on the host) or `container` (Docker/Podman). See [Execution environments](#execution-environments) |
| `--command-env` | config `command_env.inherit`, else `all` | Environment `bash`/`start_process` inherit: `all`, `core` or `none`. See [Command environment](#command-environment) |
| `-o, --output` | — | Trajectory JSON, rewritten after every step |
| `-v, --verbose` | off | Print tool calls and their outputs |
| `--json` | off | Write machine-readable run events as JSONL to stdout |
| `--output-schema` | | End the run with a JSON result validated against this JSON Schema file (printed to stdout) |
| `--log-db[=path]` | off | Also record run events in a local SQLite database (default `<cwd>/.tinyagent/tinyagent.db`); see [Event log database](#event-log-database) |
| `--no-stream` | off | Wait for each full response instead of streaming it |
| `--no-agents-md` | off | Don't append the nearest `AGENTS.md` (at or above `<cwd>`, up to the repo root; capped at 32KB) to the system prompt |
| `-V, --version` | | Print the version and exit |
| `--update` | | Replace this binary with the latest GitHub release (checksum-verified) and exit |

### Machine-readable output

`--json` makes stdout a UTF-8 JSONL event stream, with one complete JSON object and newline per event. It can be combined with `--no-stream`, `--read-only`, `--resume`, and `-o` (which still writes the trajectory JSON to its file). In this mode, streamed assistant text and live bash output are represented by events, and `--verbose` has no effect because tool calls and results are already events; the human summary and warnings remain on stderr. Setup failures before a run starts may leave stdout empty.

Every event has a `type`. Event names and fields in schema version 1 are:

| Type | Fields |
|---|---|
| `run.started` | `schema_version` (currently `1`), `model`, canonical `root`, `read_only`, `resumed`, `follow_up` (`true`, only for a follow-up run), `reasoning_effort` (only when set), `openai_api` (the resolved `responses` or `chat`, only for the `openai` provider), `command_env` (`inherit`, `exclude_secrets`, sorted `passed` names; only with a [command environment](#command-environment) policy) |
| `assistant.message` | `text` (complete assistant message) |
| `tool.call` | `id`, `name`, `arguments` (decoded JSON value, or a string if malformed) |
| `tool.result` | `id`, `name`, `output` (same configured truncation as tool output), `is_error` |
| `hook` | `event` (`pre_tool`, `post_tool`, or `stop`), `tool_call_id`, `tool_name`, `command`, `decision` (`allow`, `deny`, `feedback`, `continue`, or `error`), `duration_ms`; tool hooks are emitted between the call's `tool.call` and `tool.result`, and `stop` events have no tool fields |
| `plan.updated` | `step`, `version`, `items` (`id`, `text`, `status`), `note`, `diff` |
| `checkpoint.saved` | `step`, `path`, `pending` (tool calls without results yet) |
| `resume.interrupted_call` | `id`, `name` of a call that was in flight when the previous process stopped |
| `resume.process_lost` | `id`, `pid`, `cmd` of a background process from the previous invocation |
| `compaction` | `step`, `reason`, `stage`, `tokens_before`, `tokens_after`, `elided_messages`, `summarized_messages` |
| `warning` / `error` | `message` |
| `provider.retry` | `attempt` (the upcoming attempt number), `reason` (`http_<status>` or `network`), `backoff_ms`; only emitted when a model request is retried |
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

### Resume an unfinished task

```sh
# Checkpoint before and after every tool call; this run stops at 20 cumulative model steps.
tinyagent -t "fix the failing test" --cwd ~/src/project --step-limit 20 -o run.json

# Inspect the workspace (for example, git diff) and rerun relevant checks.
# Continue the same task with a ceiling of 40 total steps.
tinyagent --resume run.json --cwd ~/src/project --step-limit 40 -o continued.json
```

`--resume <trajectory.json>` is noninteractive and uses the saved task and model. Do not pass `--task` for an unfinished checkpoint (it is only for [follow-ups](#continue-a-finished-run-with-a-follow-up-task) of finished runs); an explicitly different `--model` is rejected. The saved model wins over `$AGENT_MODEL` and the config's default model. Credentials and provider endpoints come from the current normal setup, never the checkpoint. Paths to checkpoints are relative to the invoking shell's directory, independently of `--cwd`.

An explicit `--cwd` must resolve to the recorded canonical root (directory symlinks are resolved). The effective system prompt must match the saved prompt, including `{{cwd}}`/`{{os}}` substitutions and the recorded AGENTS.md loading mode. Current config is used to reconstruct it without appending AGENTS.md twice. Changed instructions, a conflicting `--no-agents-md`, or changed tool definitions/compatibility require a fresh task. In particular, a task that edits its effective AGENTS.md cannot resume under changed instructions.

Only new-format, unfinished checkpoints are eligible: an in-progress save (including one left by a crash or `kill -9`) or a stop caused by a step/cost/time limit, interruption, or error. Successful `Submitted`/`Completed` runs are not resumed this way; continue them with a follow-up task instead (see below). Legacy logs, unsupported versions/state, and malformed or partial tool turns are refused before provider setup. Checkpoint loading is limited to regular files of at most **64 MiB**. Larger trajectories remain audit logs.

With `-o`, the checkpoint is written before tools run (recording the calls as `pending_calls`) and again after each tool result. If the process dies mid-turn, `--resume` gives every call without a result the error result `[interrupted: tool did not complete before the agent stopped; it may have partially run — verify state before retrying]` and emits `resume.interrupted_call`. **Interrupted calls are never re-executed automatically**: a half-run `bash` or edit may have had side effects, so the model decides what to inspect or retry. Background processes started with `start_process` are recorded with their PID; they do not survive into a new invocation, so resume emits `resume.process_lost` and tells the model which ones are gone.

Compacted runs resume too. The trajectory always keeps the full history; the model's compacted view is rebuilt from the last `compactions` record (its message map, elisions, and summary) plus the messages after it, so the resumed request is the same one the original process would have sent. `--no-compact` is no longer needed to keep a run resumable.

A distinct `-o`/`--output` is required, and any existing destination is refused, including symlink and hard-link aliases of the source. The input stays unchanged. The validated starting state is saved to the new destination before the first request; completed tool turns and termination then replace it using a private temporary file. Initial publication requires filesystem hard-link support to publish atomically without replacing another file. A save failure in resume mode stops further work, prints an error, and exits nonzero, preserving the last complete checkpoint. Ordinary fresh-run save failures retain their existing warning behavior. Each save writes a private temporary file, fsyncs it, renames it over the checkpoint, and fsyncs the directory (where the platform supports it), so a crash or power loss leaves either the previous or the new complete checkpoint, never a partial one.

Steps, cost, token counts, and cache totals remain cumulative. Step/cost limits are cumulative ceilings: if already exhausted, no model request is made; increase the ceiling explicitly (or set it to 0 for unlimited). Runtime limits, tool timeouts, output settings, and compaction settings use normal config/flag precedence. Wall time and the stderr elapsed duration restart for each invocation; stopped time is not charged. Normal streaming, summaries, and success/failure exit codes still apply. The next action is a new model query, which incurs ordinary context/token costs; loading a checkpoint never dispatches historical tool calls.

New JSON keeps the existing top-level trajectory fields and adds versioned metadata: `schema_version` (currently 2; version 1 files still load), `root`, `system_prompt`, `skip_agents_md`, `tools`, and `tool_signature`. Version 2 adds `pending_calls`, `processes`, `plan_enabled`, `plan`, and `plan_history`; runs with skills enabled also record `skills_enabled` and `skills` (name, scope, and path of each discovered skill). `resume` records the input path, previous status/submission, cumulative step, and continuation-message index, plus `follow_up: true` for a follow-up boundary; nested `previous` entries retain earlier resume boundaries. These audit paths are not read as configuration. Metadata excludes credentials and environment dumps. Checkpoints still contain prompts, code, and tool outputs: treat them as private, trusted local execution inputs. New checkpoint and temporary files use owner-only permissions (0600) where supported.

A checkpoint restores **conversation state**, not files, Git state, shell variables, live processes, or external services. The model receives a continuation notice and stderr warns to inspect and revalidate the current workspace. Commands may have produced unrecorded side effects after the last save, especially during a hard crash in a tool batch. Historical process IDs cannot reattach to children from another invocation. There is no rollback or exactly-once execution guarantee; inspect current state before resuming.

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

Without `-c`, tinyagent loads a per-user config file if one exists, so settings you always want don't need a flag:

| OS | Default path |
|---|---|
| Linux and other Unix | `$XDG_CONFIG_HOME/tinyagent/config.yaml` (usually `~/.config/tinyagent/config.yaml`) |
| macOS | `~/Library/Application Support/tinyagent/config.yaml` |
| Windows | `%AppData%\tinyagent\config.yaml` |

The lookup order is: `-c PATH` if given (a missing or unreadable file is an error, with no fallback); otherwise the user config above, if it exists; otherwise the built-in defaults. A user config that exists but can't be read or parsed is an error naming the file, not skipped.

Every key is optional, but an unrecognized key (a typo like `step_limt`) is an error naming the file and the key, rather than silently ignored. Command-line flags override the file.

Set `read_only: true` to make repository analysis the default. `--read-only` overrides the config value, including `--read-only=false`. In this mode, executor dispatch allows only `read_file`, `list_files`, and `grep`; writes, edits, shell commands, and background process tools are denied even if task text or repository instructions request them. Denials are returned to the model as tool errors. The restriction is model tool authority, not an OS sandbox (for that, see `--sandbox` under Sandboxing commands): an explicit `-o` trajectory destination can be written by the CLI, provider requests still send their context to the selected service, and the Codex provider may refresh its credential file. `--update` is an administrative action outside task-tool policy. Readable secrets, symlinks that point outside `--cwd`, existing lexical path behavior, and AGENTS.md discovery retain their existing limitations; read-only mode does not make an untrusted repository safe.

`model` saves a default model, so `tinyagent -t "fix the failing test"` needs no `-m`. The model is chosen in this order: `-m`/`--model` if given (even `-m openai/gpt-4o`), then a nonempty `$AGENT_MODEL`, then a nonempty `model` in the config, then `openai/gpt-4o`.

```yaml
model: ollama/llama3.1    # same <provider>/<model> syntax as -m
system_prompt: |          # {{cwd}} and {{os}} are substituted
  You are a coding agent...
read_only: true            # expose only read_file, list_files, and grep
sandbox: false             # OS-confine bash/start_process; see Sandboxing commands
environment: local         # or container; see Execution environments
command_env:               # omitted by default (inherit everything); see Command environment
  inherit: core            # all, core or none
  exclude_secrets: true
step_limit: 50
cost_limit: 2.0
wall_time_seconds: 1800
max_format_errors: 3      # consecutive malformed-call steps before RepeatedFormatError (>= 1)
bash_timeout_seconds: 120
max_output_chars: 30000   # tool output is truncated, keeping head and tail
max_background_processes: 4   # start_process processes running at once (0 uses 4)
input_price_per_mtok: 3   # override the built-in price table
output_price_per_mtok: 15
context_window: 200000    # tokens; overrides the built-in table, used for compaction
plan:
  enabled: false          # give the model the update_plan tool (same as --plan)
skills: false             # discover Agent Skills and offer the skill tool (same as --skills)
images:
  enabled: false          # view_image tool and MCP image pass-through (same as --images)
max_image_bytes: 5242880  # largest image sent to the model (5 MiB); must be >= 1
log_db:                   # opt-in SQLite event log; see Event log database
  enabled: false
  path: .tinyagent/tinyagent.db   # relative to --cwd
  level: metadata         # metadata (no bodies) or full (redacted bodies)
  max_days: 30            # prune runs older than this; 0 keeps all
  max_runs: 500           # keep at most this many runs; 0 keeps all
  max_payload_bytes: 65536
reasoning_effort: medium  # optional; see Reasoning; --reasoning-effort wins
openai_api: auto          # openai provider transport: auto, responses, or chat; --openai-api wins
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
```

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

An unknown value exits 2 before any request. Which values a model accepts varies; a provider rejection is reported like any other model error and is not retried. The effort is recorded as `reasoning_effort` in trajectories and is kept on `--resume` (a different `--reasoning-effort` is refused, like `--model`). Higher effort means more output tokens, which providers bill and which count toward `--cost-limit`; on Anthropic, thinking also counts toward the request's 8192 `max_tokens`. Changing the effort invalidates provider prompt caches.

Reasoning state is stored in each assistant message's `extra` (`anthropic_content`, and `codex_reasoning` for Responses API reasoning items from both `codex` and `openai`) in trajectories and checkpoints, so `--resume` replays it. It may include thinking text and encrypted reasoning blobs; treat trajectory files as private. Reasoning state is not shown in the terminal or in `--json` events.

### Local MCP tools

MCP is disabled unless `mcp_servers` is explicitly configured. Each configured executable is trusted code with its own host and network permissions; this is tool preauthorization, not a sandbox or an interactive approval prompt. Configuration is read only from the user config or the explicitly selected `-c` file, never from the repository or model output. Tinyagent launches the command directly (no shell), with `--cwd` as its working directory and a minimal environment (`PATH`, plus the Windows process variables when present) and only the named forwarded variables. Missing forwarded variables fail before launch. Do not forward secrets unless the server needs them. Read-only runs never start MCP servers.

The Go MCP SDK v1.8.0 supports protocol revisions 2026-07-28, 2025-11-25, 2025-06-18, 2025-03-26, and 2024-11-05 over stdio. Tinyagent discovers the complete paginated catalog once per run, with a 15-second default discovery timeout, 100-page, 256-selected-tool, 64-KiB-per-schema, and 1-MiB-total-schema caps, and exposes only configured names. Tool names are sent to providers as `<alias>__<original-name>` and must fit provider-compatible naming rules. The accepted input schema subset supports object, array, string, number, integer, boolean and null types (a single type or an array such as `["string", "null"]`), nested properties/items, required, enum, const, numeric bounds and `multipleOf`, length, `pattern`, `format`, item and property counts, `uniqueItems`, annotations (`title`, `description`, `default`, `examples`), and boolean `additionalProperties`. A top-level `$schema` declaration is dropped. Other keywords, including `anyOf`/`oneOf`/`allOf` and references, are rejected, and a selected tool with such a schema stops startup. Each call defaults to a 60-second timeout; a timed-out call returns an error to the model and the server stays available, while any other call failure disables that server for the rest of the run. Text content is joined with newlines, structured output is appended as JSON, unsupported content is marked as omitted, tool errors remain visible, and model-visible output is capped at 256 KiB before the normal output cap is applied. Stdio protocol data is kept separate from server stderr.

MCP tool calls and results are stored in trajectories along with the external tool names. Trajectories from MCP-enabled runs cannot be resumed. Resuming a built-in-only checkpoint works with `mcp_servers` configured; the servers are not started and the run keeps the built-in tools. Trajectories and server stderr may contain sensitive data supplied by tools or the server; no blanket redaction is provided. On shutdown tinyagent closes MCP sessions and asks their processes to exit, then the SDK applies its bounded termination. Unix process descendants and Windows child-process cleanup have platform-specific behavior; tinyagent does not promise process-tree cleanup on Windows.

### Tool hooks

Hooks are disabled unless `hooks` is configured. A `pre_tool` hook runs before a matching tool call and can deny it with a reason the model sees; a `post_tool` hook runs after it and can append feedback, such as formatter or linter output, to the result the model sees. Like MCP servers, hooks are trusted local executables running with your privileges: they are read only from the user config or the `-c` file, never from the repository, launched directly (no shell) with an absolute `command`, `--cwd` as the working directory, and the same minimal environment (`PATH`, the Windows process variables when present, and only the named `env` variables, which must be set at startup). Provider API keys are not passed unless named. Hooks are a guardrail and automation point, not a sandbox: they see a call's parsed arguments, not what a shell command actually does.

Each hook gets one JSON object on stdin: `schema_version` (`1`), `event` (`pre_tool` or `post_tool`), `tool_name`, `tool_call_id`, `arguments` (decoded JSON, or the raw string if malformed), canonical `root`, `read_only`, and `step`; `post_tool` also gets `output` (the model-visible result after normal truncation) and `is_error`. The input can contain secrets from tool arguments and output.

| Event | Exit 0 | Exit 2 | Other exit, failure to start, timeout, or invalid stdout JSON |
|---|---|---|---|
| `pre_tool` | Allow, unless stdout is `{"decision":"deny","reason":"..."}` | Deny; the reason is stderr | Apply `on_error`: deny by default (the reason names the hook and the failure), or allow with a warning |
| `post_tool` | Optional stdout `{"feedback":"..."}` is appended to the result | stderr is appended to the result | Warning on stderr; the result is unchanged |

A denied call is not executed; the model gets the error result `error: denied by pre_tool hook <command>: <reason>` and the run continues. Matching hooks run one at a time in config order: the first `pre_tool` deny stops the rest, and every matching `post_tool` hook runs, each appending its feedback under `[post_tool hook <command>]` (capped at 8 KiB per hook; the combined result still honors `max_output_chars`). Read-only denials and unknown tools are rejected before hooks, so hooks never run for them and cannot widen authority; with `--read-only`, hooks still run for `read_file`, `list_files`, and `grep`. MCP tools are matched by their exposed `<alias>__<tool>` name. Ctrl-C or the wall-time limit kills a running hook (its process group on Unix; only the process on Windows). `-v` prints one stderr line per hook run, `--json` emits a `hook` event, and trajectories record the model-visible results, including denial reasons and feedback. Resumed runs use the hooks in the current config. Hooks do not run for `update_plan`, which only changes the run's plan.

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

The window comes from a small built-in table (`internal/model/context.go`) or `context_window` in the config, which wins. Without either, only the retry-on-error path runs. Each compaction prints one line to stderr, such as `compacted context: ~182,000 → ~31,000 tokens (summarized 41 messages)`. The summary request's tokens and cost count toward the run's totals and `--cost-limit`, but not `--step-limit`. With `-o`, the trajectory keeps every original message, and `compactions` records each one: the step, the estimates before and after, the stage, the summary, and which messages the model was sent next.

Summaries lose detail. Put instructions that must hold for the whole run in `AGENTS.md`, which is part of the system prompt and never compacted. `--no-compact` turns all of this off.

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

## Exit status

The run ends when the model writes `COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT` (`Submitted`), stops calling tools (`Completed`), or hits a limit (`StepLimitExceeded`, `CostLimitExceeded`, `TimeLimitExceeded`), Ctrl-C (`Interrupted`), a non-retryable API error (`Error`), too many consecutive malformed tool-call steps (`RepeatedFormatError`, see below), or, with `--output-schema`, 3 consecutive invalid results (`InvalidOutput`), or stop hooks still rejecting the result after `max_stop_continuations` (`VerificationFailed`, see [Stop hooks](#stop-hooks)). The process exits 0 only for `Submitted` and `Completed`.

Every run ends with one summary line on stderr, so stdout keeps only the model's text:

```text
Submitted after 12 steps in 1m42s (48,213 input / 3,104 output tokens, $0.23)
```

Cost is shown only when it's above zero, and the token/cost part is left out if the run ended before any model response. When some input was read from the prompt cache, the line also shows how much, e.g. `(48,213 input / 3,104 output tokens, 41,900 cached, $0.09)`. With `-o`, the trajectory JSON also records `input_tokens` and `output_tokens` totals, plus `cache_read_tokens` and `cache_write_tokens` when they're non-zero.

### Malformed tool calls

A tool call is a *format error* when its arguments aren't valid JSON or not a JSON object, its tool name isn't offered, or its arguments miss a field the tool's schema marks `required` or give a field the wrong JSON type (integers may still be sent as numeric strings). The tool isn't run; the model gets a short correction instead, such as `format error: read_file is missing required field "path". Call one of: read_file, ... with arguments matching its schema.`, and the run continues. Errors from a well-formed call that runs (a command exiting 1, a missing file) are not format errors, and in `--read-only` mode a non-read-only built-in keeps its read-only denial.

A step whose tool calls are all format errors counts toward a consecutive limit; any step with at least one well-formed call resets it. After `--max-format-errors` (config `max_format_errors`, default 3) such steps in a row, the run ends with `RepeatedFormatError`, an empty submission, and a non-zero exit. Format-error steps are ordinary model calls, so they count toward `--step-limit` and `--cost-limit`, which are checked first. A response with no tool calls still ends the run as `Completed`.

The summary line shows the count (`..., 3 format errors: <last reason>`). With `--json`, a malformed call's `tool.result` event has `"format_error": true` and the `result` event has `format_errors`. With `-o`, the trajectory records `format_errors`, `consecutive_format_errors`, `format_error_messages` (indexes of the correction messages) and `last_format_error`, and keeps the original arguments. The consecutive count is restored on `--resume`, and a `RepeatedFormatError` checkpoint can be resumed.

## Troubleshooting

When a run fails on a common setup problem, tinyagent prints a `hint:` line under the error. The error itself is kept, and hints name environment variables without printing their values.

| Symptom | Fix |
|---|---|
| `HTTP 401` or `403` from openai, openrouter or anthropic | Set the key: `export OPENAI_API_KEY=...` (or `OPENROUTER_API_KEY`, `ANTHROPIC_API_KEY`). If it's already set, check that it's current. |
| `HTTP 404` from openai, openrouter or anthropic | Check the model name after the `/`. |
| `connection refused` with `ollama/...` | Start Ollama with `ollama serve`, or set `OLLAMA_HOST` to where it runs. |
| `HTTP 404` with `ollama/...` | Pull the model: `ollama pull <model>`. |
| `codex: no login` | Run `codex login` and choose ChatGPT. |
| `model is not supported` with `codex/...` | Use a model the Codex CLI's model picker shows for your plan. |

## Sandboxing commands

`--sandbox` (or `sandbox: true`) confines every `bash` and `start_process` command, and all processes they spawn, at the OS level. Commands can write only inside `--cwd`, a per-run temporary directory (exported as `TMPDIR`, removed when the run ends) and the directories in `sandbox_writable_paths`. Reads stay unrestricted so toolchains work. Outbound network is denied unless you pass `--sandbox-network` (`sandbox_network: true`). Flags override the config, including `--sandbox=false`. It is off by default.

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
- **Not covered:** the in-process file tools (`read_file`, `write_file`, ... and their symlink behavior), MCP servers, and provider requests. Commands can still read anything you can, including credentials, and inherit your environment variables (API keys included) unless you set a [command environment](#command-environment) policy. The sandbox reduces risk; it does not make untrusted code safe.

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

This is not a sandbox. It does not protect credentials stored in files (`~/.aws/credentials`, `.env`, the Codex auth file), which commands can still read, and it does not redact secrets from tool output, trajectories or `--json`. `pass` and `set` deliberately give commands the values you name.

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

- **Mount model:** only `--cwd` is shared, at the same path, so the in-process file tools on the host and commands in the container see the same files. Commands run as your uid:gid so files they create are yours on the host (for rootless Podman add `run_args: [--userns=keep-id]`). `$HOME` is `/tmp` inside the container; writes outside `--cwd` stay in the container. Later `run_args` such as `--user` or `-e HOME=...` override these defaults.
- **What runs where:** only `bash` and `start_process` commands run in the container. File tools, MCP servers, provider requests and the trajectory stay on the host. No host environment variables are forwarded (pass them with `run_args: [-e, NAME]` if needed). The network is the runtime's default unless `run_args` say otherwise.
- **Timeouts and `stop_process`** kill the command's processes inside the container (each command is tagged with a `TINYAGENT_EXEC` variable that its children inherit), not only the host-side `exec` client. Output format, return codes, the process limit and buffers are unchanged.
- **Cleanup:** the container is removed when the run ends: normally, on error, at a limit, or on Ctrl-C. `docker ps -a --filter label=tinyagent` lists any left behind by a killed tinyagent.
- **Fail closed:** a missing runtime, an unreachable daemon or an image that won't start exits with an error before any provider request. tinyagent never falls back to running commands on the host.
- `--sandbox` can't be combined with `--env container` (exit 2); use `run_args` such as `--network none` or `--read-only` to restrict the container. `--read-only` runs start no container. With `--resume`, the current invocation's environment applies. Not supported on Windows.

## Notes

- `read_file` takes optional `offset` (1-based start line) and `limit` (max lines) to read part of a large file. A partial read ends with `[lines A-B of N]`; with neither set, it returns the whole file as before. Binary files are rejected; use `bash` (for example, `file` or `xxd | head`) to inspect them.
- `edit_file` replaces exactly one occurrence of `old_str` and fails if it occurs zero or several times. Pass `replace_all: true` to replace every occurrence (for a rename); it still fails if there are none, and returns `ok (N replacements)`.
- `grep` uses Go regular expressions by default. Set `fixed_strings: true` for literal text, e.g. `{"pattern":"func(","path":".","include":"*.go","fixed_strings":true}` finds literal `func(` in Go files without regex escaping.
- `grep` accepts `ignore_case: true` for case-insensitive content searches, e.g. `{"pattern":"error","path":".","include":"*.log","ignore_case":true}` matches `error`, `Error`, and `ERROR` in `.log` files. The default is case-sensitive; inline regex flags such as `(?i)` still work. The `include` glob stays case-sensitive.
- `grep` accepts `context_lines` from 0 to 20 to include that many surrounding lines on each side, e.g. `{"pattern":"connection failed","path":"internal","include":"*.go","fixed_strings":true,"context_lines":1}`. Matches use `file:line: content`; context uses `file-line- content`, and `--` separates excerpts. The default is 0 and preserves match-only output. Context excerpts merge when they overlap or touch; positive-context results stop at 500 matches or 500 source lines and say when output may be incomplete. Use `read_file` with `offset` and `limit` for a larger or targeted excerpt.
- Each `bash` call runs in a new process; `cd` and exported variables don't carry over. On timeout the whole process group is killed (Unix). The model can pass an optional `timeout` (1 to 600 seconds) on a `bash` call to override `bash_timeout_seconds` for that command.
- `bash` passes `stream: true` to also print a command's output to stdout as it is produced; the result returned to the model is the same. By default nothing is streamed.
- For servers, watch modes, and other long-running commands, `start_process` runs `bash -c <command>` in the background and returns a process id (`p1`, `p2`, ...) with its first output. `read_process` returns only output produced since the last read, optionally waiting up to `timeout` milliseconds (max 60000) for some, or just the last `tail` lines. `stop_process` kills the process and its children (the whole process group on Unix) and reports the exit status and unread output. For example: start a server, `read_process` until it logs "listening", run tests against it, then stop it.
- At most `max_background_processes` (default 4) run at once, and each keeps only its last 1 MiB of output; a read notes how many bytes were dropped. When the run ends for any reason (submission, a limit, Ctrl-C, or an error), every background process still registered is killed and its status and output tail are saved as `background_processes` in the trajectory, so nothing is left running. On Windows only the `bash` process itself is killed.
- Path sandboxing is lexical: `..` and absolute paths outside `--cwd` are rejected, but a symlink inside the root that points outside it is followed. `bash` isn't sandboxed unless you pass `--sandbox` or `--env container` (see Sandboxing commands and Execution environments); without them, only run it on code you trust.
- Cost uses a small built-in price table (`internal/model/provider.go`). Unknown models cost $0 unless you set the price in the config, so `--cost-limit` does nothing for them. With a positive cost limit, tinyagent prints a warning on stderr before the first request when the `openai`, `openrouter` or `anthropic` model has no price, naming `input_price_per_mtok` and `output_price_per_mtok`. The run continues as normal.
- The `anthropic` provider turns on [prompt caching](https://platform.claude.com/docs/en/build-with-claude/prompt-caching) for every request, so each step reads the history the previous step already sent from the cache instead of paying full price for it again. Input tokens in the summary and trajectory include cached tokens. Cost prices cache writes at 1.25× the input price and cache reads at 0.1× (less on models where Anthropic charges less, such as 0.05× for Claude Opus 5.5 and Sonnet 5.5). `input_price_per_mtok` overrides the base price and the multipliers apply on top.
