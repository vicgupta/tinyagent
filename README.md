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

Each step is printed to stderr as it happens, and the download shows a `#` progress bar when stderr is a terminal:

```text
update: current version: v0.10.3 at /usr/local/bin/tinyagent
update: query latest release: https://api.github.com/repos/vicgupta/tinyagent/releases/latest
update: query latest release: found v0.10.4 (current v0.10.3)
update: select asset: tinyagent-linux-amd64 for linux/amd64
update: download checksums: SHA256SUMS
update: download binary: https://github.com/vicgupta/tinyagent/releases/download/v0.10.4/tinyagent-linux-amd64
  [####################....................]  50%  4.5 MiB / 9.0 MiB
```

It ends with the received size, the verified SHA-256, the install step and `update: done: updated v0.10.3 -> v0.10.4 at <path>`. If a step fails, the error names it: `error: update failed at verify checksum: ...`. See [`--update`](docs/subcommands.md#updating) for the details.

### Shell completion (Bash, Zsh and Fish)

`tinyagent --completion bash`, `tinyagent --completion zsh` and `tinyagent --completion fish` print a script that completes flag names (`--sandbox-<Tab>`, `--tool-out<Tab>`, short aliases such as `-t`). After `--env`, `--command-env`, `--reasoning-effort`, `--openai-api` and `--completion` (as `--flag <Tab>` or `--flag=<Tab>`) they also complete that flag's fixed values, so `--env <Tab>` offers `local` and `container`. Other flag values, such as models and profiles, are not completed; those words fall back to filenames.

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
tinyagent -m codex/gpt-5.6-luna -t "..."   # ChatGPT subscription, see docs/providers.md
cat task.md | tinyagent -t -               # read a long or multi-line task from stdin
tinyagent --read-only --cwd ~/src/project -t "Review error handling in pkg/api"
cat review-task.md | tinyagent --read-only -t - -o review.json
```

Run `tinyagent --help` (or plain `tinyagent` at a terminal) for the overview: usage, two examples, the common options (`-t/--task`, including `-t -` for stdin; `-m/--model` with its providers and default; `-c`, `--cwd`, `--resume`, `--read-only`, `-o`, `--json`, `--check`, `--update`, `-V`) and the subcommands, and `tinyagent --help --verbose` for every flag. Subcommands are shortcuts for common flags: `tinyagent sandbox`, `tinyagent memory list`, `tinyagent ratchet list`, `tinyagent planner --first`, `tinyagent plugins list`, `tinyagent tinygo status` (see [CLI reference](docs/subcommands.md)); each one alone, such as `tinyagent sandbox`, prints its help.

## Features

- Small set of file, search, shell and background-process tools, plus opt-in planning, skills, images, patches, subagents, web fetch, LSP and MCP tools.
- Per-role model routing (`models:` in config, `--aux-model`, `--delegate-model`, `--route`, keyword or `task_type` routes, `--no-routing`) for compaction, the verify review and delegated children, each role with its own fallback chain and a `usage_by_role` cost split in the summary, JSON, trajectory, `--log-db` and eval reports; off by default.
- Many providers: OpenAI, Anthropic, OpenRouter, Ollama, a ChatGPT subscription through Codex, and your own OpenAI-compatible endpoints.
- Streaming text, `--json` event output, structured output, SQLite event logs and OpenTelemetry.
- Project memory across runs, with approval before entries become active.
- Session handoff for long-running work (`--handoff`): each run leaves `.tinyagent/handoff.md` with what it did and what's next, the next fresh run reads it as data, and an optional startup command runs before the first turn; see [Session handoff](docs/long-runs.md#session-handoff).
- Project rules from agent failures (`tinyagent ratchet add`): each rule records its incident and an expiry or review condition, is added to the system prompt, and can be enforced as a permission deny rule; see [Project rules](docs/ratchet.md).
- A task planner that can require a plan before any change.
- Run budgets: turn cap, cost limit and token limit (`--token-limit`), with an 80% warning to the model; delegates, workers and `--attempts` share the budget, and a run that hits it can be resumed.
- Verify before stop (`--verify-on-stop`): a run that changed files isn't accepted until a test or build command passes; otherwise it is marked unverified. Optional computational checks (`--verify-check`, e.g. lint, type check, tests) run in order, cheapest first, before the run may finish; see [Computational checks](docs/verify.md#computational-checks). An optional inferential review (`--verify-review`) runs last, once every check passes, and reports the model's findings; see [Inferential review](docs/verify.md#inferential-review).
- Help everywhere: `tinyagent` or `tinyagent <subcommand>` alone at a terminal prints help (the top-level one shows how to pass the task, pick the model and the common options); `--help --verbose` lists every flag by section.
- `tinyagent --update`: checksum-verified self-update with step-by-step progress and a `#` download bar.
- Undo for file edits, workspace rewind, resume and follow-up runs.
- Sandboxed commands with a network allowlist, permission rules, hooks, untrusted-content lockdown and file-access rules.
- WebAssembly plugin modules run through Extism with permission checks, a version store, rollback and prune. Go source modules are built with TinyGo, an external dependency that tinyagent downloads on first use (pinned version, checksum-verified, cached locally, `tinyagent tinygo update` to update); TinyGo is never embedded in tinyagent.
- AGENTS.md, Agent Skills, plugins, skill workflows, ACP editor integration and a GitHub Action.

## Documentation

### Getting started

| Topic | Doc |
|---|---|
| Tasks: passing a task, task types, task-based routing | [docs/tasks.md](docs/tasks.md) |
| Models: choosing a model, providers, roles, fallback chains, usage by role | [docs/models.md](docs/models.md) |
| Every flag, subcommands and exit status | [docs/subcommands.md](docs/subcommands.md) |
| Config file, profiles, MCP and command tools | [docs/config.md](docs/config.md) |

### Running tasks

| Topic | Doc |
|---|---|
| Reviews, worktrees, best-of-N, resume, transcripts, follow-ups, asking for input, token and cost budgets | [docs/runs.md](docs/runs.md) |
| Long runs, session handoff, images, tool output files, apply_patch, subagents, workers, web fetch, LSP | [docs/long-runs.md](docs/long-runs.md) |
| Planner (`update_plan`, `--plan-first`) | [docs/planner.md](docs/planner.md) |
| Verify before stop (`--verify-on-stop`) | [docs/verify.md](docs/verify.md) |
| `undo_file` and `--rewind` | [docs/undo.md](docs/undo.md) |
| Project memory | [docs/memory.md](docs/memory.md) |
| Project rules from agent failures (`ratchet`) | [docs/ratchet.md](docs/ratchet.md) |
| AGENTS.md, skills and skill workflows | [docs/project-context.md](docs/project-context.md) |

### Models

| Topic | Doc |
|---|---|
| Providers, ChatGPT subscription, reasoning and fallback | [docs/providers.md](docs/providers.md) |
| `--json`, streaming, structured output, event log, OpenTelemetry | [docs/json-output.md](docs/json-output.md) |

### Safety

| Topic | Doc |
|---|---|
| Sandboxing, network allowlist, command environment, execution environments | [docs/sandbox.md](docs/sandbox.md) |
| Permissions, hooks, lockdown, secret redaction, file access, extra directories | [docs/permissions.md](docs/permissions.md) |

### Extending

| Topic | Doc |
|---|---|
| Plugins, WebAssembly modules, version store and TinyGo | [docs/plugins.md](docs/plugins.md) |
| WebAssembly code plugins: manifests, deny-by-default permissions, hot swap | [docs/wasm-plugins.md](docs/wasm-plugins.md) |
| Built-in tool notes | [docs/tools.md](docs/tools.md) |
| Editor integration (ACP) and GitHub Actions | [docs/integrations.md](docs/integrations.md) |
| Evaluation framework | [docs/eval-framework.md](docs/eval-framework.md) |
| Malformed tool calls, output truncation, troubleshooting | [docs/troubleshooting.md](docs/troubleshooting.md) |

### Evaluate changes in GitHub Actions

The experimental Action eval mode runs suites with the verified installer and
fails below a pass-rate threshold:

```yaml
- uses: actions/checkout@v4
  with: { fetch-depth: 0 }
- uses: vicgupta/tinyagent@main
  with:
    version: latest
    eval: evals/suite.yaml
    eval-out: evals/out
    eval-min-pass-rate: '0.8'
    cost-limit: '5'
    job-summary: 'true'
  env:
    ANTHROPIC_API_KEY: ${{ secrets.ANTHROPIC_API_KEY }}
- uses: actions/upload-artifact@v4
  if: always()
  with: { name: eval-results, path: evals/out }
```

Use full history (`fetch-depth: 0`) and set a cost limit. Run only trusted suites;
do not expose secrets to untrusted fork PRs. Pin the Action ref and binary
version for reproducible CI once released. See the [complete CI example and
outputs](docs/eval-framework.md#9-ci-usage-and-pass-rate-gating), including fork
PR filtering and dry runs.
