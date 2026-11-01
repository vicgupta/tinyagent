# Models

Every model request tinyagent makes belongs to one of three **roles**: the main agent loop, auxiliary helper calls, and delegated children. Each role can use its own model and fallback chain, and runs report what each role used. This page covers choosing a model, providers, roles, fallback chains and usage reporting. To send individual delegated tasks to other models, see [Task-based routing](tasks.md#task-based-routing).

## Choosing a model

Pass `-m/--model` as `provider/model`:

```bash
tinyagent -m anthropic/claude-sonnet-4-5 -t "..."
tinyagent -m openrouter/qwen/qwen3-coder -t "..."
tinyagent -m ollama/llama3.1 -t "..."
```

Everything after the first `/` is the provider's model name, so `openrouter/qwen/qwen3-coder` sends `qwen/qwen3-coder`. Without `-m`, the model is `$AGENT_MODEL`, then `model:` in the [config file](config.md), then the built-in default `openai/gpt-4o`. `tinyagent --check` prints the resolved model and key status without calling the model.

## Providers

| Provider | Credentials | Notes |
|---|---|---|
| `openai` | `OPENAI_API_KEY` | `OPENAI_BASE_URL` overrides the endpoint; see [OpenAI transport](providers.md#openai-transport) |
| `anthropic` | `ANTHROPIC_API_KEY` | `ANTHROPIC_BASE_URL` overrides the endpoint |
| `openrouter` | `OPENROUTER_API_KEY` | |
| `ollama` | none | Local models |
| `codex` | ChatGPT sign-in | See [ChatGPT subscription](providers.md#chatgpt-subscription-codex) |
| `claude-code` | Claude Code's own sign-in | Runs your installed `claude` CLI; see [CLI handoff providers](#cli-handoff-providers) |
| `codex-exec` | Codex CLI's own sign-in | Runs your installed `codex exec`; see [CLI handoff providers](#cli-handoff-providers) |
| `opencode` | OpenCode's own sign-in | Runs your installed `opencode run`; see [opencode](#opencode) |
| your own id | per provider | [Named providers](providers.md#named-providers) from `providers:` in the config |

See [Providers and models](providers.md) for transports, [reasoning](providers.md#reasoning) and the top-level [model fallback](providers.md#model-fallback) chain.

## CLI handoff providers

`claude-code`, `codex-exec` and `opencode` don't call a model API. Each query runs a coding-agent CLI you installed and signed in to yourself, hands it the task, and returns the CLI's final answer as the model's reply. The CLI does the work with its own tools, so a handoff run normally ends after one step. tinyagent never reads, copies, stores, refreshes or proxies the CLI's credentials (such as `~/.claude/.credentials.json`, `~/.codex/auth.json` or OpenCode's `~/.local/share/opencode/auth.json`) and never sets auth environment variables; the CLI inherits tinyagent's environment and signs itself in.

```bash
tinyagent -m claude-code/default -t "fix the failing test"   # Claude Code's configured model
tinyagent -m claude-code/sonnet -t "..."                     # --model sonnet
tinyagent -m codex-exec/default -t "..."                     # Codex's configured model
tinyagent -m codex-exec/gpt-5.5 -t "..."                     # --model gpt-5.5
tinyagent -m opencode/default -t "..."                       # OpenCode's configured model
tinyagent -m opencode/big-pickle -t "..."                    # Zen model: --model opencode/big-pickle
tinyagent -m opencode/anthropic/claude-sonnet-4-5 -t "..."   # --model anthropic/claude-sonnet-4-5
```

The model name `default` sends no model flag, so the CLI's own default applies. Any other name is passed to the CLI as is.

### Common behavior

- **Binary:** looked up on `PATH` (`claude`, `codex`, `opencode`), or set `claude_code.path` / `codex_exec.path` / `opencode.path` in the [config](config.md). If it's missing, the run fails before any request with install and sign-in instructions; `tinyagent --check` shows which binary will run.
- **Prompt:** tinyagent's system prompt is left out (it describes tinyagent's tools, which the CLI doesn't have). A single task is sent as is; a longer conversation (e.g. `--resume`, chat) is sent as a labeled transcript. The prompt goes on stdin (as the message argument for `opencode`), never through a shell.
- **Working directory and time limit:** the CLI runs in `--cwd`. `--timeout` (and any cancellation) kills the CLI's whole process group on Unix; on Windows only the CLI process is killed.
- **Errors:** a non-zero exit, an error result or malformed output fails the step with the CLI's own message; sign-in problems add the command to run. Output that isn't the expected JSON is used as plain answer text.
- **Cost:** usage on a Claude or ChatGPT subscription isn't API spend, so cost always shows as $0 and `--cost-limit` has no effect; token counts the CLI reports are still recorded. If you point the CLI at an API key (for example `ANTHROPIC_API_KEY` or `CODEX_API_KEY` in your environment), that usage is billed by the vendor but still shows here as $0; check the vendor's console.
- **Session id:** the CLI's session or thread id is stored in the reply's `extra.handoff_session_id` in the trajectory.
- **Not supported:** `--reasoning-effort`, `--max-output-tokens` and images. Delegated children (`delegate`, `worker`) that use a handoff model run in tinyagent's working directory without the read-only mapping.

### claude-code

Runs Claude Code in print mode ([CLI reference](https://code.claude.com/docs/en/cli-reference), [headless mode](https://code.claude.com/docs/en/headless)):

```
claude -p --output-format json [--model <name>] [--tools Read,Glob,Grep --permission-mode dontAsk] "<instruction>"  < prompt
```

- **Sign in:** run `claude`, then `/login` ([authentication](https://code.claude.com/docs/en/authentication)). `--bare` is not used because it skips the subscription sign-in. If `ANTHROPIC_API_KEY` is set, Claude Code uses it in print mode instead of your subscription.
- **Permissions:** normally no permission flags are passed, so your Claude Code settings decide what it may do; in print mode anything that would need an approval prompt is refused. With `--read-only`, tinyagent restricts it to the Read, Glob and Grep tools and `dontAsk`, so nothing else runs.
- **Output:** the JSON result's `result` is the answer; `is_error`/`subtype` mark failures; `session_id` and `usage` are recorded. `total_cost_usd` is Claude Code's client-side estimate ([cost tracking](https://code.claude.com/docs/en/agent-sdk/cost-tracking)) and is not reported as spend.

### codex-exec

Runs `codex exec`, OpenAI's documented non-interactive route ([non-interactive mode](https://developers.openai.com/codex/noninteractive), [CLI reference](https://developers.openai.com/codex/cli/reference)). The separate `codex` provider instead talks to the ChatGPT Codex backend directly with the Codex CLI's saved tokens.

```
codex exec --json --sandbox <read-only|workspace-write> --cd <cwd> [--model <name>] -  < prompt
```

- **Sign in:** run `codex login` ([authentication](https://developers.openai.com/codex/auth)).
- **Sandbox:** `--read-only` maps to Codex's `read-only` sandbox; otherwise `workspace-write` ([sandboxing](https://developers.openai.com/codex/security)). No approval or bypass flags are passed. Codex requires a Git repository by default; tinyagent doesn't override that, so outside one the run fails with Codex's message.
- **Output:** the JSONL events are read; the last `agent_message` item is the answer, `thread.started` gives the thread id, `turn.completed` gives tokens, and `turn.failed`/`error` events fail the step. Codex reports no price, so cost is $0.

### opencode

Runs `opencode run`, OpenCode's documented non-interactive mode ([CLI](https://opencode.ai/docs/cli/), see `run`). tinyagent only starts the `opencode` binary; OpenCode loads its own config, providers and credentials.

```
opencode run --format json [--model <provider/model>] [--agent plan] "<prompt>"   # runs in --cwd
```

- **Install:** `curl -fsSL https://opencode.ai/install | bash`, `npm i -g opencode-ai`, or Homebrew ([intro](https://opencode.ai/docs/), [repository](https://github.com/anomalyco/opencode)). `tinyagent --check -m opencode/default` shows the binary and its `opencode --version`.
- **Sign in:** run `opencode auth login` (or set the provider's API key variable) as described in [Providers](https://opencode.ai/docs/providers/). OpenCode stores credentials in `~/.local/share/opencode/auth.json`; tinyagent never reads, copies or proxies that file or any token.
- **Model:** a `provider/model` after `opencode/` is passed to `--model` unchanged, in OpenCode's `provider/model` format ([Models](https://opencode.ai/docs/models/)); `opencode models` lists the ids. A single name (anything but `default`) is a model of OpenCode's own [Zen](https://opencode.ai/docs/zen/) provider, whose models are `opencode/<model-id>`: `-m opencode/big-pickle` sends `--model opencode/big-pickle`, and so does the explicit `-m opencode/opencode/big-pickle`. `opencode/default` sends no `--model`, so OpenCode's configured model applies.
- **Working directory:** the process runs in `--cwd` (its working directory); no `--dir` flag is passed. `opencode run --dir` exists only in v1.2.0 to v1.x and the v2 CLI rejects it (`Unrecognized flag: --dir`), while the working directory works on every version. A prompt starting with `-` gets a leading space so it isn't read as a flag.
- **Permissions:** normally no permission flags are passed (no `--auto`), so your OpenCode [permission](https://opencode.ai/docs/permissions/) config decides; a non-interactive run can't answer an `ask` prompt, so such requests are rejected by OpenCode.
- **Read-only:** `--read-only` selects OpenCode's built-in `plan` agent (`--agent plan`, [Agents](https://opencode.ai/docs/agents/)) and sets `OPENCODE_CONFIG_CONTENT` (inline config, loaded after global and project config, [Config](https://opencode.ai/docs/config/)) denying `edit`, `bash`, `webfetch`, `websearch`, `task`, `external_directory`, `question` and `doom_loop`, both at the top level and on the `plan` agent. If `OPENCODE_CONFIG_CONTENT` is already set, tinyagent refuses read-only instead of overwriting it. OpenCode managed settings outrank inline config, and this is OpenCode's own policy, not an OS sandbox.
- **Output:** `--format json` prints one JSON event per line. The event schema isn't documented; tinyagent reads it defensively, based on OpenCode's source: the `text` parts of the last assistant message are the answer, `step_finish` parts give `cost` and `tokens`, `error` events (or a non-zero exit) fail the step, and `sessionID` is recorded. Output that isn't JSON is used as plain text. `--format` exists since OpenCode v0.11.3; if an older build rejects it (`Unrecognized flag` / `Unknown argument`), tinyagent retries once without it and reads plain text. When `opencode` exits non-zero, the error names its `opencode --version` and path and suggests upgrading OpenCode or setting `opencode.path`. Unlike the subscription CLIs, OpenCode's `cost` is reported as spend, so `--cost-limit` counts it.

## Model roles

The `models:` section picks models per role. Every key is optional and nothing changes when it is unset.

- **main** is the model from `--model`, `$AGENT_MODEL` or `model:`; it runs the agent loop.
- **auxiliary** covers helper calls made outside the agent loop: `compaction` (summarizing context, see [Long runs](long-runs.md#long-runs)) and `review` (the `verify_on_stop.review` pass). A task's model is `--route <task>=`, then `models.auxiliary.tasks.<task>`, then `--aux-model`, then `models.auxiliary.model`, then the main model. If the routed model fails, the call is retried once on the main model (a stderr warning says so). Helper calls are not steps, but their cost and tokens count toward `--cost-limit` and `--token-limit` as before.
- **delegated** covers the children started by the [delegate and worker tools](long-runs.md#subagents-and-workers-at-a-glance): read-only researcher subagents and read-write builder workers. The `delegate` task's model is `--route delegate=`, then `models.delegated.tasks.delegate` (or its alias `subagents.model`), then `--delegate-model`, then `models.delegated.model`, then the main model; the `worker` task resolves the same way with `models.delegated.tasks.worker` and its alias `subagents.workers.model`.

The matching flags are `--aux-model <spec>`, `--delegate-model <spec>` and `--route <task>=<spec>` (see the [CLI reference](subcommands.md)). `--no-routing` ignores them all for one run; see [Turning routing off](tasks.md#turning-routing-off). The config keys are listed in [Configuration](config.md#model-roles).

```yaml
models:
  auxiliary:
    model: openai/gpt-5-mini
    tasks:
      review: anthropic/claude-sonnet-4
  delegated:
    model: openai/gpt-5-mini
    fallback_models: [anthropic/claude-sonnet-4-5]
```

`subagents.model` and `subagents.workers.model` remain supported as aliases of the task keys; setting an alias and its task key to different models is a config error. Unknown task names, empty values and specs containing whitespace are rejected. `--check` prints a `compaction:` and `review:` line when those tasks are routed away from the main model, and a `delegate:` and `worker:` line when those tools are on (each with the model, role, the key or flag it came from and any fallback chain); `run.started` lists the resolved routes as `models` (keyed by task: `role`, `model`, `source`).

## Per-role fallback chains

Each role may set `fallback_models`, its own [fallback chain](providers.md#model-fallback) (at most 3, no duplicates) used when the role's model keeps failing with `429`, `5xx` or network errors; it is checked like the main chain before any request and never inherits the top-level `fallback_models`. `--no-fallback` clears every role's chain too.

## Usage by role

Every model request is counted under its role and task: `main`/`main` for the agent loop, `auxiliary`/`compaction` and `auxiliary`/`review` for the helpers, `delegated`/`delegate` and `delegated`/`worker` for children (including their own helpers' role), split again by the model that served it. When anything other than the main loop made requests, the trajectory (`-o`), the `result` event, the [event log database](json-output.md#event-log-database) (`run_usage` table) and [eval](eval-framework.md) results and reports carry `usage_by_role`, and the summary line ends with `; by role: ...`. The split always adds up to the run's totals. `model.request` events carry `role` and `task`.
