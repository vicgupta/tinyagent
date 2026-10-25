# tinyagent

```bash
curl -fsSL https://raw.githubusercontent.com/vicgupta/tinyagent/main/install.sh | sh
```

A minimal coding agent in a single static Go binary. It gives an LLM nine tools (`read_file`, `write_file`, `edit_file`, `list_files`, `grep`, `bash`, and `start_process`, `read_process`, `stop_process` for background commands) and keeps a linear message history until the model submits.

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
| `-t, --task` | — | Task prompt (required unless `--resume`); `-` reads it from stdin |
| `--resume` | — | Continue a validated unfinished trajectory; requires explicit matching `--cwd` and a new `-o` path |
| `-m, --model` | `$AGENT_MODEL`, else config `model`, else `openai/gpt-4o` | `<provider>/<model>`; no `/` means openai. Providers: openai, openrouter, anthropic, ollama, codex |
| `-c, --config` | user config, else built-in | YAML config file; see [Config](#config) |
| `--cwd` | `.` | Root directory; tools can't reach outside it |
| `--step-limit` | 0 (unlimited) | Max model calls |
| `--cost-limit` | 0 (unlimited) | Max USD |
| `--time-limit` | 0 (unlimited) | Max wall-clock seconds; overrides `wall_time_seconds` |
| `--max-format-errors` | config `max_format_errors`, else 3 | Stop with `RepeatedFormatError` after this many consecutive steps whose tool calls are all malformed; must be >= 1. See [Malformed tool calls](#malformed-tool-calls) |
| `--no-compact` | off | Never compact the history; see [Long runs](#long-runs) |
| `--read-only` | config `read_only`, else off | Permit only `read_file`, `list_files`, and `grep`; blocks edits, shell, and process tools. `--read-only=false` explicitly disables it. |
| `--sandbox` | config `sandbox`, else off | Confine `bash`/`start_process` at the OS level: writes only under `--cwd`, a temp dir and `sandbox_writable_paths`, network off. See [Sandboxing commands](#sandboxing-commands) |
| `--sandbox-network` | config `sandbox_network`, else off | With `--sandbox`, allow network access |
| `-o, --output` | — | Trajectory JSON, rewritten after every step |
| `-v, --verbose` | off | Print tool calls and their outputs |
| `--json` | off | Write machine-readable run events as JSONL to stdout |
| `--output-schema` | | End the run with a JSON result validated against this JSON Schema file (printed to stdout) |
| `--no-stream` | off | Wait for each full response instead of streaming it |
| `--no-agents-md` | off | Don't append the nearest `AGENTS.md` (at or above `<cwd>`, up to the repo root; capped at 32KB) to the system prompt |
| `-V, --version` | | Print the version and exit |
| `--update` | | Replace this binary with the latest GitHub release (checksum-verified) and exit |

### Machine-readable output

`--json` makes stdout a UTF-8 JSONL event stream, with one complete JSON object and newline per event. It can be combined with `--no-stream`, `--read-only`, `--resume`, and `-o` (which still writes the trajectory JSON to its file). In this mode, streamed assistant text and live bash output are represented by events, and `--verbose` has no effect because tool calls and results are already events; the human summary and warnings remain on stderr. Setup failures before a run starts may leave stdout empty.

Every event has a `type`. Event names and fields in schema version 1 are:

| Type | Fields |
|---|---|
| `run.started` | `schema_version` (currently `1`), `model`, canonical `root`, `read_only`, `resumed` |
| `assistant.message` | `text` (complete assistant message) |
| `tool.call` | `id`, `name`, `arguments` (decoded JSON value, or a string if malformed) |
| `tool.result` | `id`, `name`, `output` (same configured truncation as tool output), `is_error` |
| `hook` | `event` (`pre_tool` or `post_tool`), `tool_call_id`, `tool_name`, `command`, `decision` (`allow`, `deny`, `feedback`, or `error`), `duration_ms`; emitted between the call's `tool.call` and `tool.result` |
| `compaction` | `step`, `reason`, `stage`, `tokens_before`, `tokens_after`, `elided_messages`, `summarized_messages` |
| `warning` / `error` | `message` |
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

### Resume an unfinished task

```sh
# Save complete tool turns; this run stops at 20 cumulative model steps.
tinyagent -t "fix the failing test" --cwd ~/src/project --step-limit 20 --no-compact -o run.json

# Inspect the workspace (for example, git diff) and rerun relevant checks.
# Continue the same task with a ceiling of 40 total steps.
tinyagent --resume run.json --cwd ~/src/project --step-limit 40 --no-compact -o continued.json
```

`--resume <trajectory.json>` is noninteractive and uses the saved task and model. Do not pass `--task`; an explicitly different `--model` is rejected. The saved model wins over `$AGENT_MODEL` and the config's default model. Credentials and provider endpoints come from the current normal setup, never the checkpoint. Paths to checkpoints are relative to the invoking shell's directory, independently of `--cwd`.

An explicit `--cwd` must resolve to the recorded canonical root (directory symlinks are resolved). The effective system prompt must match the saved prompt, including `{{cwd}}`/`{{os}}` substitutions and the recorded AGENTS.md loading mode. Current config is used to reconstruct it without appending AGENTS.md twice. Changed instructions, a conflicting `--no-agents-md`, or changed tool definitions/compatibility require a fresh task. In particular, a task that edits its effective AGENTS.md cannot resume under changed instructions.

Only new-format, unfinished checkpoints at a complete model-query boundary are eligible: an in-progress save or a stop caused by a step/cost/time limit, interruption, or error. Successful `Submitted`/`Completed` runs cannot be extended. Legacy logs, unsupported versions/state, malformed or partial tool turns, and **compacted checkpoints** are refused before provider setup. This release does not restore the separate compacted model view; use `--no-compact` when the ability to resume later is needed. Checkpoint loading is limited to regular files of at most **64 MiB**. Larger trajectories remain audit logs.

A distinct `-o`/`--output` is required, and any existing destination is refused, including symlink and hard-link aliases of the source. The input stays unchanged. The validated starting state is saved to the new destination before the first request; completed tool turns and termination then replace it using a private temporary file. Initial publication requires filesystem hard-link support to publish atomically without replacing another file. A save failure in resume mode stops further work, prints an error, and exits nonzero, preserving the last complete checkpoint. Ordinary fresh-run save failures retain their existing warning behavior. Rename/link publication does not promise power-loss durability.

Steps, cost, token counts, and cache totals remain cumulative. Step/cost limits are cumulative ceilings: if already exhausted, no model request is made; increase the ceiling explicitly (or set it to 0 for unlimited). Runtime limits, tool timeouts, output settings, and compaction settings use normal config/flag precedence. Wall time and the stderr elapsed duration restart for each invocation; stopped time is not charged. Normal streaming, summaries, and success/failure exit codes still apply. The next action is a new model query, which incurs ordinary context/token costs; loading a checkpoint never dispatches historical tool calls.

New JSON keeps the existing top-level trajectory fields and adds versioned metadata: `schema_version` (currently 1), `root`, `system_prompt`, `skip_agents_md`, `tools`, and `tool_signature`. `resume` records the input path, previous status/submission, cumulative step, and continuation-message index; nested `previous` entries retain earlier resume boundaries. These audit paths are not read as configuration. Metadata excludes credentials and environment dumps. Checkpoints still contain prompts, code, and tool outputs: treat them as private, trusted local execution inputs. New checkpoint and temporary files use owner-only permissions (0600) where supported.

A checkpoint restores **conversation state**, not files, Git state, shell variables, live processes, or external services. The model receives a continuation notice and stderr warns to inspect and revalidate the current workspace. Commands may have produced unrecorded side effects after the last save, especially during a hard crash in a tool batch. Historical process IDs cannot reattach to children from another invocation. There is no rollback or exactly-once execution guarantee; inspect current state before resuming.

### Project instructions (AGENTS.md)

Unless `--no-agents-md` is set, tinyagent looks for `AGENTS.md` starting at `--cwd` and walking up through its parents, stopping at the repository root (the nearest directory containing `.git`). The nearest file wins, and only that one is added to the system prompt. Without a `.git` above `--cwd`, only `<cwd>/AGENTS.md` is checked. The lookup never goes above the repository root, and `-v` prints which file was loaded. This only changes the prompt: tools are still confined to `--cwd`.

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
```

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

A denied call is not executed; the model gets the error result `error: denied by pre_tool hook <command>: <reason>` and the run continues. Matching hooks run one at a time in config order: the first `pre_tool` deny stops the rest, and every matching `post_tool` hook runs, each appending its feedback under `[post_tool hook <command>]` (capped at 8 KiB per hook; the combined result still honors `max_output_chars`). Read-only denials and unknown tools are rejected before hooks, so hooks never run for them and cannot widen authority; with `--read-only`, hooks still run for `read_file`, `list_files`, and `grep`. MCP tools are matched by their exposed `<alias>__<tool>` name. Ctrl-C or the wall-time limit kills a running hook (its process group on Unix; only the process on Windows). `-v` prints one stderr line per hook run, `--json` emits a `hook` event, and trajectories record the model-visible results, including denial reasons and feedback. Resumed runs use the hooks in the current config.

Two examples: a `post_tool` hook on `write_file|edit_file` that runs `gofmt -l` on the edited path and prints `{"feedback":"..."}` when it lists files, and a `pre_tool` hook on `write_file|edit_file` that exits 2 with `edit schema.sql and run make gen instead` when `arguments.path` is under `migrations/`.

## Long runs

tinyagent resends the whole history on every step, so a long run can outgrow the model's context window. Before each request it estimates the request's size (the previous request's reported input tokens plus about 4 characters per token for anything added since). Above 80% of the window, it compacts:

1. Old tool outputs are replaced with a placeholder such as `[output elided: 12,400 chars]`. The system prompt, the task, and the most recent turns (about 25% of the window, and never fewer than the last 2 assistant turns with their tool results) are kept verbatim. If that brings the estimate under 50% of the window, it stops here.
2. Otherwise, one extra request asks the same model, with no tools, to summarize everything older than the recent turns. The model then sees the system prompt, the task, that summary, and the recent turns.
3. If it still doesn't fit, the run ends as `Error` with `context still exceeds the window after compaction`.

If a provider rejects a request as too long (`prompt is too long`, `context_length_exceeded`, `maximum context length`), tinyagent compacts and retries that step once; a second rejection ends the run as `Error`. This works even for models whose window isn't known.

The window comes from a small built-in table (`internal/model/context.go`) or `context_window` in the config, which wins. Without either, only the retry-on-error path runs. Each compaction prints one line to stderr, such as `compacted context: ~182,000 → ~31,000 tokens (summarized 41 messages)`. The summary request's tokens and cost count toward the run's totals and `--cost-limit`, but not `--step-limit`. With `-o`, the trajectory keeps every original message, and `compactions` records each one: the step, the estimates before and after, the stage, the summary, and which messages the model was sent next.

Summaries lose detail. Put instructions that must hold for the whole run in `AGENTS.md`, which is part of the system prompt and never compacted. `--no-compact` turns all of this off.

## Exit status

The run ends when the model writes `COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT` (`Submitted`), stops calling tools (`Completed`), or hits a limit (`StepLimitExceeded`, `CostLimitExceeded`, `TimeLimitExceeded`), Ctrl-C (`Interrupted`), a non-retryable API error (`Error`), too many consecutive malformed tool-call steps (`RepeatedFormatError`, see below), or, with `--output-schema`, 3 consecutive invalid results (`InvalidOutput`). The process exits 0 only for `Submitted` and `Completed`.

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
- **Not covered:** the in-process file tools (`read_file`, `write_file`, ... and their symlink behavior), MCP servers, and provider requests. Commands can still read anything you can, including credentials, and inherit your environment variables (API keys included). The sandbox reduces risk; it does not make untrusted code safe.

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
- Path sandboxing is lexical: `..` and absolute paths outside `--cwd` are rejected, but a symlink inside the root that points outside it is followed. `bash` isn't sandboxed unless you pass `--sandbox` (see Sandboxing commands); without it, only run it on code you trust.
- Cost uses a small built-in price table (`internal/model/provider.go`). Unknown models cost $0 unless you set the price in the config, so `--cost-limit` does nothing for them. With a positive cost limit, tinyagent prints a warning on stderr before the first request when the `openai`, `openrouter` or `anthropic` model has no price, naming `input_price_per_mtok` and `output_price_per_mtok`. The run continues as normal.
- The `anthropic` provider turns on [prompt caching](https://platform.claude.com/docs/en/build-with-claude/prompt-caching) for every request, so each step reads the history the previous step already sent from the cache instead of paying full price for it again. Input tokens in the summary and trajectory include cached tokens. Cost prices cache writes at 1.25× the input price and cache reads at 0.1× (less on models where Anthropic charges less, such as 0.05× for Claude Opus 5.5 and Sonnet 5.5). `input_price_per_mtok` overrides the base price and the multipliers apply on top.
