# tinyagent

A minimal coding agent in a single static Go binary.

## Quick start

Four commands from install to a first, read-only task. All tools run on your machine; read [What is sent where](#what-is-sent-where) before you point tinyagent at real work.

```bash
# 1. Install (macOS and Linux)
curl -fsSL https://raw.githubusercontent.com/vicgupta/tinyagent/main/install.sh | sh

# 2. Give the default model (openai/gpt-4o) its API key
export OPENAI_API_KEY=...

# 3. Check the setup: no model request, writes nothing
tinyagent --check

# 4. Run a first task, read-only, in a scratch copy of a repository
tinyagent --read-only --cwd ~/src/project -t "Review error handling in pkg/api"
```

`tinyagent --check` prints the model, provider, API-key status, sandbox, how commands run, which web tools are on, permissions and config source a run would use, and exits 2 with a `hint:` line naming the fix when a run would fail (a missing key, an invalid setting).

- **Install options:** Windows `winget install VicGupta.TinyAgent`; with Go, `go install github.com/vicgupta/tinyagent/cmd/tinyagent@latest`; also a direct download. See [Install](#install) for all of them, and updating.
- **Models:** any other provider is `-m <provider>/<model>`, e.g. `anthropic/claude-sonnet-4-5` (`ANTHROPIC_API_KEY`), `openrouter/qwen/qwen3-coder` (`OPENROUTER_API_KEY`), a local `ollama/llama3.1` (no key), a ChatGPT subscription (`codex/gpt-5.6-luna`). Per-project settings go in `.tinyagent/tinyagent.yaml`, which `tinyagent init` writes with comments, and its `env:` map can hold the key. See [docs/models.md](docs/models.md), [docs/providers.md](docs/providers.md) and [docs/config.md](docs/config.md).
- **First real task:** drop `--read-only` to let it edit files, and pass `--sandbox` to confine the commands it runs. From there, [Usage](#usage) and [Features](#features).

### What is sent where

- **Sent to the model provider you pick:** your task, the system prompt with the context tinyagent attaches to it (for example `AGENTS.md` and project memory), and each tool result the model asks for as the run goes — file contents read from your repository, command output, fetched pages and search results. That conversation follows the provider's data terms. Optional `redact_secrets` (off by default) masks known secret values in command and web-tool output before it reaches the model; file reads are not covered. With `ollama`, the model runs locally and the conversation never leaves your machine.
- **Run locally:** every tool runs on your machine, in `--cwd`, as your user. Commands are not sandboxed by default: a `bash` call runs with your environment and can read anything you can, including your API keys. Every run that can execute commands unsandboxed says so on stderr (`warning: commands run unsandboxed, as your user, with your full environment ...`); no setting hides that line, and `--sandbox`, `--read-only` or `--env container` are the ways to not get it. The on-by-default web tools make their own requests (`web_fetch` to public hosts, `web_search` queries to DuckDuckGo with Exa as backup; `--no-web-fetch` and `--no-web-search` turn them off). `--read-only` does not turn them off: a read-only run says so on stderr. See [Execution posture](docs/sandbox.md#execution-posture).
- **Boundaries you can draw:** `--sandbox` confines every command at the OS level (Linux: Bubblewrap with Landlock stacked inside, or Landlock alone; macOS: Seatbelt; not supported on Windows) to writing only inside `--cwd` and a per-run temp dir, denies outbound network unless you allow it, and fails closed rather than running unsandboxed. `--read-only` permits only `read_file`, `list_files` and `grep`, so nothing is changed and no commands run. The sandbox does not cover the provider requests, the web tools or the in-process file tools; permissions, a network allowlist, file-access rules (with opt-in symlink containment, [`file_access.follow_symlinks: false`](docs/permissions.md#restricting-file-access)) and secret redaction add finer control. See [docs/sandbox.md](docs/sandbox.md) and [docs/permissions.md](docs/permissions.md).

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

Install with WinGet:

```powershell
winget install VicGupta.TinyAgent
```

The package identifier is `VicGupta.TinyAgent` (WinGet matches it without regard to case). WinGet installs a zip whose only file is `tinyagent.exe` and adds the `tinyagent` command to your PATH. The release assets for that are `tinyagent-windows-amd64.zip` (x64) and `tinyagent-windows-arm64.zip` (arm64). See [Install](docs/install.md) for the zip layout and [Releasing](docs/release.md) for how the package is published. The package is listed after Microsoft merges the first submission to the Windows Package Manager community repository.

To install from the release assets directly, download `tinyagent-windows-amd64.zip` (or `-arm64.zip`) from the [latest release](https://github.com/vicgupta/tinyagent/releases/latest), extract `tinyagent.exe`, and put it in a folder on your `PATH`. Those zips are the only Windows files on the release. `tinyagent --update` downloads the zip, checks it against `SHA256SUMS`, and replaces this binary with the `tinyagent.exe` inside. An older release that has no zip is installed from its bare `tinyagent-windows-<arch>.exe` instead. The `bash` tool needs `bash` on your `PATH`, e.g. from Git for Windows.

### With Go

```bash
go install github.com/vicgupta/tinyagent/cmd/tinyagent@latest
```

### Manual download

Unix and macOS binaries, the Windows zips, and `SHA256SUMS` are on the [releases page](https://github.com/vicgupta/tinyagent/releases). `SHA256SUMS` lists `tinyagent-windows-amd64.zip` and `tinyagent-windows-arm64.zip`, not a bare Windows `.exe`. Verify with `shasum -a 256 -c SHA256SUMS --ignore-missing`. On macOS, a binary downloaded with a browser is quarantined; clear it once with `xattr -d com.apple.quarantine tinyagent`.

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

`tinyagent --completion bash`, `tinyagent --completion zsh` and `tinyagent --completion fish` print a script that completes flag names (`--sandbox-<Tab>`, `--tool-out<Tab>`, short aliases such as `-t`). After `--env`, `--command-env`, `--reasoning-effort`, `--openai-api`, `--sandbox-backend` and `--completion` (as `--flag <Tab>` or `--flag=<Tab>`) they also complete that flag's fixed values, so `--env <Tab>` offers `local` and `container`. Other flag values, such as models and profiles, are not completed; those words fall back to filenames.

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
make build      # ./tinyagent
make test       # go test -count=1 ./..., same as CI
make test-race  # go test -race -count=1 ./...
make fmt        # gofmt check, same as CI
make lint       # go vet, plus staticcheck if installed
make dist       # linux/darwin/windows × amd64/arm64 in dist/, plus Windows zips
```

See [CONTRIBUTING.md](CONTRIBUTING.md#getting-started) for what each target runs.

## Usage

```bash
export OPENAI_API_KEY=...
tinyagent -t "fix the failing test in pkg/foo" --cwd ~/src/project -o run.json -v
tinyagent -m anthropic/claude-sonnet-4-5 -t "..."
tinyagent -m openrouter/qwen/qwen3-coder -t "..."
tinyagent -m ollama/llama3.1 -t "..."
tinyagent -m codex/gpt-5.6-luna -t "..."   # ChatGPT subscription, see docs/providers.md
tinyagent -m opencode/anthropic/claude-sonnet-4-5 -t "..."   # your installed OpenCode, see docs/models.md#opencode
tinyagent -m opencode/big-pickle -t "..."                    # an OpenCode Zen model (opencode/big-pickle)
cat task.md | tinyagent -t -               # read a long or multi-line task from stdin
tinyagent --read-only --cwd ~/src/project -t "Review error handling in pkg/api"
cat review-task.md | tinyagent --read-only -t - -o review.json
tinyagent goal "fix the failing test in pkg/foo" --check "go test ./..."
```

Run `tinyagent --help` (or plain `tinyagent` at a terminal) for the overview: usage, two examples, the common options (`-t/--task`, including `-t -` for stdin; `-m/--model` with its providers and default; `-c`, `--cwd`, `--resume`, `--read-only`; optional `--tools` to scope a task to named tools ([docs](docs/tasks.md#tool-scoping)), `-o`, `--json`, `--check`, `--update`, `-V`) and the subcommands, and `tinyagent --help --verbose` for every flag. Subcommands are shortcuts for common flags: `tinyagent sandbox`, `tinyagent memory list`, `tinyagent ratchet list`, `tinyagent planner --first`, `tinyagent goal "..."`, `tinyagent plugins list`, `tinyagent triage issues.json`, `tinyagent tinygo status` (see [CLI reference](docs/subcommands.md)); each one alone, such as `tinyagent sandbox`, prints its help.

## Features

tinyagent gives an LLM ten base tools (`read_file`, `write_file`, `edit_file`, `undo_file`, `list_files`, `grep`, `bash`, and `start_process`, `read_process`, `stop_process` for background commands), plus `web_fetch` (on by default for public hosts; `--no-web-fetch` turns it off), the keyless `web_search` (on by default; DuckDuckGo with Exa as backup, with retries for transient failures and rate limits; `--no-web-search` turns it off) and the opt-in `update_plan`, `skill`, `view_image`, `apply_patch`, `delegate`, `lsp`, and MCP resource (`list_mcp_resources`, `read_mcp_resource`) tools, and keeps a linear message history until the model submits.

- Small set of file, search, shell and background-process tools, plus web fetch of public pages (on by default, optional host allowlist, private addresses always refused; `--no-web-fetch` turns it off), free keyless web search (DuckDuckGo, falling back to Exa's public MCP endpoint, or your own SearXNG; `--no-web-search` turns it off) and opt-in planning, skills, images, patches, subagents (read-only researchers), workers (read-write builders in their own Git worktree), LSP and MCP tools.
- Per-role model routing (`models:` in config, `--aux-model`, `--delegate-model`, `--route`, keyword or `task_type` routes, `--no-routing`) for compaction, the verify review and delegated children, each role with its own fallback chain and a `usage_by_role` cost split in the summary, JSON, trajectory, `--log-db` and eval reports; off by default.
- Many providers: OpenAI, Anthropic, OpenRouter, Ollama, a ChatGPT subscription through Codex, handoff to your installed Claude Code (`claude-code`), Codex CLI (`codex-exec`) or OpenCode (`opencode`), and your own OpenAI-compatible endpoints. Handoff providers stream the CLI's assistant text and its own tool activity as they arrive; the saved reply is still the CLI's final answer. See [CLI handoff providers](docs/models.md#cli-handoff-providers).
- Interactive chat (`tinyagent chat` or `-i`): a multi-turn conversation on the terminal with line editing, history, large clipboard pastes (bracketed paste; several MB, up to 8 MiB, arrive as one message), Ctrl-C to cancel a running turn and exit, and Markdown replies rendered for the terminal (headings, lists, code blocks, tables; `/raw` to turn off), a compact tool activity line and slash commands (`/cost`, `/context`, `/new`, `/save`, `/compact` (runs immediately and reports the size before and after), `/raw`, `/model`); every turn gets the same tools as a run, including [command tools](docs/config.md#command-tools); every session is saved and can be resumed. See [Interactive chat](docs/chat.md).
- Live tool output: `bash` and configured `command_tools` show progress in chat and web, and emit `tool.output` with `--json`. A CLI handoff provider uses the same preview for the CLI's own tools. `--no-stream` disables it; bash `stream: false` silences one call. Previews are bounded and use configured secret redaction, without changing final results. See [Streaming](docs/json-output.md#streaming).
- Web UI (`tinyagent web`): the same chat in your browser on `127.0.0.1`, guarded by a per-run token. The message box accepts large clipboard pastes (up to 8 MiB, matching terminal chat; the API body limit is 64 MiB). Assistant replies render as sanitized Markdown while they stream (headings, lists, tables, quotes, links, inline code and scrollable highlighted code blocks). A **Collapse tool calls** checkbox (checked by default, remembered in the browser) folds tool calls and raw tool output into one-line rows you can expand one by one, or shows them expanded inline, with or without `-v`/`--verbose`; `-v` adds internal detail cards (reasoning, debug and status events). See [tool calls and raw output](docs/web.md#features). It also has inline permission approve and deny, stop, edit and regenerate, a model picker (`/model` shows or switches the model), `/compact` (runs immediately), a searchable sessions sidebar, a `/` command menu, a Ctrl/⌘-K palette and light or dark mode. No Node build step and no dependencies. See [Web UI](docs/web.md).
- Streaming text, `--json` event output, structured output, SQLite event logs and OpenTelemetry.
- Agent Skills from `./skills`: on by default at an interactive terminal, off in CI, the GitHub Action, `--eval`, `--attempts`, `--lockdown`, `--read-only` and other non-TTY runs (`--skills`/`--no-skills` or `skills:` override; `--check` shows why); see [Agent Skills](docs/project-context.md#agent-skills).
- Terminal-aware styling: color only at a terminal, off with `NO_COLOR`, or forced with `--color=auto|always|never`; piped output stays plain text.
- Project memory across runs, with approval before entries become active, stored only in `./.tinyagent/memory` inside `--cwd`; see [Project memory](docs/memory.md#project-memory).
- Cwd-only project state: memory settings (`./memory.toml`), plugin data (`./.tinyagent/plugin-data/<name>`) and memory live inside `--cwd`, never in the user config directory; old locations get a one-line hint, not a migration. See [Memory settings](docs/memory.md#project-memory) and [Plugins](docs/plugins.md).
- Session handoff for long-running work (`--handoff`): each run leaves `.tinyagent/handoff.md` with what it did and what's next, the next fresh run reads it as data, and an optional startup command runs before the first turn; see [Session handoff](docs/long-runs.md#session-handoff).
- Prompt-cache-friendly history: the system prompt (with `AGENTS.md`) and everything already sent stay byte-identical from step to step, resume and follow-ups only append, and compaction never touches the system prompt or the task, so provider prompt caches keep hitting; see [Append-only history and prompt caching](docs/long-runs.md#append-only-history-and-prompt-caching).
- Project rules from agent failures (`tinyagent ratchet add`): each rule records its incident and an expiry or review condition, is added to the system prompt, and can be enforced as a permission deny rule; see [Project rules](docs/ratchet.md).
- A task planner that can require a plan before any change.
- Goal mode (`tinyagent goal "..."`): a shortcut that plans first, runs the task, and verifies before stop; optional `--check <cmd>` is the success-condition command (`--verify-check`). See [Goal mode](docs/tasks.md#goal-mode).
- Run budgets: turn cap, cost limit and token limit (`--token-limit`), with an 80% warning to the model; delegates, workers and `--attempts` share the budget, and a run that hits it can be resumed.
- Verify before stop (`--verify-on-stop`): a run that changed files isn't accepted until a test or build command passes; otherwise it is marked unverified. On by default for every `--workflow` stage (`--verify-on-stop=false` opts out); see [Default on for workflow runs](docs/verify.md#default-on-for-workflow-runs). Optional computational checks (`--verify-check`, e.g. lint, type check, tests) run in order, cheapest first, before the run may finish; see [Computational checks](docs/verify.md#computational-checks). An optional inferential review (`--verify-review`) runs last, once every check passes, and reports the model's findings; see [Inferential review](docs/verify.md#inferential-review).
- Skill workflows (`--workflow file.yaml -t ...`): run a task through fixed stages (plan, code, vet, test, verify behavior), each gated by a success check, with intent routing or `--workflow-name`; `tinyagent help workflows` at the terminal, full guide in [docs/workflows.md](docs/workflows.md).
- Spec gate for skill workflows (`gate: spec`): a plan whose tasks lack acceptance criteria or a runnable `Verify:` command is re-planned once, then surfaced instead of reaching generate-code; see [Spec gate](docs/project-context.md#spec-gate).
- Issue triage (`tinyagent triage issues.json`): ranks `gh issue list` output by value against effort with fixed, explainable rules, flags likely duplicates, stale and blocked items without dropping them, and writes a deterministic next-up JSON list an outside loop can work from; offline, no model call. See [Issue triage](docs/triage.md).
- Blocked tasks for skill workflows: a stage whose check keeps failing on its merits ends the run as `Blocked` (exit 5) rather than `Failed`, with no further attempts, so an outside loop can park the task until a person fixes it; see [Blocked tasks](docs/project-context.md#blocked-tasks).
- Learning from workflow failures (`--workflow-observe`, opt-in): a failed or blocked workflow run writes one proposed project rule with a ready-to-run `tinyagent ratchet add` command, and a failure that recurs is flagged as a gate that did not hold; see [Learning from failures](docs/project-context.md#learning-from-failures).
- Self-tuning (`tinyagent tune`, opt-in, off by default): from at least two past failed workflow runs with the same failure, propose one small skill or success-check change with its evidence, validate it by rerunning the tasks with and without it in isolated copies (regressions rejected), and apply or revert only the exact change you approve by digest; see [Self-tuning](docs/self-tuning.md).
- Contract docs for skill workflows: a `contract` workflow writes a stable reference doc (`docs/contracts/<surface>.md`: public API, invariants, how to extend) for a part of the target repo that keeps getting built on, and `plan-tasks` reads matching contracts before researching and reports surfaces that need one; see [Contract docs](docs/project-context.md#contract-docs).
- Behavior verification for skill workflows: a `verify-behavior` stage (`behavior:` in the workflow file) runs the built program and passes or fails on its observed exit code and output against the task's behavioral acceptance criteria, records that output as evidence, and is skipped when the task has none; see [Verifying behavior](docs/project-context.md#verifying-behavior).
- `tinyagent skills validate <path>...`: check skill-workflow skill files, skills directories and workflow files without a model call (exit 1 names an invalid skill), for CI or a pre-commit hook; see [Skill workflows](docs/project-context.md#skill-workflows).
- Help everywhere: `tinyagent` or `tinyagent <subcommand>` alone at a terminal prints help (the top-level one shows how to pass the task, pick the model and the common options); `--help --verbose` lists every flag by section.
- Per-project config: `tinyagent init` writes a commented `./.tinyagent/tinyagent.yaml`, the first config file read from `--cwd` in every mode (one-shot, chat, web, ACP; an old `./tinyagent.yaml` is no longer read; tinyagent prints a hint to move it) (flags, exported environment variables and `--profile` outrank it). It is local configuration that tinyagent never commits. Loading it or running `init` adds `/tinyagent.yaml` to `.tinyagent/.gitignore` and an untracked `tinyagent.yaml` pattern to `.git/info/exclude` (the repository `.gitignore` is left alone) so `git add .` skips an untracked copy, including the old `./tinyagent.yaml`; those rules do not apply to a file that is already tracked. Worker snapshots and worker commits leave it out, and refuse when a copy is already staged instead of changing the index. `bash`, `start_process`, command tools, and the container environment are not part of that guarantee: a `git commit` they run is left as the command made it. See [Keeping the config out of commits](docs/config.md#keeping-the-config-out-of-commits). Settings that launch programs or loosen security (`mcp_servers`, `hooks`, `command_tools`, `permissions`, ...) are honored from it only with `--trust-config`. See [docs/config.md](docs/config.md#config).
- Environment variables from the project config: a top-level `env:` map (`NAME: value`) in `.tinyagent/tinyagent.yaml` (or a profile) is set in tinyagent's own environment before providers, API-key checks, MCP servers and commands start, in every mode (one-shot, chat, web, ACP, resume, workflow stages, attempts and eval). So bare `tinyagent web` or `tinyagent chat` works with `OPENAI_API_KEY` kept in the local file instead of exported. A variable you already exported wins; flags beat both. Values are never printed (`-v`, `--check` and warnings name variables only), and variables that change which program runs (`PATH`, `LD_*`, `GIT_*`, ...) need `--trust-config` from a cwd file. `.env` files are still not read. See [Environment variables](docs/config.md#environment-variables).
- `tinyagent --check`: a setup summary table (model, provider, API key set/missing, sandbox, commands, web tools, permissions, lockdown, config source) with no model request, or `--check --json` for CI and scripts.
- Disclosed execution posture: an unsandboxed run always warns on stderr that commands run as your user with your environment, a `--read-only` run notes that `web_fetch`/`web_search` still reach the network, and `--check` reports both (`commands:` and `web:` lines; `commands` and `web_tools` in `--check --json`). See [Execution posture](docs/sandbox.md#execution-posture).
- Setup hints: a known setup failure (missing API key, invalid config setting, unavailable sandbox, missing `claude`/`codex` CLI) prints one `hint:` line naming the next action, including the config path and rule; see [Troubleshooting](docs/troubleshooting.md#troubleshooting).
- Actionable tool errors: each tool error the model sees states the cause and what to try next (same shape as `edit_file`'s unique-match message); see [Tool error messages](docs/tools.md#tool-error-messages).
- Every tool call gets a tool result: denials, errors, timeouts, lockdown refusals, ask_user answers and resume-interrupted calls always emit `tool.result` and a matching history message; see [Tool results](docs/json-output.md#tool-results).
- `tinyagent --update`: checksum-verified self-update with step-by-step progress and a `#` download bar.
- Undo for file edits, workspace rewind, resume and follow-up runs.
- Sandboxed commands (Linux: Bubblewrap with Landlock stacked inside, or Landlock alone, via `--sandbox-backend`; macOS: Seatbelt) with a network allowlist, permission rules (optional [AI permission judge](docs/permissions.md#ai-permission-judge) for `ask` decisions), hooks, untrusted-content lockdown and file-access rules.
- WebAssembly plugin modules run through Extism with permission checks, a version store, rollback and prune. Go source modules are built with TinyGo, an external dependency that tinyagent downloads on first use (pinned version, checksum-verified, cached in `./.tinyagent/cache/toolchains` inside `--cwd`, `tinyagent tinygo update` to update; see [Managing TinyGo](docs/wasm-plugins.md#managing-tinygo)); TinyGo is never embedded in tinyagent.
- AGENTS.md, Agent Skills, plugins, skill workflows, ACP editor integration and a GitHub Action.

## Documentation

### Getting started

| Topic | Doc |
|---|---|
| Install (script, WinGet, Go, direct download) | [docs/install.md](docs/install.md) |
| Releasing, including WinGet (maintainers) | [docs/release.md](docs/release.md) |
| Every flag, subcommands and exit status | [docs/subcommands.md](docs/subcommands.md) |
| Config file, profiles, MCP and command tools | [docs/config.md](docs/config.md) |
| Architecture: packages, request flow, key invariants (contributors) | [ARCHITECTURE.md](ARCHITECTURE.md) |

### Running tasks

| Topic | Doc |
|---|---|
| Interactive chat (`tinyagent chat`) | [docs/chat.md](docs/chat.md) |
| Web UI (`tinyagent web`) | [docs/web.md](docs/web.md) |
| Tasks: passing a task, goal mode, task types, task-based routing | [docs/tasks.md](docs/tasks.md) |
| Reviews, worktrees, best-of-N, resume, transcripts, follow-ups, asking for input, token and cost budgets | [docs/runs.md](docs/runs.md) |
| Long runs, session handoff, images, tool output files (MCP results capped at 256 KiB), apply_patch, subagents (read-only researchers), workers (read-write builders), web fetch, web search, LSP | [docs/long-runs.md](docs/long-runs.md) |
| Planner (`update_plan`, `--plan-first`) | [docs/planner.md](docs/planner.md) |
| Verify before stop (`--verify-on-stop`) | [docs/verify.md](docs/verify.md) |
| `undo_file` and `--rewind` | [docs/undo.md](docs/undo.md) |
| Project memory | [docs/memory.md](docs/memory.md) |
| Project rules from agent failures (`ratchet`) | [docs/ratchet.md](docs/ratchet.md) |
| Workflows: multi-stage skill pipelines (`--workflow`), routing, `--workflow-observe`, events | [docs/workflows.md](docs/workflows.md) |
| Self-tuning: propose, validate and approve skill or check improvements from past runs (`tinyagent tune`) | [docs/self-tuning.md](docs/self-tuning.md) |
| Setting up workflows: required fields, how to run, three worked examples (simple, medium, complex), limits | [docs/workflow-guide.md](docs/workflow-guide.md) |
| AGENTS.md, skills and skill workflows (reference) | [docs/project-context.md](docs/project-context.md) |

### Models

| Topic | Doc |
|---|---|
| Models: choosing a model, providers, roles, fallback chains, usage by role | [docs/models.md](docs/models.md) |
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
| Issue triage: ranking open issues into a next-up list | [docs/triage.md](docs/triage.md) |
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
