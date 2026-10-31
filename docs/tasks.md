# Tasks

A **task** is the prompt tinyagent works on. One invocation runs one main task in `--cwd`; along the way it can start other tasks: subagents and workers it delegates to, review passes, extra attempts, or the steps of a workflow. Each task runs on the model of its [role](models.md#model-roles), and delegated tasks can be [routed](#task-based-routing) to other models by their content.

## Passing a task

```bash
tinyagent -t "fix the failing test in pkg/foo" --cwd ~/src/project
cat task.md | tinyagent -t -          # a long or multi-line task from a file, via stdin
git diff | tinyagent --read-only -t - # review what is piped in, changing nothing
```

- `-t/--task` is the prompt; it is required unless `--resume` is set. `-t -` reads it from stdin, the easy way to pass a long task kept in a file.
- `--read-only` permits only `read_file`, `list_files` and `grep`, for review and question tasks that must not change anything.
- `--image <path>` attaches images to the task; see [Attaching images](long-runs.md#attaching-images-to-the-task).
- `--untrusted-task` marks task text taken from an issue or ticket as untrusted; see the [CLI reference](subcommands.md).
- A finished run takes a follow-up task with `--resume run.json -t "..."`, and an unfinished one can be resumed; see [Follow-ups](runs.md#continue-a-finished-run-with-a-follow-up-task), [Resume](runs.md#resume-an-unfinished-task) and [Asking for input](runs.md#asking-for-input).

## Task types

| Type | Started by | Role / task | Details |
|---|---|---|---|
| Main run | `-t` | `main` / `main` | [CLI reference](subcommands.md) |
| Subagent | the `delegate` tool (`--subagents`) | `delegated` / `delegate` | Read-only researcher: investigates and reports back with a fresh context, cannot change files; [Subagents](long-runs.md#subagents) |
| Worker | the `worker` tool (`--workers`) | `delegated` / `worker` | Read-write builder: makes changes in its own Git worktree, and the parent applies them with `apply_worker`; [Workers](long-runs.md#workers) |
| Review | `--review`, `--review-base`, `--review-commit` | `main` / `main` | Read-only review of a diff; [Reviewing changes](runs.md#reviewing-changes) |
| Inferential review | `--verify-review` | `auxiliary` / `review` | Model review before stop; [Inferential review](verify.md#inferential-review) |
| Compaction | long runs, automatically | `auxiliary` / `compaction` | Context summary; [Long runs](long-runs.md#long-runs) |
| Attempts | `--attempts N` | `main` / `main` | Up to 4 tries, each in a worktree; [Best-of-N attempts](runs.md#best-of-n-attempts) |
| Workflow | `--workflow file.yaml` | `main` / `main` | A YAML workflow file whose stages run Markdown skills; [Skill workflows](project-context.md#skill-workflows) |

## Task-based routing

`models.delegated.routes` sends individual delegated tasks to other models. Each route has a `model` and at least one of `match` (keywords; the route matches when the task brief contains any of them, case-insensitive) and `type` (a single word). When a route sets a `type`, the `delegate` and `worker` tools get an optional `task_type` argument listing the configured types, and a route also matches when the model passes its type. Routes are checked in order and the first match wins; a task that matches none uses the delegated model resolved above. A routed child keeps the role's `fallback_models` chain.

```yaml
models:
  delegated:
    model: openai/gpt-5-mini
    routes:
      - match: [migration, schema]
        model: anthropic/claude-sonnet-4
      - type: docs
        model: openai/gpt-5-nano
```

`--route <task>=<spec>` sets one task's model for a run (for example `--route worker=openai/gpt-5-mini`) and wins over the config keys; the full order for each task is in [Model roles](models.md#model-roles).

### The model.route event

With routes configured, every delegated task emits a `model.route` event (`role`, `task`, `model`, `reason`: `type <name>`, `match "<keyword>"` or `default`), and an auxiliary helper that falls back to the main model emits one with `reason: fallback` and `fallback_from`. Without routes the tool schemas and events are unchanged. `--check` prints one `route:` line per route. Routes are only accepted under `models.delegated`.

See [Machine-readable output](json-output.md#machine-readable-output) for every event field.

### Turning routing off

`--no-routing` turns all of this off for one run: the `models:` section and the `--aux-model`, `--delegate-model` and `--route` flags are ignored and every task uses the main model; `subagents.model` and `subagents.workers.model` still apply.
