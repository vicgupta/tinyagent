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
```

### ChatGPT subscription (Codex)

The `codex` provider uses your ChatGPT subscription instead of an API key. Sign in once with the [Codex CLI](https://github.com/openai/codex) (`codex login`, choose ChatGPT). tinyagent reads the tokens it saves in `$CODEX_HOME/auth.json` (default `~/.codex/auth.json`), refreshes them when they expire and writes them back, so Codex stays signed in too. Requests go to the Codex backend's Responses API. Usage counts against your plan's limits rather than costing money, so cost shows as $0 and `--cost-limit` has no effect. Only models your plan offers in Codex work (the ones the Codex CLI's model picker shows); others fail with "model is not supported".

## Flags

| Flag | Default | |
|---|---|---|
| `-t, --task` | — | Task prompt (required) |
| `-m, --model` | `$AGENT_MODEL` or `openai/gpt-4o` | `<provider>/<model>`; no `/` means openai. Providers: openai, openrouter, anthropic, ollama, codex |
| `-c, --config` | embedded defaults | YAML config file |
| `--cwd` | `.` | Root directory; tools can't reach outside it |
| `--step-limit` | 0 (unlimited) | Max model calls |
| `--cost-limit` | 0 (unlimited) | Max USD |
| `-o, --output` | — | Trajectory JSON, rewritten after every step |
| `-v, --verbose` | off | Print tool calls and their outputs |
| `--no-stream` | off | Wait for each full response instead of streaming it |
| `--version` | | Print the version and exit |
| `--update` | | Replace this binary with the latest GitHub release (checksum-verified) and exit |

Environment: `OPENAI_API_KEY`, `OPENAI_BASE_URL` (any OpenAI-compatible endpoint), `ANTHROPIC_API_KEY`, `ANTHROPIC_BASE_URL`, `OPENROUTER_API_KEY`, `OLLAMA_HOST`, `CODEX_HOME`.

## Config

Every key is optional. Command-line flags override the file.

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
```

## Exit status

The run ends when the model writes `COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT` (`Submitted`), stops calling tools (`Completed`), or hits a limit (`StepLimitExceeded`, `CostLimitExceeded`, `TimeLimitExceeded`), Ctrl-C (`Interrupted`), or a non-retryable API error (`Error`). The process exits 0 only for `Submitted` and `Completed`.

## Notes

- Each `bash` call runs in a new process; `cd` and exported variables don't carry over. On timeout the whole process group is killed (Unix).
- Path sandboxing is lexical: `..` and absolute paths outside `--cwd` are rejected, but a symlink inside the root that points outside it is followed. `bash` isn't sandboxed at all. Only run it on code you trust.
- Cost uses a small built-in price table (`internal/model/provider.go`). Unknown models cost $0 unless you set the price in the config, so `--cost-limit` does nothing for them.
