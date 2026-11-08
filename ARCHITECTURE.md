# Architecture

A short map of the code for contributors. Behavior lives in [docs/](docs/);
this page says where it is implemented and which rules every change must keep.
See [CONTRIBUTING.md](CONTRIBUTING.md) for the workflow and checks.

## Packages

`tinyagent` is one Go module with a single binary.

| Path | Role |
|---|---|
| `cmd/tinyagent/` | CLI entrypoint: flag parsing, subcommands (`chat`, `web`, `goal`, `init`, `eval`, `triage`, `ratchet`, `tune`, ...), `--help` topics, chat and web sessions, workers and subagents, worktrees, `--update` glue. See [docs/subcommands.md](docs/subcommands.md). |
| `cmd/tinyagent/web/` | The embedded `tinyagent web` page (hand-written HTML/CSS/JS, no build step). See [docs/web.md](docs/web.md). |
| `embed/prompts/` | System and review prompt templates. |
| `internal/agent/` | The step loop: query the model, check permissions, run tool calls, record results, compact, verify before stop. |
| `internal/model/` | Providers behind one OpenAI-shaped message format, prices, context windows, CLI handoff providers (`claude-code`, `codex-exec`, `opencode`). See [docs/models.md](docs/models.md) and [docs/providers.md](docs/providers.md). |
| `internal/routing/` | Which model serves each role and task, and fallback chains. |
| `internal/tools/` | Built-in file and shell tools sandboxed to `--cwd`, opt-in tools, and the tool selection and its signature. See [docs/tools.md](docs/tools.md). |
| `internal/config/` | `.tinyagent/tinyagent.yaml` and `memory.toml` loading over built-in defaults. See [docs/config.md](docs/config.md). |
| `internal/cwdpath/` | Confines config and env paths to `--cwd`. |
| `internal/skills/`, `internal/harness/`, `internal/observer/` | Agent Skills discovery, skill workflows, and the failure observer. See [docs/project-context.md](docs/project-context.md) and [docs/workflows.md](docs/workflows.md). |
| `internal/permissions/`, `internal/hooks/`, `internal/lockdown/`, `internal/sandboxnet/` | Permission policy (and the optional AI judge), tool hooks, untrusted-content lockdown, sandbox network allowlist. See [docs/permissions.md](docs/permissions.md) and [docs/sandbox.md](docs/sandbox.md). |
| `internal/mcptools/`, `internal/plugins/`, `internal/tinygo/`, `internal/lsp/` | Local MCP servers, Agent Plugins and WebAssembly plugins, the pinned TinyGo toolchain, language servers. See [docs/plugins.md](docs/plugins.md) and [docs/wasm-plugins.md](docs/wasm-plugins.md). |
| `internal/trajectory/`, `internal/resume/`, `internal/snapshot/`, `internal/transcript/`, `internal/handoff/` | The `-o` run record and checkpoint, what `--resume` must match, file snapshots for undo and rewind, Markdown transcripts, the session handoff file. See [docs/runs.md](docs/runs.md), [docs/undo.md](docs/undo.md) and [docs/long-runs.md](docs/long-runs.md). |
| `internal/eventlog/`, `internal/telemetry/` | `--json` events, the SQLite event log, OpenTelemetry export. See [docs/json-output.md](docs/json-output.md). |
| `internal/selftune/` | Opt-in self-tuning (`tinyagent tune`): skill and check candidates from past workflow runs, validation in isolated copies, digest-approved apply and revert. See [docs/self-tuning.md](docs/self-tuning.md). |
| `internal/memory/`, `internal/ratchet/` | Project memory and project rules from agent failures. See [docs/memory.md](docs/memory.md) and [docs/ratchet.md](docs/ratchet.md). |
| `internal/attempts/`, `internal/gitwt/` | Best-of-N attempts and the Git plumbing for workers. |
| `internal/acp/` | Agent Client Protocol (editor integration). See [docs/integrations.md](docs/integrations.md). |
| `internal/eval/`, `internal/triage/` | The eval runner and issue triage. See [docs/eval-framework.md](docs/eval-framework.md) and [docs/triage.md](docs/triage.md). |
| `internal/outschema/`, `internal/termmd/`, `internal/update/` | `--output-schema`, the streaming terminal Markdown renderer, `--update` self-update. |
| `action.yml`, `action/` | The GitHub Action. |

## Request flow

1. **Startup** (`cmd/tinyagent/main.go`): locate and load the config (`-c`, else `.tinyagent/tinyagent.yaml` in `--cwd`, else defaults; loading the cwd file makes sure `.tinyagent/.gitignore` ignores it), apply the profile, environment and flags, and pick the mode (one-shot `-t`, chat, web, ACP, a subcommand).
2. **Setup**: resolve the main model and role routes (`internal/routing`), build the provider client (`internal/model`), choose the tool set (`internal/tools`, plus MCP, command and plugin tools), and build the system prompt once from `embed/prompts`, `AGENTS.md`, memory, project rules and the skills catalog. `--resume` checks the checkpoint first (`internal/resume`).
3. **Loop** (`internal/agent`, `Agent.Run`): send the history to the model; for each tool call, check permissions, hooks and lockdown, run the tool, and append its result; save the checkpoint with `-o`; compact when the context fills up; repeat until the model submits, a limit is hit, or verify-before-stop sends it back to work.
4. **Output**: the final answer and summary line on the terminal, `--json` events (`internal/eventlog`), and the trajectory (`internal/trajectory`).

## Invariants

Keep these when you change code; each has tests or docs that pin it.

- **State lives in the working directory.** Project files are read from `--cwd` only, with no walk-up and no home-directory fallback: `./.tinyagent/tinyagent.yaml` (local, never committed: [docs/config.md](docs/config.md#keeping-the-config-out-of-commits)) (the old `./tinyagent.yaml` is no longer read; a hint says to move it), `./memory.toml`, and skills from `./skills/<name>/SKILL.md` only (`.agents/skills` and `.claude/skills` are not scanned; a warning points at `./skills`, see [docs/project-context.md](docs/project-context.md#agent-skills)). Files tinyagent writes go under `./.tinyagent/` in `--cwd` (memory, tool output files, plugin data, handoff file, toolchain cache). There is no playbooks feature yet; one added later must follow the same rule (`./playbooks`). Paths from config or env are confined with `internal/cwdpath`.
- **AGENTS.md only.** Project instructions come from `AGENTS.md`, never `CLAUDE.md`. The startup lookup is the one read outside `--cwd`: it walks up from `--cwd` to the repository root and the nearest file wins; `--scoped-agents-md` adds subdirectory files lazily. See [docs/project-context.md](docs/project-context.md#project-instructions-agentsmd).
- **No user config.** The old per-user `config.yaml`, `memory.toml` and plugin-data folder under the OS user config directory are never read; tinyagent only checks whether they exist to print a hint (`tinyagent init --migrate` copies the config into `--cwd`). Use `-c PATH` to share one config across projects. A repository's `.tinyagent/tinyagent.yaml` cannot run programs or loosen security without `--trust-config`. See [docs/config.md](docs/config.md#config) and [Trusting a project config](docs/config.md#trusting-a-project-config). CI fails if tests write into the user config directory.
- **Tool signature pinning.** The tool set is fixed for a run. `tools.Selection.Signature()` fingerprints the built-in tool definitions plus a versioned execution-contract prefix per opt-in tool set, and the checkpoint records it as `tool_signature` (external tool names are checked separately); `--resume` refuses a checkpoint whose tool signature, system prompt or recorded settings no longer match. Bump a prefix when tool semantics change without a spec change. See [Resume an unfinished task](docs/runs.md#resume-an-unfinished-task).
- **Append-only history.** Every request is the previous one plus new messages at the end; the system prompt and tool definitions stay byte-identical and model replies are resent as returned, so prompt caches keep hitting. Compaction (view only, never the stored history) and the `--plan` block are the documented exceptions. `internal/agent/append_only_test.go` pins it. See [Append-only history and prompt caching](docs/long-runs.md#append-only-history-and-prompt-caching).
- **Every tool call gets a result.** Denied, errored, timed-out and interrupted calls still append a tool message and emit `tool.result`. See [Tool results](docs/json-output.md#tool-results).
