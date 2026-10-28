# tinyagent

```bash
curl -fsSL https://raw.githubusercontent.com/vicgupta/tinyagent/main/install.sh | sh
```

A minimal coding agent in a single static Go binary. It gives an LLM nine tools (`read_file`, `write_file`, `edit_file`, `list_files`, `grep`, `bash`, and `start_process`, `read_process`, `stop_process` for background commands), plus the opt-in `update_plan`, `skill`, `view_image`, `apply_patch`, `delegate`, `web_fetch`, `lsp`, and MCP resource (`list_mcp_resources`, `read_mcp_resource`) tools, and keeps a linear message history until the model submits.

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

### Shell completion (Bash, Zsh and Fish)

`tinyagent --completion bash`, `tinyagent --completion zsh` and `tinyagent --completion fish` print a script that completes flag names (`--sandbox-<Tab>`, `--tool-out<Tab>`, short aliases such as `-t`). Only flag names are completed, not models, profiles or other flag values.

**Bash.** Load it for the current session:

```bash
source <(tinyagent --completion bash)
```

To keep it, add that line to `~/.bashrc`.

**Zsh.** The script registers itself with Zsh's native completion system (`compdef`, no `bashcompinit`), so it must be sourced after `compinit`. For the current session, or to keep it, put this in `~/.zshrc`:

```zsh
autoload -Uz compinit
compinit
source <(tinyagent --completion zsh)
```

If your framework (Oh My Zsh, Prezto and the like) already runs `compinit`, add only the last line, after the framework is loaded. Sourcing it before `compinit` prints an error and changes nothing.

**Fish.** Load it for the current session:

```fish
tinyagent --completion fish | source
```

For automatic loading in new sessions, save the script:

```fish
mkdir -p ~/.config/fish/completions
tinyagent --completion fish > ~/.config/fish/completions/tinyagent.fish
```

Other shell names exit 2. In all three shells, words that don't start with `-` (and flag prefixes that match no flag) keep the shell's normal filename completion, and pressing Tab never runs tinyagent: the flag list is fixed when the script is generated, so re-source it after `--update` (or open a new shell); for Fish automatic loading, regenerate the saved file after updating. The command needs no task, credentials or config.

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
| `-t, --task` | — | Task prompt (required unless `--resume`; with `--resume` of a finished run, the follow-up task; of a `NeedsInput` run, the answer); `-` reads it from stdin |
| `--resume` | — | Continue a validated unfinished trajectory; requires explicit matching `--cwd` and a new `-o` path |
| `--ask-user` | config `ask_user.enabled`, else off | Give the model the `ask_user` tool: it may end the run with one question (status `NeedsInput`, exit 4), answered with `--resume <trajectory> -t "<answer>"`. Requires `-o`; can't be combined with `--output-schema`; `--ask-user=false` disables it. See [Asking for input](#asking-for-input) |
| `--acp` | off | Serve the [Agent Client Protocol](#editor-integration-acp) on stdin/stdout so editors such as Zed and JetBrains IDEs can run tinyagent as an external agent (experimental). Can't be combined with `-t`, `--resume`, `--rewind`, `--transcript`, review flags, `--worktree`, `--output-schema`, `--json`, `--ask-user`, `--check`, `--init-config`, `--completion`, `-o`, `--cwd`, `--image` or `--snapshots` (exit 2) |
| `-m, --model` | `$AGENT_MODEL`, else config `model`, else `openai/gpt-4o` | `<provider>/<model>`; no `/` means openai. Providers: openai, openrouter, anthropic, ollama, codex, plus any defined under [`providers`](#named-providers) |
| `--reasoning-effort` | config `reasoning_effort`, else unset | Reasoning effort sent to the provider: `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, `max`; see [Reasoning](#reasoning) |
| `--fallback-model <spec>` | config `fallback_models`, else none | Model to switch to when the current one keeps failing with `429`, `5xx` or network errors after retries; repeatable, tried in order, at most 3, and replaces `fallback_models`; see [Model fallback](#model-fallback) |
| `--no-fallback` | off | Don't fall back to other models (clears `fallback_models`); conflicts with `--fallback-model` |
| `--openai-api` | config `openai_api`, else `auto` | Transport for the `openai` provider: `auto`, `responses`, or `chat`; see [OpenAI transport](#openai-transport) |
| `--init-config` | | Write a commented starter config to the user config path (or `-c PATH`), refuse overwrites, and exit |
| `-c, --config` | user config, else built-in | YAML config file; see [Config](#config) |
| `--profile` | none | Apply one named profile from the config file's `profiles:` section over its base settings; see [Profiles](#profiles) |
| `--list-profiles` | | Print the profile names in the selected config file (`-c`) or the user config, sorted, one per line, then exit. Makes no model request and needs no credentials; see [Profiles](#profiles) |
| `--cwd` | `.` | Root directory; tools can't reach outside it |
| `--memory` | off | Load this project's active [project memory](#project-memory) entries into the system prompt (overrides `memory.enabled`; `--memory=false` turns it off). `--resume` keeps the checkpoint's setting; a conflicting `--memory` exits 2 |
| `--memory-list` | | Print the [project memory](#project-memory) entries for `--cwd` (pending first, then newest first), or a JSON array with `--json`, then exit. Makes no model request and needs no credentials |
| `--memory-approve <id\|all>` | | Approve one pending project memory entry, or every pending entry, then exit |
| `--memory-forget <id\|all>` | | Delete one project memory entry; `all` deletes every entry and requires `--memory-yes` |
| `--memory-path` | | Print the project memory store path for `--cwd`, then exit |
| `--review` | off | Review the uncommitted changes (staged, unstaged and untracked) against `HEAD`: tinyagent collects the diff and runs a read-only review; `-t` adds focus instructions. See [Reviewing changes](#reviewing-changes) |
| `--review-base <ref>` | — | Review what the current branch adds on top of `<ref>` (`merge-base..HEAD`), read-only. See [Reviewing changes](#reviewing-changes) |
| `--review-commit <rev>` | — | Review the changes introduced by one commit, read-only. See [Reviewing changes](#reviewing-changes) |
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
| `--lockdown` | config `untrusted_content.enabled`, else off | Untrusted-content lockdown: once the run reads untrusted content (a `web_fetch` or MCP result, a review diff, `--untrusted-task`), commands and MCP tools are refused, and `web_fetch` outside `untrusted_content.lockdown.web_fetch_allowed_domains`; `--lockdown=false` turns it off. See [Untrusted-content lockdown](#untrusted-content-lockdown) |
| `--untrusted-task` | off | Treat the task text as untrusted (an issue body, a ticket): the run starts tainted. Requires lockdown (`--lockdown` or `untrusted_content.enabled`) and doesn't turn it on; without it, exit 2 |
| `--tool-output-files` | config `tool_output_files.enabled`, else off | Save the full text of tool results over `max_output_chars` to a per-run file the model can page with `read_file` and `grep`; `--tool-output-files=false` disables it. See [Tool output files](#tool-output-files) |
| `--images` | config `images.enabled`, else off | Let vision-capable models see images: the `view_image` tool and MCP image pass-through; see [Images](#images) |
| `--image <path>` | — | Attach a PNG, JPEG, GIF or WebP file to the task message (or a `--resume -t` follow-up). Repeatable, up to 10, each within `max_image_bytes`; needs `-t`; independent of `--images`. See [Attaching images to the task](#attaching-images-to-the-task) |
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
| `--redact-secrets` | config `redact_secrets.enabled`, else off | Mask known secret values as `[REDACTED]` in `bash`, process, MCP, `web_fetch` and hook output before the model sees it; `--redact-secrets=false` turns the config setting off. See [Redacting secrets from tool output](#redacting-secrets-from-tool-output) |
| `-o, --output` | — | Trajectory JSON, rewritten after every step |
| `--snapshots` | config `snapshots`, else off | Snapshot the workspace before every tool batch that can change files, so `--rewind` can restore it. Requires `-o`. See [Rewind workspace changes](#rewind-workspace-changes) |
| `--rewind <trajectory>` | | Restore `--cwd` to a snapshot recorded in that trajectory, with `--to start\|<step>\|<snapshot-id>` (default `start`) and `--dry-run` to preview. Makes no model request |
| `--transcript <trajectory>` | | Print that trajectory as a redacted Markdown transcript on stdout and exit. Makes no model request and needs no API key; see [View a run as a transcript](#view-a-run-as-a-transcript) |
| `-v, --verbose` | off | Print tool calls and their outputs |
| `--json` | off | Write machine-readable run events as JSONL to stdout |
| `--output-schema` | | End the run with a JSON result validated against this JSON Schema file (printed to stdout) |
| `--log-db[=path]` | off | Also record run events in a local SQLite database (default `<cwd>/.tinyagent/tinyagent.db`); see [Event log database](#event-log-database) |
| `--otel` | config `otel.enabled`, else off | Export the run as an OpenTelemetry trace over OTLP/HTTP; `--otel=false` turns the config setting off. `OTEL_*` variables alone never enable it. See [OpenTelemetry export](#opentelemetry-export) |
| `--no-stream` | off | Wait for each full response instead of streaming it |
| `--no-agents-md` | off | Don't append the nearest `AGENTS.md` (at or above `<cwd>`, up to the repo root; capped at 32KB) to the system prompt |
| `--scoped-agents-md` | off | Also deliver `AGENTS.md` files in directories below `<cwd>`, appended to the first file tool result that touches a path there; see [Project instructions](#project-instructions-agentsmd). Can't be combined with `--no-agents-md` |
| `-V, --version` | | Print the version and exit |
| `--update` | | Replace this binary with the latest GitHub release (checksum-verified) and exit |
| `--check` | | Print the resolved config file, model, provider key status, fallbacks, cwd and limits (and `ask_user` when it is on or set by flag, and the [project memory](#project-memory) approval policy, store path and active/pending entry counts when memory is on or `--memory` is passed, and a `lockdown:` line when [lockdown](#untrusted-content-lockdown) is on or `--lockdown` is passed), then exit without a model request (exit 2 on a setup error) |
| `--completion bash\|zsh\|fish` | | Print a Bash, Zsh or Fish flag-name completion script and exit (`source <(tinyagent --completion bash)`; for Zsh, after `compinit`; for Fish, `tinyagent --completion fish \| source`); see [Shell completion](#shell-completion-bash-zsh-and-fish) |

### Machine-readable output

`--json` makes stdout a UTF-8 JSONL event stream, with one complete JSON object and newline per event. It can be combined with `--no-stream`, `--read-only`, `--resume`, and `-o` (which still writes the trajectory JSON to its file). In this mode, streamed assistant text and live bash output are represented by events, and `--verbose` has no effect because tool calls and results are already events; the human summary and warnings remain on stderr. Setup failures before a run starts may leave stdout empty.

Every event has a `type`. Event names and fields in schema version 1 are:

| Type | Fields |
|---|---|
| `run.started` | `schema_version` (currently `1`), `model`, canonical `root`, `read_only`, `resumed`, `follow_up` (`true`, only for a follow-up run), `attachments` (`index`, `name`, `mime_type`, `bytes`, `sha256` per [`--image`](#attaching-images-to-the-task) file, never the data; only with `--image`), `reasoning_effort` (only when set), `fallback_models` (only when a [fallback chain](#model-fallback) is configured), `openai_api` (the resolved `responses` or `chat`, only for the `openai` provider), `profile` (the name only; only with [`--profile`](#profiles)), `command_env` (`inherit`, `exclude_secrets`, sorted `passed` names; only with a [command environment](#command-environment) policy), `redact_secrets` (`enabled`, `patterns`, sorted variable `names`; only with [secret redaction](#redacting-secrets-from-tool-output) on), `file_access` (`deny_read`, `deny_write`; only with [file access](#restricting-file-access) rules), `web_fetch` (`allowed_domains`; only with [web fetch](#web-fetch) on), `lsp` (`servers`, the configured names; only with [language servers](#language-servers-lsp) on), `scoped_agents_md_enabled` and `scoped_agents_md` (scoped files already in context, `[]` for a fresh run; only with [`--scoped-agents-md`](#project-instructions-agentsmd) on), `additional_dirs` (`path`, `mode`; only with [additional directories](#additional-directories)), `worktree` (`path`, `branch`, `base_commit`, `repo_root`; only with [`--worktree`](#worktrees)), `review` (`target`, `ref`, `base_commit`, `head_commit`, `commit`, `files_changed`, `diff_bytes`, `diff_bytes_sent`, `truncated`, `hidden_paths`; only with a [review flag](#reviewing-changes)), `otel` (`enabled`, `endpoint_host`, `protocol`, `trace_id`; only with [`--otel`](#opentelemetry-export), never headers), `lockdown` (`enabled`, `sources`, `web_fetch_allowed_domains`, `tainted`; only with [lockdown](#untrusted-content-lockdown) on) |
| `model.request` | `step` (the step the request belongs to), `model` (the provider/model that served it, after any fallback), `duration_ms`, and either `input_tokens`, `output_tokens`, `cache_read_tokens`, `cache_write_tokens` or `error`; one per model request of the agent loop (compaction summaries excluded), emitted before that response's `assistant.message` |
| `assistant.message` | `text` (complete assistant message) |
| `tool.call` | `id`, `name`, `arguments` (decoded JSON value, or a string if malformed) |
| `tool.result` | `id`, `name`, `output` (same configured truncation as tool output), `is_error`; with [tool output files](#tool-output-files), a saved result also has `output_file` (path relative to `--cwd`) and `output_bytes`; with [secret redaction](#redacting-secrets-from-tool-output), `redactions` (count, only when > 0) |
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
| `memory.loaded` | `path` (the store file), `loaded` (entry count), `ids` (loaded entry ids, newest first), `omitted` (active entries left out to stay within `memory.max_bytes`); once after `run.started` for a fresh run with [project memory](#project-memory) on. A resumed or follow-up run of a checkpoint that had memory on emits it too, describing the replayed checkpoint snapshot (the store is not re-read), with `from_checkpoint: true`. Never includes entry text |
| `memory.saved` | `op` (`add`, `replace` or `forget`), `id`, `status` (`active`, `pending` or `forgotten`), `redacted`, `untrusted_context`; one per successful `remember` write, emitted before its `tool.result`. Never includes entry text |
| `lsp.server` | `name`, `state` (`started`, `unavailable`, `crashed` or `stopped`), `reason` (for `unavailable` and `crashed`); one per language-server state change (see [Language servers (LSP)](#language-servers-lsp)) |
| `instructions.loaded` | `path`, `scope_dir` (both relative to `--cwd`), `bytes` (file size), `truncated`, `tool`, `tool_call_id`, `step`; a scoped `AGENTS.md` was appended to that call's result, emitted before its `tool.result` (only with [`--scoped-agents-md`](#project-instructions-agentsmd)) |
| `instructions.skipped` | `path`, `scope_dir`, `reason` (`file_access`, `outside_root`, `modified`, `read_error` or `cap`), `tool`, `tool_call_id`, `step`, and `error` for `read_error`; a scoped `AGENTS.md` that applied was not delivered |
| `provider.retry` | `attempt` (the upcoming attempt number), `reason` (`http_<status>` or `network`), `backoff_ms`; only emitted when a model request is retried |
| `model.fallback` | `step`, `from`, `to`, `reason` (`http_<status>` or `network`), `attempts`; the run switched to the next [fallback model](#model-fallback) after `from` used up its retries |
| `taint` | `source` (`web_fetch`, `mcp`, `review`, `untrusted_task` or `resume`), `step`, and `tool_call_id`, `tool`, `host` (the `web_fetch` host only) and `resumed` when they apply; one per new taint source of a run with [lockdown](#untrusted-content-lockdown) on. Never includes content or URLs |
| `lockdown.denied` | `channel` (`command`, `web_fetch` or `mcp`), `tool`, `tool_call_id`, `step`, and `host` for `web_fetch`; one per call lockdown refused after taint |
| `result` | `status`, `submission` (the question for `NeedsInput`), cumulative `steps`, `structured_output` (only with `--output-schema` and status `Submitted`), `question` (`tool_call_id`, `question`, `options` when given; only for status `NeedsInput`, see [Asking for input](#asking-for-input)), `input_tokens`, `output_tokens`, `cache_read_tokens`, `cache_write_tokens`, `cost_usd`, invocation `duration_ms`, and `tainted` and `lockdown_denials` (a count; only with [lockdown](#untrusted-content-lockdown) on) |

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
tinyagent --review-base main --output-schema review.schema.json > review.json
```

The schema file (at most 64 KiB) must be a JSON object with `"type": "object"` at the root and is checked before any provider call; a bad schema exits 2. The model is given a `submit_result` tool whose parameters are the schema, and the run finishes only when it calls that tool, alone in its message, with arguments that validate. Invalid arguments, mixing `submit_result` with other tool calls, the completion marker, or a turn with no tool call are answered with the validation errors or a reminder; after 3 consecutive invalid attempts the run ends with status `InvalidOutput`. Hooks never run for `submit_result`.

On success the status is `Submitted`, stdout contains only the compact JSON object and a newline, and the summary, streamed text, and `-v` output go to stderr. With `--json`, the object is the `structured_output` field of the `result` event instead. Any other outcome exits 1 with nothing on stdout. Trajectories (`-o`) record `structured_output`, `output_schema`, and `output_schema_digest`; `submission` holds the compact JSON string. Runs started with `--output-schema` cannot be resumed, and `--resume` with `--output-schema` exits 2.

Supported keywords (the same subset as MCP tool schemas): `type`, `properties`, `required`, `additionalProperties: false`, `items`, `enum`, `const`, `minimum`, `maximum`, `exclusiveMinimum`, `exclusiveMaximum`, `multipleOf`, `minLength`, `maxLength`, `pattern` (Go regexp syntax), `minItems`, `maxItems`, `uniqueItems`, `minProperties`, `maxProperties`. `format` is accepted but not enforced. Providers also see the schema, but tinyagent validates locally, so results never depend on provider-side strict modes.
### Event log database

`--log-db` (or `log_db.enabled: true` in config) records every event the `--json` stream would contain in a local SQLite file, whether or not `--json` is also on. It is off by default; nothing is created unless enabled. The database lives at `<cwd>/.tinyagent/tinyagent.db` unless `--log-db=path` or `log_db.path` says otherwise (a flag path is relative to the shell's directory, a config path to `--cwd`; `~/` is expanded). Add `.tinyagent/` to your `.gitignore`. The file is created with mode 0600, uses WAL mode with a busy timeout so concurrent runs can share it, and needs no cgo (pure-Go `modernc.org/sqlite`).

Tables: `runs` (one row per invocation: model, provider, cwd, status, steps, token and cost totals, duration, version), `events` (`run_id`, `seq`, `step`, `ts`, `type`, JSON `payload`), `tool_calls` (tool name, arguments, result summary, duration, `is_error`), `subagents` (one row per [delegate](#subagents) or [worker](#workers) subagent: `kind`, `subagent_id`, `tool_call_id`, `parent_step`, `label`, `model`, `status`, `steps`, token and cost totals, `started_at`, `ended_at`, `duration_ms`; workers also record `branch`, `base_commit`, `commit_sha` (empty when the worker changed nothing), `files_changed` and `applied`, set to `1` when `apply_worker` applies the change), and `schema_migrations`. Timestamps are Unix milliseconds.

```sh
sqlite3 .tinyagent/tinyagent.db \
  "SELECT tool, count(*), avg(duration_ms) FROM tool_calls GROUP BY tool"
```

At the default `level: metadata` no prompt, message, argument, or output bodies are stored: those fields are replaced with their lengths, and the task is stored only as a SHA-256 hash and length. `level: full` stores bodies, after redacting values of environment variables named `*_KEY`, `*_TOKEN`, `*_SECRET`, or `*_PASSWORD`, secrets set in MCP or hook `env`, and common token shapes; payloads over `max_payload_bytes` are truncated. Old runs are pruned when a run starts (`max_days`, `max_runs`; `0` keeps everything). Database errors never fail a run: tinyagent prints one warning and continues without the log. The trajectory JSON (`-o`) is unchanged and remains the resume format.

### OpenTelemetry export

`--otel` (or `otel.enabled: true` in config) sends each invocation to an OpenTelemetry backend (an OpenTelemetry Collector, Jaeger, Grafana Tempo, Honeycomb, Datadog, ...) as one trace over OTLP/HTTP. It is off by default, and the standard `OTEL_*` variables alone never turn it on, so a CI job that sets them for other tools exports nothing until you opt in. Spans are built from the same events as `--json` and `--log-db`, so the three always agree.

| Span | Kind | Attributes |
|---|---|---|
| `invoke_agent tinyagent` (root) | INTERNAL | `gen_ai.operation.name=invoke_agent`, `gen_ai.agent.name`, `gen_ai.request.model`, `gen_ai.provider.name`, `tinyagent.model`, `tinyagent.status`, `tinyagent.steps`, `gen_ai.usage.input_tokens`/`output_tokens`/`cache_read.input_tokens`/`cache_creation.input_tokens`, `tinyagent.cost_usd`, `tinyagent.duration_ms` (the `result` event's values), `tinyagent.resumed`, `tinyagent.read_only`, `tinyagent.version` |
| `chat {model}` | CLIENT | `gen_ai.operation.name=chat`, `gen_ai.request.model`, `gen_ai.response.model`, `gen_ai.provider.name`, per-request token usage, `tinyagent.step`; `provider.retry` and `model.fallback` span events; `error.type=model_error` for a failed request |
| `execute_tool {name}` | INTERNAL | `gen_ai.operation.name=execute_tool`, `gen_ai.tool.name`, `gen_ai.tool.call.id` (the `tool.call` `id`), `tinyagent.output_bytes`, `tinyagent.redactions`, `error.type=tool_error` when the result is an error |
| `hook {event}` | INTERNAL | `tinyagent.hook.event`, `tinyagent.hook.decision`, `gen_ai.tool.call.id`; a child of its tool's span (`stop` hooks: of the root) |
| `invoke_agent delegate` | INTERNAL | a [subagent](#subagents), child of its `execute_tool delegate` span; the subagent's model and tool spans nest under it |
| `invoke_agent worker` | INTERNAL | a [worker](#workers), child of its `execute_tool worker` span (concurrent workers each get their own); `tinyagent.subagent.id`, `tinyagent.subagent.kind=worker`, `tinyagent.worker.branch`, `tinyagent.worker.base_commit`, and when it finishes `tinyagent.worker.commit` (only if it changed files) and `tinyagent.worker.files_changed`; the worktree path is not exported. `apply_worker` adds a `worker.applied` span event to its tool span |

Other events (`compaction`, `checkpoint.saved`, `plan.updated`, `sandbox.network_denied`, `web_fetch`, `warning`, `error`, ...) become span events on the open tool span or the agent span, with the fields `--log-db` stores at its default level. The root span's status is ERROR for every final status except `Submitted`, `Completed` and `NeedsInput`. Each invocation (including `--resume` and follow-ups, and each [ACP](#editor-integration-acp) prompt) is its own trace. When `TRACEPARENT` (and `TRACESTATE`) holds a valid W3C trace context, the root span becomes its child, so the run shows up inside your CI pipeline's trace; a malformed value is ignored with one warning.

**Content is off by default**: no task, assistant text, tool arguments or tool output is exported, only the metadata above (span-event body fields become `<field>_len`). `otel.content: true` adds `tinyagent.task`, `tinyagent.assistant.text`, `gen_ai.tool.call.arguments` and `gen_ai.tool.call.result`, each first redacted like the event log (secret-named environment variables, configured secrets, the export headers and token shapes become `[REDACTED]`) and cut to `max_attribute_bytes`.

```yaml
otel:
  enabled: false            # or --otel
  endpoint: ""              # OTLP/HTTP base URL; /v1/traces is appended
  protocol: http/protobuf   # or http/json; grpc is not supported (exit 2)
  headers_env: []           # names of env vars holding k=v,k2=v2 header lists
  timeout: 10s              # bound on each export, retries included, and on the final flush
  service_name: tinyagent   # resource service.name
  content: false            # add redacted prompt, assistant, argument and output text
  max_attribute_bytes: 4096 # cap per content attribute
```

Empty settings fall back to the standard variables: the endpoint to `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` (used as is), then `OTEL_EXPORTER_OTLP_ENDPOINT` (with `/v1/traces` appended), then `http://localhost:4318/v1/traces`; the protocol to `OTEL_EXPORTER_OTLP_TRACES_PROTOCOL`, then `OTEL_EXPORTER_OTLP_PROTOCOL`; the timeout to `OTEL_EXPORTER_OTLP_TRACES_TIMEOUT`, then `OTEL_EXPORTER_OTLP_TIMEOUT` (milliseconds); the service name to `OTEL_SERVICE_NAME`, then `service.name` in `OTEL_RESOURCE_ATTRIBUTES` (whose other entries become resource attributes). Headers come from `OTEL_EXPORTER_OTLP_HEADERS`, `OTEL_EXPORTER_OTLP_TRACES_HEADERS` and the `headers_env` variables, never from the config file, and their values are never printed or recorded. Bad settings (unknown keys, an invalid `timeout`, `grpc`, a malformed header list, an unset `headers_env` variable) exit 2 before any model request. `-v` prints the endpoint host and protocol.

Spans are buffered in memory and sent in batches, at the latest when the run ends, including after Ctrl-C. `429`, `502`, `503`, `504` and network errors are retried with jittered exponential back-off (honoring `Retry-After`) within `timeout`. Export problems never change the exit code, stdout, the `result` event or the trajectory: they produce exactly one `warning` (stderr, and a `warning` event with `--json`) giving the number of spans not delivered, and the run waits at most `timeout` at exit. The exporter runs in tinyagent's own process, so `--sandbox` network rules don't apply to it. Use `https` for any endpoint that is not on localhost.

To try it locally, run an OpenTelemetry Collector (or Jaeger, which accepts OTLP) on port 4318 and point tinyagent at it:

```sh
docker run --rm -p 16686:16686 -p 4318:4318 jaegertracing/all-in-one
tinyagent --otel -t "fix the failing test"     # then open http://localhost:16686
```

Span and attribute names follow the OpenTelemetry GenAI semantic conventions, which are still in Development status; names may change in a later release to follow them.

### Reviewing changes

`--review`, `--review-base <ref>` and `--review-commit <rev>` review one Git change set without giving the model a shell. tinyagent runs fixed, read-only `git` commands itself, puts the diff in the first user message with built-in review instructions, and runs the model with the [read-only](#flags) tool set (plus any of `skill`, `web_fetch`, `lsp` and `delegate` you turned on) so it can read the surrounding code. The model reports findings ordered by severity, each with `file:line`, a severity and a short failure scenario. Nothing in the working tree or index changes.

```sh
tinyagent --review                                  # staged + unstaged + untracked vs HEAD
tinyagent --review-base main                        # merge-base(main, HEAD)..HEAD
tinyagent --review-commit HEAD~1                    # changes introduced by one commit
tinyagent --review -t "focus on error handling in pkg/api"
cat focus.md | tinyagent --review-base origin/main -t -
tinyagent --review-base origin/main --output-schema findings.schema.json > findings.json
```

- **Targets.** `--review` covers tracked changes against `HEAD` (staged and unstaged together) and untracked files that aren't ignored, shown as new files. `--review-base <ref>` covers the commits on `HEAD` since its merge base with `<ref>`; uncommitted changes are left out, with a note on stderr when there are some. `--review-commit <rev>` covers one commit against its first parent (a root commit against the empty tree), with its subject and author. Only one target per run.
- **Scope.** Only changes under `--cwd` are included, with paths relative to it, matching what the file tools can read. Binary files show as `binary file changed`. Paths hidden by `file_access.deny_read` are left out; the prompt, stderr and `review.hidden_paths` give only their count.
- **Size cap.** The diff text is capped at 200,000 bytes. Over the cap every changed file is still listed with its `+`/`-` counts, whole per-file diffs are included in order while they fit, the others are named so the model can read them with `read_file`, and stderr and `review.truncated` report it.
- **Safety.** Refs come only from the command line (a ref starting with `-` is rejected). Git runs with `--no-ext-diff`, `--no-textconv`, `core.fsmonitor=false`, every configured filter driver turned off and `GIT_OPTIONAL_LOCKS=0`, so reviewing an untrusted repository doesn't start programs its config names and doesn't write the index.
- **Privacy.** The diff, including untracked files that aren't ignored, is sent to the selected provider.
- **Errors and exit status.** `git` missing, `--cwd` outside a work tree, an unknown ref, no commits or no merge base exit 2 before any model request. A clean change set prints `nothing to review` and exits 0 without a request. Otherwise the exit status means the review ran, as for any run; it doesn't encode findings. Two review flags, or a review flag with `--read-only=false`, `--resume`, `--rewind`, `--worktree`, `--snapshots`, `--check`, `--init-config` or `--update`, exit 2.
- **Everything else as usual.** The system prompt and AGENTS.md, limits, `--json`, `-o`, `--log-db`, `--output-schema`, profiles and stop hooks behave as for other runs. `run.started`, trajectories and the event log record a `review` object (target, ref, resolved commits, `files_changed`, `diff_bytes`, `diff_bytes_sent`, `truncated`, `hidden_paths`), and `-v` prints one `review:` line. A review run saved with `-o` can be continued with `--resume review.json -t "<follow-up>"` (without a review flag) as an ordinary follow-up; it keeps the recorded read-only mode, so use it for questions about the findings, and start a new run to fix them.

A minimal findings schema for CI:

```json
{"type": "object", "required": ["findings"], "properties": {"findings": {"type": "array", "items": {"type": "object", "required": ["severity", "location", "summary"], "properties": {"severity": {"enum": ["critical", "high", "medium", "low"]}, "location": {"type": "string"}, "summary": {"type": "string"}, "scenario": {"type": "string"}}}}}}
```

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

### View a run as a transcript

`--transcript <trajectory>` renders a saved `-o` file as Markdown on stdout, so you can see what an unattended run read, ran and changed, and why, without reading the raw JSON. It reads no config, needs no API key, makes no model or network request and writes no file.

```sh
tinyagent --transcript run.json | less
tinyagent --transcript run.json > run.md
```

**What it shows.** A header with the exit status, model (and the model serving at the end, after a fallback), profile, steps, token totals (cache included), cost, and the read-only, sandbox, environment, worktree and review settings. Then the task, and a `### Step N` section per step, numbered as in `steps`: the assistant's text, each tool call with its arguments pretty-printed, and each result in a collapsed `<details>` block (marked when it was a format-error correction). Tool calls without a result, as in an interrupted checkpoint, are listed as such. Plan updates (with the final plan as a checklist), compactions, model switches, subagent delegations and `--resume`/follow-up boundaries appear where they happened. Every message is shown, including ones compaction removed from the model's view. The footer has the submission (or the `--output-schema` result as JSON) and the snapshot ids for `--rewind`. The system prompt is left out.

**Safety.** All text that came from the task, the model, tools, MCP servers or hooks is written inside fenced code blocks (or inline code) longer than any backtick run it contains, so it can't render as an image, link or HTML wherever the Markdown is shown. Images are never inlined: each is a line like `[image: image/png, shot.png, 48,213 bytes]`. Secrets are masked as `[REDACTED]` with the same redactor as the event log: values of environment variables named `*_KEY`, `*_TOKEN`, `*_SECRET` or `*_PASSWORD` in the environment you run `--transcript` in, and `sk-…`, `ghp_…`, `Bearer …` and `xox…-` shaped tokens. Redaction is best-effort: review a transcript before posting it publicly.

**Size.** Each tool result, tool call's arguments, user message (the task included), plan diff and compaction summary is cut to its first 2,000 characters, with a `[... N characters omitted ...]` notice. Assistant text and the submission are shown whole.

**In CI.** Append it to the GitHub Actions job summary (the [GitHub Action](#github-actions) does this with `job-summary: true`). A job summary is limited to 1 MiB per step and fails to upload above that, so very long runs may not fit:

```yaml
- run: tinyagent --profile ci -t "fix the failing test" -o run.json
- if: always()
  run: tinyagent --transcript run.json >> "$GITHUB_STEP_SUMMARY"
```

Any trajectory tinyagent writes renders, finished or not, including old files and ones `--resume` would refuse; fields it doesn't know are ignored. A missing, non-regular, empty or over-64-MiB file, invalid JSON or an unsupported `schema_version` exits 2 with one error line and nothing on stdout. `--transcript` can't be combined with `-t`, `--resume`, `--rewind`, `--check`, `--json`, `-o` or the other mode flags (exit 2).

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
- A `NeedsInput` run is answered the same way, but the answer is delivered differently; see [Asking for input](#asking-for-input).

### Asking for input

`--ask-user` (or `ask_user.enabled: true`) gives the model an `ask_user` tool for headless runs. When information that blocks the task is missing (which of two APIs to change, whether a breaking change is OK), the model can call it with one question instead of guessing. The run then ends cleanly with status `NeedsInput` and exit code 4; nothing waits on a terminal, so nothing can hang in CI. Whoever started the run answers later with `--resume`:

```sh
tinyagent --ask-user -t "add rate limiting to the API" -o run.json
# stderr:
# NeedsInput after 6 steps in 41s (...): Should the limit be per API key or per client IP?
#   options: per API key | per client IP
# answer with: tinyagent --resume run.json --cwd /repo -t "<answer>" -o run-2.json

tinyagent --resume run.json --cwd . -t "per API key" -o run-2.json
```

- **The tool.** `ask_user` takes a `question` (required, non-empty, at most 2,000 characters) and optional `options`: 2 to 6 distinct suggested answers of at most 200 characters each. The person may answer with any text. A short notice in the system prompt tells the model to ask only when it is blocked and the answer isn't in the workspace, and otherwise to proceed and state its assumptions.
- **Ending the run.** A valid `ask_user` call that is the only tool call in its message ends the run. The call is saved in the trajectory as pending (`pending_calls`), with the question in `pending_question` (`tool_call_id`, `question`, `options`). The question goes to stdout (so `q=$(tinyagent ...)` captures it) and, with the options and a ready-to-paste `--resume` command, to stderr. With `--json`, the final `result` event has `status: "NeedsInput"`, `submission` set to the question, and a `question` object; `--log-db` stores the same, and at level `metadata` it keeps only the question's length. No hooks run for `ask_user`, and stop hooks don't run for a `NeedsInput` end.
- **Answering.** `--resume <trajectory> -t "<answer>"` (or `-t -` for stdin) sends the saved history and delivers the answer as the result of the pending `ask_user` call, after a short notice that the workspace may have changed; no user message is added, so the model sees the answer attached to its own question. Without `-t`, or with an empty answer, `--resume` exits 2 and prints the pending question; `--image` can't be attached to an answer. Every other resume rule applies: the same model, root, system prompt and tools (a conflicting `--ask-user` exits 2), a new `-o`, and cumulative steps, cost and tokens. The boundary is recorded in `resume` with `answer: true`.
- **Misuse.** `ask_user` together with other tool calls in one message runs none of them; each gets an error asking the model to call `ask_user` alone. Malformed arguments (missing or empty `question`, wrong types, too few, too many or duplicate `options`) are [format errors](#malformed-tool-calls). After `ask_user.max_questions` questions (default 3, counted across the whole resume chain and recorded as `questions_asked`), further calls get an error telling the model to proceed on stated assumptions, and the run continues.
- **Where it applies.** It is offered with `--read-only` and in review mode, never to [subagents](#subagents). `--ask-user` without `-o`, or with `--output-schema`, exits 2 before any request, since the question could never be answered. With it off, tools, prompts, trajectories, events and exit codes are unchanged; older tinyagent binaries refuse `NeedsInput` checkpoints rather than misreading them.

The question is model-written text that can quote workspace files or fetched pages; treat it as untrusted if you forward it to other systems.

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

### Project memory

Project memory (experimental, in progress; see [#170](https://github.com/vicgupta/tinyagent/issues/170)) keeps short notes that runs save about a project so later runs don't relearn them. Each project has one store file outside the repository, keyed by the repository's Git common directory, so the main checkout and every `--worktree` share it: `$XDG_STATE_HOME/tinyagent/memory/<project>.json`, else `~/.local/state/tinyagent/memory/` on Linux and `tinyagent/memory/` under the user config directory on macOS and Windows. Entries record the run, model and whether untrusted content was in context, and may be held as *pending* until you approve them; pending entries are never loaded into a prompt. The model saves entries with the `remember` tool when memory is on.

**Loading.** Memory is off by default. With `--memory` (or `memory.enabled: true` in the user config or `-c`; never from the repository), a run loads the project's *active* entries into a "Project memory" system prompt section right after AGENTS.md, newest first, one line each as `- [id, date] text`. The section says the notes come from earlier runs and that AGENTS.md and the task outrank them. Only the newest entries that fit within `memory.max_bytes` (default 8 KiB) are loaded; when entries are left out, the section says how many and tinyagent warns once on stderr. `-v` prints how many entries were loaded and from which file. A missing store loads nothing and creates nothing, so memory on with no entries sends exactly the same prompt as memory off. A corrupt or unreadable store warns once and the run continues without memory. Invalid `memory` settings exit 2.

**Resume.** The trajectory records whether memory was on and the exact entries loaded (`memory_enabled`, `memory`, `memory_omitted`). `--resume` rebuilds the section from that snapshot and never re-reads the store, so approving, forgetting or adding entries doesn't block a resume; passing a `--memory` value that conflicts with the checkpoint exits 2. Checkpoints from before this feature resume with memory off. Delegated subagents and workers don't load project memory.

**ACP sessions.** With memory on, an `--acp` session loads the store for the session's cwd on its first prompt, the same way a fresh run does, and later prompts in the session replay that snapshot like follow-ups, so the session never sees its own writes. The `remember` tool is offered on every prompt unless the session is read-only. One writer serves the whole session, so `max_writes_per_run` counts writes across all of the session's prompts. A session that started client-supplied MCP servers (`acp.client_mcp_servers`) saves every entry as pending, as does any session once a `web_fetch` or MCP result enters its context.

**Trust:** loaded entries are prompt input that can steer the agent, and they go to your model provider like any other prompt. Only approved (active) entries are loaded.

You can already inspect and edit the store for `--cwd` without credentials or a model request:

```sh
tinyagent --memory-path                     # print the store path
tinyagent --memory-list [--json]            # id, status, date, source and text
tinyagent --memory-approve <id|all>         # approve one or every pending entry
tinyagent --memory-forget <id>              # delete one entry (and any pending replacement of it)
tinyagent --memory-forget all --memory-yes  # delete every entry
```

These commands accept only `--cwd` (and `--json` with `--memory-list`); combining them with `-t`, `--resume` or any other run or mode flag exits 2, as does an unknown id. A corrupt store is reported, exits 1 and is left unchanged.

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

### Skill workflows

`--workflow file.yaml` runs a sequence of small skills on the task (`-t`) as one automatic pipeline, for example plan, then generate, then validate, then test. Each stage is a fresh, unattended tinyagent run in `--cwd` that gets the original task and the previous stage's output. As soon as a stage passes its success check the next one starts; there is no pause or approval between stages. Without `--workflow`, tinyagent behaves exactly as before, and `--skills` Agent Skills are unaffected.

**Skill format.** A workflow skill is a Markdown file with three required parts: a heading, an intent line, and a list of inputs. A one-line success check is required too, unless the workflow gives that stage a `check` command.

```markdown
# Skill: plan-tasks
> Intent: Break a spec into small, ordered tasks before writing any code.

**Inputs**
- spec
- repo_path

**Success check:** every task has acceptance criteria and a verification step, in dependency order.
```

Skills live in the workflow's `skills_dir` (default `skills/` next to the workflow file) as `<name>.md` or `<name>/SKILL.md`. Names are lowercase letters, digits, `.`, `_` and `-`. A `SKILL.md` with `name`/`description` frontmatter can use the description as its intent. The whole file (frontmatter removed) is the stage's instructions. These input names are filled automatically: `task` and `spec` (the `-t` task), `repo_path` and `cwd` (the absolute `--cwd`), `previous_output` or the previous skill's name (the previous stage's output). Any other input comes from the stage's `inputs:` map or is marked as not provided.

**Workflow format.**

```yaml
version: 1
skills_dir: skills        # relative to this file
default: implement        # used when routing finds no match (optional)
defaults:                 # apply to every stage
  retries: 1
  step_limit: 50
workflows:
  implement:
    intent: Implement a feature from a spec, then validate and test it.
    stages:
      - skill: plan-tasks
      - skill: generate-code
        step_limit: 100
      - skill: validate-diff
        check: [go, vet, ./...]
      - skill: run-tests
        check: [go, test, -count=1, ./...]
        check_timeout: 600
```

Settings in `defaults` or on a stage: `model`, `profile`, `step_limit`, `cost_limit`, `time_limit`, `retries`, and `check_timeout` (seconds). The defaults are 1 retry (at most 5), 50 steps, a 300 second check timeout, and otherwise your usual config. Flags you pass with `--workflow` (such as `--sandbox` or `-m`) are forwarded to every stage, and stage settings override them. Workflows are sequential and have at most 20 stages. A complete example is in [docs/workflows](docs/workflows).

**Routing.** `--workflow-name name` runs that workflow, or a single skill, directly. Otherwise the router matches the task against each workflow's intent and each skill's intent line. A clear best match runs. A tie or no match falls back to `default`, or exits 2 with an error suggesting `--workflow-name`, before any model call.

**Success checks.** A stage with `check` (an argv list, run in `--cwd` without a shell) passes when the command exits 0. Otherwise the model is told the skill's success check and must end its answer with `SUCCESS CHECK: PASS` or `SUCCESS CHECK: FAIL - <reason>`. The last such line decides, and a missing line fails. A stage run that ends without submitting (for example on a step, cost or time limit) also fails. A failed stage is retried with the failure reason in its prompt. When its retries run out, the workflow stops, prints the stage and reason, and exits 1 without running later stages. Earlier stages are not re-run.

**Output and exit status.** Progress lines go to stderr, and the last stage's output goes to stdout. `-o run.json` writes a workflow record (`"kind": "workflow"`) with the route, status, failed stage and reason, total cost, and one entry per attempt with its input, output, exit status, check result, and paths to that attempt's trajectory and log under `run.json.stages/`. Without `-o`, the per-stage files are temporary. `--json` prints JSONL events `workflow.started`, `stage.started` (with the input), `stage.finished` (input, output and `check`), and `workflow.finished`. Exit status is 0 when every stage passes, 1 when a stage fails, 2 for an invalid skill or workflow file, a routing error or a conflicting flag (all before any model request), and 130 when interrupted. `--workflow` can't be combined with flags for one interactive or resumed run, such as `--resume`, `--review`, `--worktree`, `--eval` or `--ask-user`.

**Trust:** workflow skills are prompt input, and `check` commands run with your permissions, just like a project's AGENTS.md or Agent Skills. Only run workflow files you trust.

## Editor integration (ACP)

`tinyagent --acp` speaks the [Agent Client Protocol](https://agentclientprotocol.com) (protocol version 1, newline-delimited JSON-RPC on stdin/stdout), so any ACP client can run tinyagent as an external agent: you type in the editor's agent panel, tinyagent runs its normal loop in the session's directory, and the editor shows the model's text and each tool call live. The mode is **experimental**: the stop-reason mapping and the MCP default below may change based on feedback.

Zed (`settings.json`):

```json
{
  "agent_servers": {
    "tinyagent": {
      "type": "custom",
      "command": "/usr/local/bin/tinyagent",
      "args": ["--acp", "-m", "anthropic/claude-sonnet-4-5"],
      "env": { "ANTHROPIC_API_KEY": "..." }
    }
  }
}
```

JetBrains IDEs (2025.3+, `~/.jetbrains/acp.json`):

```json
{
  "agent_servers": {
    "tinyagent": {
      "command": "/usr/local/bin/tinyagent",
      "args": ["--acp", "--profile", "editor"],
      "env": {}
    }
  }
}
```

Use the full path to the binary. Pass `-m`, `-c`, `--profile` and any policy flags (`--read-only`, `--sandbox`, `--env`, `--command-env`, `--add-dir-*`, `--plan`, `--apply-patch`, `--subagents`, `--workers`, `--web-fetch`, `--lsp`, `--images`, `--tool-output-files`, `--skills`, `--scoped-agents-md`, `--redact-secrets`, `--log-db`, `-v`) in `args`. They mean the same as in a headless run.

**How it works**

- `session/new` takes the editor's absolute `cwd` as the session's root (`AGENTS.md` discovery runs as for `--cwd`) and checks the config, model and provider key there. A relative or missing directory, an invalid config or a missing API key is returned as a JSON-RPC error with the same message and `hint:` a headless run prints. Key values are never printed. A process can run several sessions.
- The first `session/prompt` is the session's task. Each later prompt continues the same conversation, sending the provider exactly what `--resume <trajectory> -t "<follow-up>"` would. This also works after a turn that was cancelled or hit a limit. A second prompt while one is running in the same session gets a JSON-RPC error.
- Prompt content: text blocks are joined; embedded text resources are included as quoted context naming their URI; resource links become a reference line and are not fetched; images are attached like `--image` (PNG, JPEG, GIF or WebP, at most 10, each within `max_image_bytes`). Audio, binary resources and invalid images get an invalid-params error.
- Streaming: assistant text arrives as `agent_message_chunk` (without the submit marker). Each tool call is a `tool_call` with the model's call id, a short title, a `kind` (`read_file` → `read`; `write_file`/`edit_file`/`apply_patch` → `edit`; `grep`/`list_files` → `search`; `bash`/`start_process` → `execute`; `web_fetch` → `fetch`; others → `other`) and absolute `locations` for file tools. It is followed by `tool_call_update`s to `in_progress` and then `completed` or `failed`, carrying the same result text the model sees, after `max_output_chars` and secret redaction. `update_plan` sends `plan` updates, and each model call sends a `usage_update` when the context window is known.
- Stop reasons: `Submitted`/`Completed` → `end_turn`, `StepLimitExceeded` → `max_turn_requests`, `OutputTruncated` → `max_tokens`, `session/cancel` → `cancelled`. Any other status (`CostLimitExceeded`, `TimeLimitExceeded`, `RepeatedFormatError`, `VerificationFailed`, `Error`) ends with a final message naming it and `end_turn`, and the session stays usable. A provider error a headless run would print a setup `hint:` for is returned as a JSON-RPC error instead.
- `session/cancel` stops the turn the way Ctrl-C does: in-flight requests and commands are cancelled, and background processes and language servers are stopped, as at the end of every turn. Closing stdin cancels running turns, stops MCP servers and exits 0.
- `--step-limit` and `--cost-limit` apply to each prompt (on top of what the session has already used), and `--time-limit` limits each prompt's wall-clock time.
- `--log-db` records each prompt as a run, with an `acp.turn` event naming the session id and turn number. No trajectory files are written.
- stdout carries only ACP messages. Warnings, the per-turn summary and `-v` traces go to stderr, which editors show in their agent logs.

**Client-supplied MCP servers** are off by default: tinyagent starts only the `mcp_servers` from your config, and a session's first reply names any servers the editor offered. Set `acp.client_mcp_servers: true` in the user config or `-c` file to start the editor's stdio servers for that session, with every tool they list exposed as `<name>__<tool>` and their `env` values set (never logged). HTTP and SSE servers are not supported (`mcpCapabilities.http` is `false`). Read-only sessions start none.

**Not supported:** `session/load`, session modes and config options, slash commands, delegating files or commands to the client (`fs/*`, `terminal/*`; tinyagent uses its own tools on disk), audio prompts, `ask_user` (it is turned off in this mode), workspace snapshots and reasoning (`agent_thought_chunk`) streaming. Per-tool approval is available through the [`permissions`](#permissions) policy: a rule with `decision: ask` sends `session/request_permission` and the editor's Approve/Reject buttons answer it.

**Trust model:** the editor controls the prompts and the `cwd`, so it has the same power as whoever runs `tinyagent -t` in that directory. Everything that limits what tinyagent may do (the tool set, `--read-only`, `--sandbox`, `file_access`, `command_env`, hooks and limits) comes from your config and flags, and ACP adds no way to loosen it. Repository content stays untrusted prompt input, as in any run.

## GitHub Actions

`action.yml` at the root of this repo is a composite action. It installs a tinyagent release on a Linux or macOS runner (amd64 or arm64), checks it against the release's `SHA256SUMS`, runs one task or review, and sets the result as step outputs. It adds no agent behavior: every input maps to an existing flag.

```yaml
permissions:
  contents: read
steps:
  - uses: actions/checkout@v4
  - id: agent
    uses: vicgupta/tinyagent@v0.5.0
    env:
      ANTHROPIC_API_KEY: ${{ secrets.ANTHROPIC_API_KEY }}
    with:
      model: anthropic/claude-sonnet-4-5
      task: Fix the failing test in ./internal/parser.
      job-summary: true
```

**Pinning.** Under a `vX.Y.Z` tag the action installs the tinyagent release with the same tag, so `@v0.5.0` runs tinyagent v0.5.0. Under any other ref (a branch or a commit SHA) set `version` (`vX.Y.Z`, or `latest` to opt into the newest release) or `tinyagent-path`, or the step fails. A tag can be moved; for a fixed action pin to the tag's full commit SHA and set `version`: `uses: vicgupta/tinyagent@<sha> # v0.5.0` with `version: v0.5.0`.

**Read-only PR review with a findings schema.** Review flags imply `--read-only`, so the agent can't edit files or run commands:

```yaml
on: pull_request
permissions:
  contents: read
jobs:
  review:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with: { fetch-depth: 0 }
      - id: review
        uses: vicgupta/tinyagent@v0.5.0
        env:
          ANTHROPIC_API_KEY: ${{ secrets.ANTHROPIC_API_KEY }}
        with:
          model: anthropic/claude-sonnet-4-5
          review-base: origin/${{ github.base_ref }}
          output-schema: .github/tinyagent/findings.schema.json
          config: .github/tinyagent/ci.yaml
          job-summary: true
      - run: jq . <<<"$FINDINGS"
        env:
          FINDINGS: ${{ steps.review.outputs.structured-output }}
```

**Inputs.** `task` or `task-file` (one is required unless `review-base` or `review-commit` is set; with a review it adds focus), `model` (`-m`), `profile`, `config` (`-c`), `working-directory` (`--cwd`, default the workspace), `review-base`, `review-commit`, `read-only`, `sandbox`, `lockdown` and `untrusted-task` (`true`/`false`, default `false`; `lockdown: false` passes nothing, so the config decides), `output-schema`, `args` (extra flags, one argv element per line, never shell-evaluated or split), `version`, `tinyagent-path` (use an existing binary instead of downloading), `job-summary` (default `false`) and `fail-on-error` (default `true`). The task reaches tinyagent on stdin (`-t -`) and every input through `env:`, so text like `$(...)` or backticks is never run by a shell.

**Outputs.** `status`, `submission`, `structured-output` (compact JSON, set only for a `Submitted` run with `output-schema`), `steps` and `cost-usd`, all from the final [`result` event](#machine-readable-output); `exit-code` (always set once tinyagent ran); and `trajectory` and `events`, the paths of the `-o` file and the saved `--json` stream under `$RUNNER_TEMP`. They are not uploaded; add an artifact step if you want them. Each value is written to `$GITHUB_OUTPUT` behind a random delimiter, and the step fails instead of setting a value that contains it, so a submission can't forge other outputs. While tinyagent runs, workflow commands (`::add-mask::`, `::error::` ...) in its log output are disabled.

**Failing the step.** A non-zero [exit status](#exit-status) (1, 3 `OutputTruncated`, 4 `NeedsInput`) fails the step unless `fail-on-error: false`, in which case later steps can read `exit-code` and `status`. A usage error (exit 2), a bad input, or an install failure (unsupported runner, missing asset, checksum mismatch) always fails the step, before any model request.

**Job summary.** `job-summary: true` appends the [`--transcript`](#view-a-run-as-a-transcript) rendering to the job summary, also when the run failed. It is fenced and redacted but still model-authored. Over GitHub's 1 MiB step-summary limit, a one-line notice is appended instead.

**Requirements.** `jq` (preinstalled on GitHub-hosted Linux and macOS runners) and `bash`. Windows runners are not supported.

### Untrusted input

Issue bodies, PR titles, diffs and commit messages can carry prompt injection. The action keeps them out of the shell, but it can't stop the model from following instructions in them, so limit what a hijacked run can reach:

- tinyagent and its commands can read anything in their environment, including the provider API key, unless a [command environment](#command-environment) policy excludes it. Use a config such as:

  ```yaml
  # .github/tinyagent/ci.yaml
  command_env:
    inherit: core
    exclude_secrets: true
  ```

- Prefer `review-base`/`review-commit` or `read-only: true`, which have no command or write tools. Add `sandbox: true` for runs that do execute commands. On Linux it needs Landlock (kernel 6.2+, 6.7+ to deny TCP), and a network allowlist also needs unprivileged user namespaces, which Ubuntu 23.10+ can restrict with AppArmor. This hasn't been checked on GitHub-hosted runners; tinyagent fails closed and says what is missing if the runner can't enforce it.
- When the task is untrusted text, such as an issue body, set `lockdown: true` and `untrusted-task: true`. The run then starts [tainted](#untrusted-content-lockdown): commands and MCP tools are refused, and `web_fetch` only reaches `untrusted_content.lockdown.web_fetch_allowed_domains`, while file tools, edits and the submission still work. Review runs are tainted by their diff once lockdown is on, unless `review` is left out of `untrusted_content.sources`.
- Set minimal `permissions:` and don't put `GITHUB_TOKEN` in the action's `env:`; the action never passes it on itself.
- Don't run on `pull_request_target` (or `workflow_run`) with a checkout of the PR's code.
- Treat every output and the job summary as untrusted: pass them to later steps through `env:`, never `${{ }}` inside `run:`.

See GitHub's [Secure use reference](https://docs.github.com/en/actions/reference/security/secure-use).

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
memory:                   # project memory; see Project memory (user config or -c only)
  enabled: false          # load active entries into the system prompt (same as --memory)
  approval: pending       # pending or auto: policy for entries the remember tool saves
  max_writes_per_run: 5   # entries one run may add, replace or forget; >= 1
  max_entries: 50         # active entries kept per project; >= 1
  max_bytes: 8192         # cap on the Project memory prompt section; 1 to 65536
subagents:
  enabled: false          # offer the delegate tool (same as --subagents)
  model: ""               # child model, e.g. openai/gpt-5-mini; empty uses the parent model
  max_steps: 30           # step cap per delegated task; 0 means the default (30)
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
acp:                         # --acp editor mode; see Editor integration (ACP)
  client_mcp_servers: false  # start the MCP servers the editor sends with session/new
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
- **Discovery**: `tinyagent --list-profiles [-c <file>]` prints the profile names in the selected config file (or the user config without `-c`), sorted, one per line. It makes no model request and needs no credentials. No config file, no `profiles:` section, or an empty mapping prints nothing and exits 0; a missing or unreadable file, malformed YAML, or an invalid profile exits 2 with nothing on stdout. It can't be combined with `-t`, `--profile`, or another mode (`--check`, review, `--resume`, `--rewind`, `--acp`, `--transcript`, `--completion`, `--init-config`, `--update`).
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

#### MCP resources

A server can also share read-only context as [MCP resources](https://modelcontextprotocol.io/specification/2025-11-25/server/resources) (documentation pages, schemas, tickets). They are off unless the server has a `resources` allowlist of URI patterns. Each pattern is an exact URI or a prefix ending in `*` (`docs://guides/*` matches every URI starting with `docs://guides/`); `*` anywhere else, a bare `*`, an empty string or a duplicate exits 2 before any model request. There is no "all resources" wildcard. A server needs a nonempty `tools` or `resources` list, so a resource-only server is allowed. A server with `resources` must declare the MCP `resources` capability, or startup exits 2 naming the server. Resource lists are not fetched at startup; the allowlist is checked on every call. URIs are compared as literal strings, without normalization or decoding, and the server must still validate them.

When at least one server has `resources`, the model gets two read-only tools:

- `list_mcp_resources` (optional `server`, optional `cursor` from a previous result, which requires `server`) lists each allowlisted resource as one line with `server`, `uri`, `name`, and `mimeType`, `size` and a description (cut to 200 characters) when present, then the resource templates whose literal prefix (up to the first `{`) can match a pattern, marked `template`. Filtered-out entries are only counted (`[N resources hidden by the allowlist on server docs]`). A listing fetches at most 100 pages per server; a longer one ends with a note giving the cursor to continue.
- `read_mcp_resource` (`server` and `uri`, both required) calls `resources/read` for an allowlisted URI. A URI outside the allowlist returns `error: resource not allowed for server <alias>: <uri>` without contacting the server. Each content item gets a `[resource: <uri>, <mime>]` header line; text follows in order, and binary blobs become `[omitted MCP resource blob: <mime>, <n> bytes]`. With `--images`, PNG, JPEG, GIF and WebP blobs within `max_image_bytes` pass through like MCP tool images.

Both tools use the server's `call_timeout_seconds` and the same result handling as MCP tools: a 256 KiB cap before the normal output cap, `redact_secrets`, tool output files, `pre_tool`/`post_tool` hooks (matched by the names `list_mcp_resources` and `read_mcp_resource`), `tool.call`/`tool.result` events and trajectories. Server errors (such as resource not found), interactive input requests and timeouts are returned to the model as tool errors and the server stays available; a transport failure disables it, as for tools. `-v` prints one line per read (server, URI, bytes, time), the stderr startup line counts resource patterns, `--check` shows each server's tool and resource pattern counts, and `run.started` lists each server's patterns as `mcp_resources` (never contents). Resource content is untrusted input that may carry prompt injection, and it goes to the model provider and into trajectories. Read-only runs, review runs and subagents don't get these tools. MCP prompts, resource subscriptions and list-change notifications, attaching resources from the command line, and following `resource_link` items in tool results are not supported. Older tinyagent versions reject a config file containing the `resources` key.

MCP tool calls and results are stored in trajectories along with the external tool names. Trajectories from MCP-enabled runs cannot be resumed. Resuming a built-in-only checkpoint works with `mcp_servers` configured; the servers are not started and the run keeps the built-in tools. Trajectories and server stderr may contain sensitive data supplied by tools or the server; [`redact_secrets`](#redacting-secrets-from-tool-output) masks known secret values in tool results, but not other sensitive data or server stderr. On shutdown tinyagent closes MCP sessions and asks their processes to exit, then the SDK applies its bounded termination. Unix process descendants and Windows child-process cleanup have platform-specific behavior; tinyagent does not promise process-tree cleanup on Windows.

### Command tools

`command_tools` gives the model your project's own actions (run the tests, regenerate code, check a migration) as named, typed tools, without writing an MCP server. Each entry has a `name` (`^[A-Za-z][A-Za-z0-9_-]{0,63}$`, not a built-in tool, MCP `<alias>__<tool>` or other command tool name), a `description`, optional `parameters` (a JSON Schema with `type: object` at the root, in the same subset as `--output-schema`; omitted means no arguments), an `argv` template, and optional `timeout_seconds` and `dir` (see the [Config](#config) example). Like `mcp_servers` and `hooks`, definitions are read only from the user config or `-c`, never from the repository. Every definition is checked at startup, and a bad one (name collision, empty `argv`, a placeholder in `argv[0]`, a placeholder for an undeclared or object-typed property, an unsupported schema keyword, a `dir` outside `--cwd`) exits 2 before the first model request, naming the tool and key. `--check` validates them too and prints how many are configured.

When the model calls a command tool, its arguments are checked against `parameters` first; if they fail, the model gets an error listing each problem and nothing runs. Then the `argv` template is filled in: a literal element passes through unchanged, and an element that is exactly `{{name}}` is replaced by that argument. A string becomes one element verbatim, a number or boolean its JSON form, an array of scalars one element per item, and a missing (or null) optional argument drops the element. Placeholders inside a larger string (`--pkg={{p}}`) are rejected. The command runs **without a shell**, so `;`, `$(...)`, backticks, quotes and newlines in an argument arrive as plain characters in one argv element.

Command tools run where `bash` runs and under the same controls: the [execution environment](#execution-environments) (`argv[0]` is looked up on the container's `PATH` there), [`--sandbox`](#sandboxing-commands), the [command environment](#command-environment) policy (`argv[0]` is looked up on the `PATH` it passes, so with `inherit: none` and no `PATH` use an absolute path), the working directory (`--cwd`, or `dir`), the timeout (which kills the process group on Unix), and `max_output_chars` and [tool output files](#tool-output-files). The result has the `bash` format (`<returncode>` and `<output>`, or the timeout message); a non-zero exit is a normal result, and an executable that isn't found is an error result. The run continues either way.

They are not offered with `--read-only` (a forged call is rejected like any other read-only denial), and subagents never get them. [Hooks](#tool-hooks) match them by name, with `arguments` as the decoded JSON the model sent. `-v` prints one stderr line per call with the final argv, `--json` and `--log-db` record the usual `tool.call` and `tool.result` events, `run.started` lists their names as `command_tools`, and trajectories record each definition (`name`, `argv` template, `schema_digest`). With `--resume`, the current config's command tools apply; if they differ from the checkpoint's, stderr and the model are told, and a call to one that no longer exists gets an error result.

**Security:** command tools are trusted definitions that run with your privileges, like hooks and MCP servers. Schema checks limit what the model can pass; they don't make an unsafe `argv` safe: `[bash, -c, "{{script}}"]` gives back arbitrary execution. An argument starting with `-` is passed through as is and can be read as an option (for example `--output=/etc/passwd`), so constrain every placeholder with a `pattern`, `enum` or similar, as in the `^\./` pattern above. Older tinyagent versions reject a config file containing the `command_tools` key.

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

- `tool` is required: a `|`-separated list of tool-name globs (`*` wildcard), covering built-ins, MCP `<alias>__<tool>` names and [`command_tools`](#command-tools) names.
- At most one argument matcher per rule: `command` (a `*` wildcard over the command text, for `bash`, `start_process` and a command tool's resolved argv), `path` (gitignore-style, relative to `--cwd`, in the [`file_access`](#restricting-file-access) syntax; for `apply_patch`, the rule matches when any path in the patch matches), or `host` (the [`sandbox_allowed_domains`](#sandboxing-commands) syntax, for `web_fetch`). A matcher that doesn't apply to the named tool (for example `path` on `bash`) is a config error.
- `decision` is required: `allow`, `ask`, or `deny`. `message` is optional and shown to the model on deny.
- Precedence is deterministic: **deny > ask > allow > default**.
- Compound shell commands are split on unquoted `&&`, `||`, `;`, `|` and newlines before matching. A `deny`/`ask` rule matches when it matches **any** segment; an `allow` rule allows only when **every** segment is matched by some allow rule; and a command containing `$(...)`, backticks or `eval` never matches an allow rule.
- `update_plan`, `ask_user` and `submit_result` are exempt: they are never prompted or denied, even under `tool: "*"`, `decision: deny`.

An `ask` is answered through the channel the run has:

- **Interactive terminal** (`tinyagent -t ...`, not `--json`, not `--acp`): tinyagent prints the tool, the matched rule and the primary argument (after secret redaction) to stderr and reads one answer from the controlling terminal (`/dev/tty`, never stdin, so `-t -` keeps working): `y` allows once, `a` allows that exact tool and argument for the rest of the run, and `n` (optionally with a reason) denies. Ctrl-C ends the run as `Interrupted`; the `--time-limit` firing mid-prompt ends it as `TimeLimitExceeded`.
- **ACP** (`--acp`): tinyagent sends `session/request_permission` with the call's `toolCallId` and the `allow_once`, `allow_always` and `reject_once` options; the editor's Approve/Reject buttons answer it. A `cancelled` outcome cancels the turn like `session/cancel`.
- **Non-interactive** (no terminal, `--json`, piped CI/GitHub Action runs): an `ask` resolves to `permissions.non_interactive` (`deny` by default), so nobody can answer but the run continues.

A denied call does not run and no `pre_tool` hook runs for it; the model gets `error: denied by permission rule <index>: <message>` (or `... by user: <reason>`), and the run continues. An allowed call then runs its [`pre_tool` hooks](#tool-hooks) as usual, which can still deny it — but a hook can never cause a policy-denied call to run. Rules come only from the user config, `-c` or a profile, never from the repository; a profile's `rules` replace the base list. Validation errors (unknown keys, an empty `tool`, an invalid decision, two matchers in one rule, a matcher/tool mismatch) exit 2 before any model request, naming the file, profile and rule index.

Every decision is recorded: a `permission` event goes to `--json`, `--log-db` and `--otel` (with `tool_call_id`, `tool_name`, `decision`, `source` (`rule`, `default`, `user`, `session` or `non_interactive`), the rule index, and the channel for asks), `run.started` and trajectories record a `permissions` summary, `-v` prints one stderr line per non-default decision, and `--check` prints the rule count, default and resolved non-interactive behavior.

**This is a guardrail, not a security boundary.** Command-text matching cannot stop a determined model from running something equivalent (for example `Bash(rm *)` doesn't stop `/bin/rm` or `bash -c 'rm ...'`). For real containment keep using [`--sandbox`](#sandboxing-commands), [`sandbox_allowed_domains`](#sandboxing-commands) and `--env container`. Older tinyagent versions reject a config containing `permissions`.

### Tool hooks

Hooks are disabled unless `hooks` is configured. A `pre_tool` hook runs before a matching tool call and can deny it with a reason the model sees; a `post_tool` hook runs after it and can append feedback, such as formatter or linter output, to the result the model sees. Like MCP servers, hooks are trusted local executables running with your privileges: they are read only from the user config or the `-c` file, never from the repository, launched directly (no shell) with an absolute `command`, `--cwd` as the working directory, and the same minimal environment (`PATH`, the Windows process variables when present, and only the named `env` variables, which must be set at startup). Provider API keys are not passed unless named. Hooks are a guardrail and automation point, not a sandbox: they see a call's parsed arguments, not what a shell command actually does.

Each hook gets one JSON object on stdin: `schema_version` (`1`), `event` (`pre_tool` or `post_tool`), `tool_name`, `tool_call_id`, `arguments` (decoded JSON, or the raw string if malformed), canonical `root`, `read_only`, and `step`; `post_tool` also gets `output` (the model-visible result after normal truncation) and `is_error`, plus `output_file` when [tool output files](#tool-output-files) saved the full result. The input can contain secrets from tool arguments and output.

| Event | Exit 0 | Exit 2 | Other exit, failure to start, timeout, or invalid stdout JSON |
|---|---|---|---|
| `pre_tool` | Allow, unless stdout is `{"decision":"deny","reason":"..."}` | Deny; the reason is stderr | Apply `on_error`: deny by default (the reason names the hook and the failure), or allow with a warning |
| `post_tool` | Optional stdout `{"feedback":"..."}` is appended to the result | stderr is appended to the result | Warning on stderr; the result is unchanged |

A denied call is not executed; the model gets the error result `error: denied by pre_tool hook <command>: <reason>` and the run continues. Matching hooks run one at a time in config order: the first `pre_tool` deny stops the rest, and every matching `post_tool` hook runs, each appending its feedback under `[post_tool hook <command>]` (capped at 8 KiB per hook; the combined result still honors `max_output_chars`). Read-only denials and unknown tools are rejected before hooks, so hooks never run for them and cannot widen authority; with `--read-only`, hooks still run for `read_file`, `list_files`, and `grep`. MCP tools are matched by their exposed `<alias>__<tool>` name, and [command tools](#command-tools) by their configured name. Ctrl-C or the wall-time limit kills a running hook (its process group on Unix; only the process on Windows). `-v` prints one stderr line per hook run, `--json` emits a `hook` event, and trajectories record the model-visible results, including denial reasons and feedback. Resumed runs use the hooks in the current config. Hooks do not run for `update_plan`, which only changes the run's plan.

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

#### Attaching images to the task

`--image <path>` attaches a screenshot, design mock, or other image to the task message, so the model sees it with the task text and needs no tool call:

```sh
tinyagent -t "fix the layout regression shown" --image artifacts/diff.png
tinyagent -t "make current.png match reference.png" --image current.png --image reference.png
```

- **Rules.** The flag is repeatable (no comma splitting), keeps its order, and allows up to 10 images. Each file must be PNG, JPEG, GIF, or WebP, detected from its bytes (a PNG named `.jpg` is sent as `image/png`), and at most `max_image_bytes` (default 5 MiB). A missing file, a directory, another format, an oversize file, more than 10 images, or `--image` without `-t` exits 2 before any model request.
- **Paths.** Paths resolve against the directory tinyagent was started in, not `--cwd`. They are your own input, not model file access: the workspace restriction and `file_access` rules do not apply. Only the base name reaches the model.
- **Independent of `--images`.** Attachments don't need `--images`, and they don't turn on `view_image`.
- **Message shape.** The task text gets one label line per image, such as `[attached image 1: mock.png, image/png, 48213 bytes]`, so the model can refer to "image 1". The images come before the text: `anthropic` sends `image` blocks with base64 data; `openai` on the Responses API and `codex` send `input_image` parts with `data:` URLs; Chat Completions (`openai` with `openai_api: chat`, `openrouter`, `ollama`) sends a content array of `image_url` parts with `data:` URLs plus a `text` part. Messages without attachments are sent exactly as before. The model must support vision (for Ollama, use a vision model such as `llava`); otherwise the provider's error ends the run like any other request error, and this applies to fallback models as well.
- **Text channels.** `run.started` (in `--json` and `--log-db`) lists each attachment's index, base name, MIME type, size, and sha256; `-v` prints one `attached image N:` line per file. No text channel carries image data.
- **Resume and follow-up.** The images are stored base64 in the checkpoint with the task message, so `--resume` sends them again without the original files. `--resume <checkpoint> -t "still wrong, see this" --image after.png` attaches images to the follow-up message. `--image` is refused (exit 2) when resuming an unfinished run or a finished one without `-t`. Older tinyagent versions refuse checkpoints that contain attachments.
- **Compaction and cost.** The task message is never compacted, so its images stay attached and are re-sent with every request. For the context estimate each image counts as about 1,600 tokens, and the summarizer sees only the label lines. Images typically cost 1–5k input tokens each per request, so crop or downscale large screenshots. tinyagent doesn't resize them. Base64 adds about a third to the size, so a 5 MiB file can exceed a provider's per-image limit (for example, 5 MB on Bedrock); lower `max_image_bytes` if that matters.
- **Privacy.** Attached images are sent to the model provider and stored in the trajectory/checkpoint file.

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

### Workers

`--workers` (or `subagents.workers.enabled: true`) adds two tools. `worker` hands a well-scoped implementation subtask to a child agent that may edit files and run commands, but only in its own Git worktree. `apply_worker` then applies the worker's change to your working tree. Nothing is merged, pushed or applied automatically.

```yaml
subagents:
  workers:
    enabled: true
    model: ""            # default: the parent's model
    max_steps: 60        # per worker
    max_concurrent: 1    # 1 to 4 workers from one response at once
    tools: [read_file, list_files, grep, write_file, edit_file, apply_patch, bash]
```

- **How a worker runs.** tinyagent snapshots the working tree (tracked changes plus untracked, non-ignored files) into a commit using a temporary index, so your index, `HEAD` and refs are untouched. It creates a worktree at `.tinyagent/worktrees/<run>-w<n>` on branch `tinyagent/<run>-w<n>` with repository hooks disabled. The child runs there with a fresh context; its file tools, commands and sandbox writable root are the worktree. When it finishes, whether it submitted or stopped early, tinyagent (not the child) commits its changes to the branch. With no changes there is no commit, and the worktree and branch are removed.
- **Result.** The worker's report, a footer such as `[worker w1: Submitted · branch tinyagent/run-…-w1 · commit abc1234 · 3 files changed, 40 insertions(+), 5 deletions(-) · 18 steps · $0.0400]` and the `git diff --stat`.
- **Applying.** `apply_worker` with `{"worker": "w1"}` applies the worker's base-to-commit diff to the working tree (not the index), atomically: on a conflict nothing changes and it lists the conflicting files and the branch. It is refused when the diff touches a [`file_access`](#restricting-file-access) `deny_write` path or a file outside `--cwd`. It counts as a file-changing tool for `--snapshots` and runs through `pre_tool`/`post_tool` hooks.
- **Tools.** A worker gets the configured `tools` that the parent itself has, never `delegate`, the worker tools, `ask_user`, `update_plan`, MCP tools or `web_fetch`. Hooks run for its calls with `subagent` set to `w1`, `w2`, … and `worker: true`.
- **Budgets.** Worker usage counts toward the parent's `--cost-limit`; a worker that hits a limit stops, its partial work is still committed, and the run ends with the limit status.
- **Concurrency.** With `max_concurrent` above 1, two or more `worker` calls in one model response run at the same time, at most `max_concurrent` at once. Permission checks, `pre_tool` hooks and worktree creation run first, one call at a time in call order, so every worker starts from the same snapshot; commits, `post_tool` hooks and results then follow in call order. The workers share the remaining `--cost-limit`: once their combined spend reaches it, or the run is interrupted or hits `--wall-time`, every running worker is cancelled, each keeps its partial work committed, and the run ends with that status. `--json` adds a `workers.batch` event (`workers`, `max_concurrent`, `stop`). Calls from different responses, and `apply_worker`, still run one at a time.
- **Requirements.** `--workers` needs a Git repository with at least one commit, and can't be combined with `--read-only` or the `--review` modes. Commits use the repository's identity, or `tinyagent <tinyagent@localhost>` when none is configured.
- **Observability.** `--json` adds `kind: "worker"`, `branch`, `commit` and `files_changed` to `subagent.started`/`subagent.finished`, and a `worker.applied` event. The trajectory records each worker's history, branch, base commit and commit.
- **Cleanup.** Worker branches and worktrees are kept, applied or not; tinyagent never removes them on its own. After the summary line, the run lists every kept worker branch, unapplied ones first, with ready-to-paste commands: `review:` (`git show --stat <commit>`) and `discard:` for unapplied workers, `clean up:` for applied ones (`git worktree remove --force <worktree> && git branch -D tinyagent/<name>`). With `--json`, the `result` event carries the same list as `worker_branches` (`id`, `branch`, `commit`, `worktree`, `applied`, `status`, `cleanup`).
- **Resume.** A checkpoint records whether workers were enabled; a conflicting `--workers` on `--resume` exits 2. `--rewind` affects only your tree, never worker branches.

**ACP.** Workers are also available in editor sessions with `--acp --workers` or the same config. Each session must start in a Git repository with a commit; read-only sessions reject workers. Later prompts can review and apply workers from earlier turns. Worker tool results appear in the editor, and kept branches and cleanup commands appear in the per-turn summary in the agent log.

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

### Untrusted-content lockdown

Lockdown (experimental, in progress; see [#171](https://github.com/vicgupta/tinyagent/issues/171)) limits what a prompt-injected run can send out. It is off by default; turn it on with `--lockdown` or `untrusted_content.enabled: true` (`--lockdown=false` overrides the config). Like `mcp_servers` and `memory`, `untrusted_content` is read only from the user config, `-c` or a profile, never from the repository.

```yaml
untrusted_content:
  enabled: true
  sources: [web_fetch, mcp, review]   # optional; leave it out to use every source
  lockdown:
    web_fetch_allowed_domains: [pkg.go.dev]   # web_fetch hosts still readable after taint
mcp_servers:
  - alias: docs
    # ... command, tools ...
    untrusted: false          # this server's results don't taint the run
    lockdown_allowed: true    # its tools and resource reads stay callable after taint
```

- **Taint.** A run becomes tainted when content from an untrusted source enters the model's context: a `web_fetch` result, an MCP tool result or resource read, or a review diff (`--review`, `--review-base`, `--review-commit`). `--untrusted-task` marks the task text itself as untrusted, so the run starts tainted; it needs lockdown on and doesn't turn it on (exit 2 otherwise). Taint is decided by source, never by content. `sources` chooses which of `web_fetch`, `mcp`, `review` and `acp_client_mcp` taint; an empty list, an unknown name or a duplicate exits 2. `--untrusted-task` and a resumed tainted checkpoint always taint.
- **After taint.** For the rest of the run, `bash`, `start_process` and [command tools](#command-tools) are refused, `web_fetch` may only read hosts in `web_fetch_allowed_domains` (same syntax as [`sandbox_allowed_domains`](#network-allowlist); each host must also be in `web_fetch.allowed_domains`, and a bare `*` exits 2), and MCP tools and resource reads are refused unless their server has `lockdown_allowed: true`. A refused call is a tool error that names the first taint source, and the tool result that first taints the run carries a one-line notice. File tools, edits, plans and the submission keep working. [Workers](#workers) share the parent's state, so their commands are refused too; [subagents](#subagents) are read-only and stay available.
- **MCP servers.** `untrusted: false` stops a server's results from tainting the run, and `lockdown_allowed: true` keeps its tools callable after taint. Both default to the strict behavior, and a call that can't be tied to one configured server counts as untrusted and is refused.
- **Resume.** Taint carries over `--resume` and follow-up runs with its sources and denials. A tainted checkpoint resumed with lockdown off exits 2; start a fresh run instead.
- **Observability.** On start, one line such as `lockdown: on (sources: web_fetch, mcp, review, untrusted task; after taint: commands refused, web_fetch → 1 domains, mcp tools refused)` goes to stderr, and `--check` prints the same settings on a `lockdown:` line. `--json` and `--log-db` add `lockdown` to `run.started`, emit [`taint` and `lockdown.denied`](#machine-readable-output) events and add `tainted` and `lockdown_denials` to `result`. The summary line notes the taint, for example `, tainted by web_fetch at step 3 (1 call refused)`. `-o` trajectories and checkpoints record `lockdown_enabled`, `tainted`, `taint_sources` and `lockdown_denials`, and [OpenTelemetry](#opentelemetry-export) spans carry `tinyagent.lockdown.enabled` and `tinyagent.lockdown.tainted`. Records hold source and tool names, ids, steps and hosts, never fetched content, URLs or MCP results.
- **Not supported yet.** `--acp` refuses lockdown (exit 2). Commands are refused outright after taint rather than run with a narrower network allowlist.

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

The run ends when the model writes `COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT` (`Submitted`), stops calling tools (`Completed`), or hits a limit (`StepLimitExceeded`, `CostLimitExceeded`, `TimeLimitExceeded`), Ctrl-C (`Interrupted`), a non-retryable API error (`Error`), too many consecutive malformed tool-call steps (`RepeatedFormatError`, see below), or, with `--output-schema`, 3 consecutive invalid results (`InvalidOutput`), or stop hooks still rejecting the result after `max_stop_continuations` (`VerificationFailed`, see [Stop hooks](#stop-hooks)), or too many consecutive responses cut off at the output-token limit (`OutputTruncated`, see [Output truncation](#output-truncation)), or, with `--ask-user`, a question for the user (`NeedsInput`, see [Asking for input](#asking-for-input)). The process exits 0 only for `Submitted` and `Completed`, 3 for `OutputTruncated`, 4 for `NeedsInput`, and 1 otherwise (2 is reserved for usage errors).

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

A mistyped flag exits 2 with one line naming up to three close flags, such as `error: unknown flag --sandbxo; did you mean --sandbox? (see tinyagent --help)`, instead of the full usage.

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
| An editor says the ACP agent failed to start or exited | Use the full path to `tinyagent` in the agent's `command`, put the API key in the agent's `env` (editors don't read your shell profile), and check the editor's agent log, which shows tinyagent's stderr. |
| `codex: no login` | Run `codex login` and choose ChatGPT. |
| `model is not supported` with `codex/...` | Use a model the Codex CLI's model picker shows for your plan. |

## Sandboxing commands

`--sandbox` (or `sandbox: true`) confines every `bash`, `start_process` and [command tool](#command-tools) command, and all processes they spawn, at the OS level. Commands can write only inside `--cwd`, a per-run temporary directory (exported as `TMPDIR`, removed when the run ends), the directories in `sandbox_writable_paths` and any `write` [additional directories](#additional-directories). Reads stay unrestricted so toolchains work. Outbound network is denied unless you pass `--sandbox-network` (`sandbox_network: true`). Flags override the config, including `--sandbox=false`. It is off by default.

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

By default `bash`, `start_process` and [command tool](#command-tools) commands inherit tinyagent's whole environment, including the provider API keys tinyagent itself needs, so a prompt-injected `env` or `echo $OPENAI_API_KEY` can read them. `command_env` (config only, from the user config or `-c`) controls what they get instead:

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

This is not a sandbox. It does not protect credentials stored in files (`~/.aws/credentials`, `.env`, the Codex auth file), which commands can still read ([`file_access`](#restricting-file-access) rules hide them from the file tools only). It does not redact secrets from tool output by itself: `pass` and `set` deliberately give commands the values you name, so turn on [`redact_secrets`](#redacting-secrets-from-tool-output) to keep those values out of what the model sees, trajectories and `--json`.

## Redacting secrets from tool output

`redact_secrets` (config only, from the user config, `-c` or a profile; never from the repository) replaces the exact values of secrets tinyagent knows about with `[REDACTED]` in tool output, **before** the output enters the model's context. It is off by default; `--redact-secrets` / `--redact-secrets=false` overrides `enabled` for one run.

```yaml
redact_secrets:
  enabled: true
  env: [DATABASE_URL, "DEPLOY_*"]   # extra variable names or globs whose values are secrets
  allow: [PUBLIC_KEY]               # secret-named variables not to mask
  patterns: false                   # true also masks token shapes (sk-..., gh?_..., Bearer ..., xox?-...)
```

- **Known values**, collected once at startup: tinyagent's environment variables named `*_KEY`, `*_TOKEN`, `*_SECRET` or `*_PASSWORD` (the `exclude_secrets` rule), `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `OPENROUTER_API_KEY`; MCP server and hook `env` values; named providers' `api_key_env` and `env_headers` values; `command_env.set` values; the variables matched by `env`; and the `codex` provider's tokens once loaded (including ones refreshed during the run). Variables matched by `allow` are left out. Values shorter than 6 characters are skipped, as GitHub Actions advises, since they would make output unreadable. Each value is also matched in its JSON-escaped form, longest first.
- **Covered:** `bash`, `start_process`, `read_process`, `stop_process`, the `background_processes` tails saved at run end, MCP tool results, `web_fetch`, `post_tool` hook feedback and `stop` hook reasons. Masking happens where the result is created, so the provider request, `-o` trajectory, `--json` events, `--log-db`, [tool output files](#tool-output-files), `post_tool` hook input and `--transcript` all get the masked text. It runs before truncation and before a result is saved to a file, so a truncated result never shows half a secret. Background process output that ends in the start of a secret is held back until the rest arrives or the process exits, so a secret split across reads is still masked whole.
- **Not covered:** the built-in file tools (`read_file`, `grep`, `list_files`, `lsp`, `view_image`, `skill`) and edit results, because their output feeds `edit_file`, `write_file` and `apply_patch`, and a placeholder written back would corrupt files. Use [`file_access.deny_read`](#restricting-file-access) to keep secret files away from them. Also not covered: the task, `AGENTS.md`, skills, `--image` files, the live terminal echo of `bash` `stream: true`, and subagents (which only have file tools).
- **Exact matches only.** Like GitHub Actions log masking, this finds a secret only as the exact string (or its JSON-escaped form). Transformed values (Base64, URL-encoded, split or reassembled), credentials stored only in files, and secrets tinyagent doesn't know about are not caught. `patterns: true` adds the token shapes `--log-db` uses, at the risk of masking test fixtures and docs. This is defense in depth, not a guarantee: a prompt-injected model can still try to exfiltrate secrets through commands, so combine it with [`command_env`](#command-environment) and [`--sandbox`](#sandboxing-commands).
- **Observability**, by variable name and count only, never values: one stderr line such as `redact_secrets: 14 values from 12 variables`; `-v` lists the masked names, the names skipped as too short and `env` entries that matched nothing. `tool.result` events have `redactions` when a result was masked, and `run.started` and `-o` trajectories record `redact_secrets` (`enabled`, `patterns`, `names`), so variable names do appear in those files.
- **Validation:** unknown keys, an empty name or a malformed glob in `env` or `allow`, the same entry in both, and a non-boolean `enabled` or `patterns` exit 2 before any provider request. An `env` name that is unset is not an error.
- **Resume:** the current invocation's setting applies to new results only. Earlier, unredacted results in the checkpoint are not rewritten, and with the setting off a resumed run behaves as before.

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
