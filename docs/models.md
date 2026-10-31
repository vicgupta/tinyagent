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
| your own id | per provider | [Named providers](providers.md#named-providers) from `providers:` in the config |

See [Providers and models](providers.md) for transports, [reasoning](providers.md#reasoning) and the top-level [model fallback](providers.md#model-fallback) chain.

## Model roles

The `models:` section picks models per role. Every key is optional and nothing changes when it is unset.

- **main** is the model from `--model`, `$AGENT_MODEL` or `model:`; it runs the agent loop.
- **auxiliary** covers helper calls made outside the agent loop: `compaction` (summarizing context, see [Long runs](long-runs.md#long-runs)) and `review` (the `verify_on_stop.review` pass). A task's model is `--route <task>=`, then `models.auxiliary.tasks.<task>`, then `--aux-model`, then `models.auxiliary.model`, then the main model. If the routed model fails, the call is retried once on the main model (a stderr warning says so). Helper calls are not steps, but their cost and tokens count toward `--cost-limit` and `--token-limit` as before.
- **delegated** covers the children started by the [delegate and worker tools](long-runs.md#subagents). The `delegate` task's model is `--route delegate=`, then `models.delegated.tasks.delegate` (or its alias `subagents.model`), then `--delegate-model`, then `models.delegated.model`, then the main model; the `worker` task resolves the same way with `models.delegated.tasks.worker` and its alias `subagents.workers.model`.

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
