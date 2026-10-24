# tinyagent

```bash
curl -fsSL https://raw.githubusercontent.com/vicgupta/tinyagent/main/install.sh | sh
```

A minimal coding agent in a single static Go binary. It gives an LLM six tools (`read_file`, `write_file`, `edit_file`, `list_files`, `grep`, `bash`) and keeps a linear message history until the model submits.

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
```

### ChatGPT subscription (Codex)

The `codex` provider uses your ChatGPT subscription instead of an API key. Sign in once with the [Codex CLI](https://github.com/openai/codex) (`codex login`, choose ChatGPT). tinyagent reads the tokens it saves in `$CODEX_HOME/auth.json` (default `~/.codex/auth.json`), refreshes them when they expire and writes them back, so Codex stays signed in too. Requests go to the Codex backend's Responses API. Usage counts against your plan's limits rather than costing money, so cost shows as $0 and `--cost-limit` has no effect. Only models your plan offers in Codex work (the ones the Codex CLI's model picker shows); others fail with "model is not supported".

## Flags

| Flag | Default | |
|---|---|---|
| `-h, --help` | | Print usage to stdout and exit |
| `-t, --task` | — | Task prompt (required); `-` reads it from stdin |
| `-m, --model` | `$AGENT_MODEL` or `openai/gpt-4o` | `<provider>/<model>`; no `/` means openai. Providers: openai, openrouter, anthropic, ollama, codex |
| `-c, --config` | user config, else built-in | YAML config file; see [Config](#config) |
| `--cwd` | `.` | Root directory; tools can't reach outside it |
| `--step-limit` | 0 (unlimited) | Max model calls |
| `--cost-limit` | 0 (unlimited) | Max USD |
| `--time-limit` | 0 (unlimited) | Max wall-clock seconds; overrides `wall_time_seconds` |
| `--no-compact` | off | Never compact the history; see [Long runs](#long-runs) |
| `-o, --output` | — | Trajectory JSON, rewritten after every step |
| `-v, --verbose` | off | Print tool calls and their outputs |
| `--no-stream` | off | Wait for each full response instead of streaming it |
| `--no-agents-md` | off | Don't append the nearest `AGENTS.md` (at or above `<cwd>`, up to the repo root; capped at 32KB) to the system prompt |
| `-V, --version` | | Print the version and exit |
| `--update` | | Replace this binary with the latest GitHub release (checksum-verified) and exit |

tinyagent takes no positional arguments: quote the task and pass it with `-t`. A stray word such as `tinyagent "fix the tests"` or `tinyagent -t "..." typo --step-limit 5` is an error (exit 2), rather than silently dropping the flags after it.

The limit flags must be >= 0, and `--cost-limit` must also be finite. A typo such as `--step-limit -1` or `--cost-limit NaN` is an error (exit 2) instead of quietly running without the cap. An explicit 0 means unlimited and overrides the config file's limit.

Environment: `OPENAI_API_KEY`, `OPENAI_BASE_URL` (any OpenAI-compatible endpoint), `ANTHROPIC_API_KEY`, `ANTHROPIC_BASE_URL`, `OPENROUTER_API_KEY`, `OLLAMA_HOST`, `CODEX_HOME`.

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

```yaml
system_prompt: |          # {{cwd}} and {{os}} are substituted
  You are a coding agent...
step_limit: 50
cost_limit: 2.0
wall_time_seconds: 1800
bash_timeout_seconds: 120
max_output_chars: 30000   # tool output is truncated, keeping head and tail
input_price_per_mtok: 3   # override the built-in price table
output_price_per_mtok: 15
context_window: 200000    # tokens; overrides the built-in table, used for compaction
```

## Long runs

tinyagent resends the whole history on every step, so a long run can outgrow the model's context window. Before each request it estimates the request's size (the previous request's reported input tokens plus about 4 characters per token for anything added since). Above 80% of the window, it compacts:

1. Old tool outputs are replaced with a placeholder such as `[output elided: 12,400 chars]`. The system prompt, the task, and the most recent turns (about 25% of the window, and never fewer than the last 2 assistant turns with their tool results) are kept verbatim. If that brings the estimate under 50% of the window, it stops here.
2. Otherwise, one extra request asks the same model, with no tools, to summarize everything older than the recent turns. The model then sees the system prompt, the task, that summary, and the recent turns.
3. If it still doesn't fit, the run ends as `Error` with `context still exceeds the window after compaction`.

If a provider rejects a request as too long (`prompt is too long`, `context_length_exceeded`, `maximum context length`), tinyagent compacts and retries that step once; a second rejection ends the run as `Error`. This works even for models whose window isn't known.

The window comes from a small built-in table (`internal/model/context.go`) or `context_window` in the config, which wins. Without either, only the retry-on-error path runs. Each compaction prints one line to stderr, such as `compacted context: ~182,000 → ~31,000 tokens (summarized 41 messages)`. The summary request's tokens and cost count toward the run's totals and `--cost-limit`, but not `--step-limit`. With `-o`, the trajectory keeps every original message, and `compactions` records each one: the step, the estimates before and after, the stage, the summary, and which messages the model was sent next.

Summaries lose detail. Put instructions that must hold for the whole run in `AGENTS.md`, which is part of the system prompt and never compacted. `--no-compact` turns all of this off.

## Exit status

The run ends when the model writes `COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT` (`Submitted`), stops calling tools (`Completed`), or hits a limit (`StepLimitExceeded`, `CostLimitExceeded`, `TimeLimitExceeded`), Ctrl-C (`Interrupted`), or a non-retryable API error (`Error`). The process exits 0 only for `Submitted` and `Completed`.

Every run ends with one summary line on stderr, so stdout keeps only the model's text:

```text
Submitted after 12 steps in 1m42s (48,213 input / 3,104 output tokens, $0.23)
```

Cost is shown only when it's above zero, and the token/cost part is left out if the run ended before any model response. When some input was read from the prompt cache, the line also shows how much, e.g. `(48,213 input / 3,104 output tokens, 41,900 cached, $0.09)`. With `-o`, the trajectory JSON also records `input_tokens` and `output_tokens` totals, plus `cache_read_tokens` and `cache_write_tokens` when they're non-zero.

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

## Notes

- `read_file` takes optional `offset` (1-based start line) and `limit` (max lines) to read part of a large file. A partial read ends with `[lines A-B of N]`; with neither set, it returns the whole file as before.
- `edit_file` replaces exactly one occurrence of `old_str` and fails if it occurs zero or several times. Pass `replace_all: true` to replace every occurrence (for a rename); it still fails if there are none, and returns `ok (N replacements)`.
- Each `bash` call runs in a new process; `cd` and exported variables don't carry over. On timeout the whole process group is killed (Unix). The model can pass an optional `timeout` (1 to 600 seconds) on a `bash` call to override `bash_timeout_seconds` for that command.
- Path sandboxing is lexical: `..` and absolute paths outside `--cwd` are rejected, but a symlink inside the root that points outside it is followed. `bash` isn't sandboxed at all. Only run it on code you trust.
- Cost uses a small built-in price table (`internal/model/provider.go`). Unknown models cost $0 unless you set the price in the config, so `--cost-limit` does nothing for them. With a positive cost limit, tinyagent prints a warning on stderr before the first request when the `openai`, `openrouter` or `anthropic` model has no price, naming `input_price_per_mtok` and `output_price_per_mtok`. The run continues as normal.
- The `anthropic` provider turns on [prompt caching](https://platform.claude.com/docs/en/build-with-claude/prompt-caching) for every request, so each step reads the history the previous step already sent from the cache instead of paying full price for it again. Input tokens in the summary and trajectory include cached tokens. Cost prices cache writes at 1.25× the input price and cache reads at 0.1× (less on models where Anthropic charges less, such as 0.05× for Claude Opus 5.5 and Sonnet 5.5). `input_price_per_mtok` overrides the base price and the multipliers apply on top.
