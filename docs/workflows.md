# Workflows

Skill workflows run one task through a fixed sequence of stages, such as plan, generate code, vet, test and verify behavior. Each stage is a fresh, unattended tinyagent run of one **skill** (a Markdown file). A **success check** gates each stage. The next stage starts as soon as the current one passes. A failed stage is retried, and when its retries run out the workflow stops.

At the terminal, `tinyagent help workflows` prints a short version of this page.

- [What workflows are and when to use them](#what-workflows-are-and-when-to-use-them)
- [Quick start](#quick-start)
- [Files and locations](#files-and-locations)
  - [Workflow file (YAML)](#workflow-file-yaml)
  - [Skill files (Markdown)](#skill-files-markdown)
  - [Paths](#paths)
- [Routing and matching](#routing-and-matching)
- [Running stages](#running-stages)
- [Flags and settings](#flags-and-settings)
- [Learning from failures (`--workflow-observe`)](#learning-from-failures---workflow-observe)
- [Output, JSON events and exit status](#output-json-events-and-exit-status)
- [Interaction with other features](#interaction-with-other-features)
- [Troubleshooting](#troubleshooting)
- [Examples](#examples)

## What workflows are and when to use them

A plain `tinyagent -t "..."` run is one agent loop, and the model decides when it is done. A workflow splits the work into stages that you define, and each stage has to prove that it succeeded before the next one runs:

- **Use a workflow** for a repeatable process where you want the same steps and gates every time: implementing features from a spec, writing contract docs, CI jobs that must pass `go vet` and tests, or an outside loop that feeds issues to tinyagent.
- **Use a plain run or `tinyagent chat`** for one-off or exploratory work, or when you want to steer the agent as it goes. A workflow runs unattended, with no pauses or approvals between stages.

Workflows are opt-in. Without `--workflow`, tinyagent behaves as before. Workflow skills are separate from [Agent Skills](project-context.md#agent-skills) (`--skills`), which the model loads on demand during a normal run.

## Quick start

The repository ships a complete example in [docs/workflows](workflows): [`workflow.yaml`](workflows/workflow.yaml) and its [skills](workflows/skills).

New to workflows? [Setting up workflows](workflow-guide.md) walks through three examples (one stage, a few stages with checks and retries, and the full plan-to-behavior pipeline), with every file you need.

```sh
# 1. Check the files (no model call, no credentials needed)
tinyagent skills validate docs/workflows/workflow.yaml

# 2. Run it on a task; the router picks the "implement" workflow
tinyagent --workflow docs/workflows/workflow.yaml -t "add CSV export to the report command"

# 3. Run a named workflow and keep a record of every stage
tinyagent --workflow docs/workflows/workflow.yaml --workflow-name contract \
  -t "write a contract doc for internal/tools" -o run.json
```

To start your own, copy `docs/workflows/` into your project (for example to `.tinyagent/workflows/`), edit the stages and checks, and run `tinyagent skills validate .tinyagent/workflows/workflow.yaml`.

## Files and locations

A workflow has two layers: one YAML **workflow file** (the pipeline) and one Markdown **skill file** per stage (the instructions).

### Workflow file (YAML)

```yaml
version: 1                # required, must be 1
skills_dir: skills        # relative to this file (default: skills)
default: implement        # used when routing finds no match (optional)
defaults:                 # apply to every stage
  retries: 1
  step_limit: 50
workflows:
  implement:
    intent: Implement a feature from a spec, then validate, test and verify its behavior.
    stages:
      - skill: plan-tasks
        gate: spec        # check the plan before code is generated
      - skill: generate-code
        step_limit: 100
      - skill: validate-diff
        check: [go, vet, ./...]
      - skill: run-tests
        check: [go, test, -count=1, ./...]
        check_timeout: 600
      - skill: verify-behavior
        behavior: .tinyagent/behavior.yaml
```

| Key | Where | Meaning |
|---|---|---|
| `version` | top level | Must be `1` |
| `skills_dir` | top level | Directory with the skill files, relative to the workflow file (default `skills`) |
| `default` | top level | Workflow or skill to run when routing finds no clear match |
| `defaults` | top level | Settings applied to every stage (see below) |
| `workflows.<name>.intent` | workflow | One sentence the router matches tasks against |
| `workflows.<name>.stages` | workflow | Ordered stages, at most 20 |
| `skill` | stage | Skill name (a file in `skills_dir`) |
| `check` | stage | Argv list run in `--cwd` without a shell; the stage passes when it exits 0 |
| `behavior` | stage | Behavior file path; the harness runs its cases and decides the stage on observed output. See [Verifying behavior](project-context.md#verifying-behavior). Can't be combined with `check` |
| `gate: spec` | stage | Reject a plan without acceptance criteria and a `Verify:` command, and re-plan once. See [Spec gate](project-context.md#spec-gate) |
| `inputs` | stage | Map of extra skill inputs, for example `contracts_dir: docs/contracts` |
| `model`, `profile` | `defaults` or stage | Model and config profile for the stage |
| `step_limit`, `cost_limit`, `token_limit`, `time_limit` | `defaults` or stage | Per-stage limits (defaults: 50 steps, otherwise your usual config) |
| `retries` | `defaults` or stage | Retries after a failed check (default 1, at most 5) |
| `check_timeout` | `defaults` or stage | Seconds for `check` or behavior cases (default 300) |

Unknown keys are rejected, so a typo fails at load time with exit 2.

### Skill files (Markdown)

Each skill is `<skills_dir>/<name>.md` or `<skills_dir>/<name>/SKILL.md`. Names use lowercase letters, digits, `.`, `_` and `-`. A skill needs a heading, an intent line and an inputs list. It also needs a success check unless its stage has a `check` command:

```markdown
# Skill: plan-tasks
> Intent: Break a spec into small, ordered tasks before writing any code.

**Inputs**
- spec
- repo_path

**Success check:** every task has acceptance criteria and a verification step, in dependency order.
```

**Markdown and backticks.** The whole skill file (frontmatter removed) is sent to the model as written, including backticks:

- Inline code spans (`` `go test ./...` ``) are kept in the heading, the `Intent:` line, input descriptions and the `Success check:` line. Bold around a span is removed and the backticks are kept, so `` **`make test`** passes `` becomes `` `make test` passes ``. Input names may be written in backticks (`` - `spec`: the task ``).
- Fenced code blocks (three or more `` ` `` or `~`, with an optional info string such as ```` ```sh ````) are passed through verbatim. Lines inside a fence are never read as the skill's heading, intent, inputs or success check, so a `# comment`, a `- item` or a sample `Success check:` inside a fence is safe.
- The task (`-t`) and the previous stage's output are passed to each stage as an argument, never through a shell, so backticks in them are not run as commands. `check` is an argv list, also run without a shell.

These inputs are filled automatically: `task` and `spec` (the `-t` task), `repo_path` and `cwd` (the absolute `--cwd`), and `previous_output` or the previous skill's name (the previous stage's output). Other inputs come from the stage's `inputs:` map, or are marked as not provided.

### Paths

All project paths are relative to the current directory:

- **The workflow file** given to `--workflow` resolves from the directory you run tinyagent in. Keep it in the project, for example `--workflow .tinyagent/workflows/workflow.yaml` or `--workflow docs/workflows/workflow.yaml`.
- **`skills_dir`** resolves from the workflow file's directory, so a workflow and its skills move together.
- **`check` commands, `behavior:` files and stage runs** use `--cwd` (default `.`, the current directory). A behavior file must stay inside `--cwd`.
- **Observer files** from `--workflow-observe` go to `.tinyagent/observer/` in `--cwd`.

## Routing and matching

With `--workflow-name <name>`, that workflow, or a single skill with that name, runs directly.

Without it, the router compares the task's content words with each workflow's `intent` and each skill's `Intent:` line:

1. The single best match that shares at least **2** distinct content words with the task runs.
2. If nothing matches, or two candidates tie, the file's `default` runs.
3. With no `default`, tinyagent exits 2 before any model call and suggests `--workflow-name`.

The chosen route (`kind` `workflow` or `skill`, how it was picked (`named`, `intent` or `default`) and its score) is printed on stderr and recorded in the `-o` record and the `workflow.started` event.

## Running stages

- Each stage is a fresh tinyagent run in `--cwd`. It gets the original task, the previous stage's output and the skill's instructions.
- **Success checks.** A stage with `check` passes when the command exits 0. Otherwise the model must end with `SUCCESS CHECK: PASS` or `SUCCESS CHECK: FAIL - <reason>`. The last such line decides, and a missing line fails the stage. A run that stops without submitting (for example at a limit) also fails.
- **Retries.** A failed stage is retried with the failure reason in its prompt. When its retries run out, the workflow stops. Later stages don't run, and earlier stages are not re-run.
- **Blocked or Failed.** A stage whose check keeps failing on its merits ends the workflow **Blocked** (exit 5). A run that broke ends **Failed** (exit 1). See [Blocked tasks](project-context.md#blocked-tasks).
- **Verify before stop** is on for every stage; see [Default on for workflow runs](verify.md#default-on-for-workflow-runs).

## Flags and settings

| Flag | Meaning |
|---|---|
| `--workflow <file.yaml>` | Run the workflow file on the `-t` task. Required for every flag below except `--skills-validate` |
| `--workflow-name <name>` | Run this workflow or skill instead of routing by intent |
| `--workflow-observe` | After a `Failed` or `Blocked` run, propose one project rule; see [below](#learning-from-failures---workflow-observe) |
| `--skills-validate <path>` / `tinyagent skills validate <path>...` | Check skill files, skills directories or workflow files with the same rules `--workflow` uses, then exit. No model call. Exit 0 all valid, 1 invalid, 2 missing or unsupported path |
| `-t, --task` | The task (required). `-t -` reads it from stdin |
| `-o, --output <path>` | Write the workflow record (`"kind": "workflow"`), with per-stage trajectories and logs under `<path>.stages/` |
| `--json` | Print JSONL events; see [below](#output-json-events-and-exit-status) |
| `--cwd <dir>` | Project directory for stages and checks (default `.`) |
| `--verify-on-stop=false` | Turn off the per-stage verify guard, which is on by default for workflows |

Every other flag you pass, such as `-m`, `--sandbox`, `--cost-limit` or `--profile`, is forwarded to every stage, and stage settings in the workflow file override it.

**Config keys.** `tinyagent.yaml` has no workflow-specific keys. Workflow settings live in the workflow file ([table above](#workflow-file-yaml)). The stages use your normal [config](config.md), so `model`, `profiles`, `sandbox`, `verify_on_stop` and others apply to every stage. A stage's `profile:` selects one of your config profiles.

All workflow flags are also listed in [docs/subcommands.md](subcommands.md#flags).

## Learning from failures (`--workflow-observe`)

`--workflow-observe` is off by default. After a run that ends `Failed` or `Blocked`, it turns the failure into a proposed [project rule](ratchet.md#observer-proposals):

```sh
tinyagent --workflow docs/workflows/workflow.yaml -t "add CSV export" --workflow-observe
# observer: proposed project rule p-3f2a9c1b7d4e for stage generate-code in .tinyagent/observer/proposals.jsonl; apply it with:
#   tinyagent ratchet add '...' --incident '...' --cwd '...'
```

- The first time a failure signature is seen, it appends one proposal to `.tinyagent/observer/proposals.jsonl`. The proposal includes the stage, the check argv, a redacted output excerpt and a ready-to-run `tinyagent ratchet add` command.
- When the same failure happens again, it bumps a count in `.tinyagent/observer/ledger.jsonl` and warns `gate did not hold`.
- It never applies a rule and makes no model call. Passed and interrupted runs write nothing.

The signatures and files are described in full in [Learning from failures](project-context.md#learning-from-failures).

## Output, JSON events and exit status

Progress lines go to stderr, and the last stage's output goes to stdout. With `--json`, tinyagent prints these JSONL events (see [JSON output](json-output.md#machine-readable-output)):

| Event | When |
|---|---|
| `workflow.started` | After routing, with the route |
| `stage.started` | Each stage attempt, with its input |
| `stage.gate` | A `gate: spec` decision: `attempt`, `passed`, `reason`, `missing` |
| `stage.finished` | Each attempt: output, `check`, and `unverified: true` when it ended with untested changes |
| `workflow.finished` | `status` `Passed`, `Failed`, `Blocked` or `Interrupted`, plus `failed_stage` and `reason` |
| `observer.proposed` / `observer.recurrence` | With `--workflow-observe`, after a `Failed` or `Blocked` run |

| Exit | Meaning |
|---|---|
| 0 | Every stage passed |
| 1 | A stage failed |
| 2 | Invalid workflow or skill file, routing error or conflicting flag (before any model call) |
| 5 | A stage is blocked |
| 130 | Interrupted |

## Interaction with other features

- **Chat.** `tinyagent chat` / `-i` can't be combined with `--workflow` (exit 2). Workflows are unattended. Use chat to design or debug a skill, then run it with `--workflow`.
- **One-run flags.** `--workflow` can't be combined with `--resume`, `--rewind`, `--review`, `--worktree`, `--acp`, `--eval`, `--ask-user`, `--attempts`, `--output-schema`, `--image`, `--plan-file`, `--check`, `--triage` or `--skills-validate` (exit 2).
- **Agent Skills (`--skills`)** are a different format and aren't checked by `skills validate`. They still work inside stages if you pass `--skills`.
- **AGENTS.md, project rules (`ratchet`) and memory** apply to every stage like any run in `--cwd`.
- **Verify before stop** is on for every stage; see [verify.md](verify.md#default-on-for-workflow-runs).

## Troubleshooting

| Symptom | Fix |
|---|---|
| `error: --workflow needs a task (-t)` | Pass `-t "..."` or `-t -` |
| `error: --workflow-name applies only to --workflow` (also `--workflow-observe`) | Add `--workflow <file.yaml>` |
| `workflow ...: version must be 1` or an unknown-field error | Fix the YAML; run `tinyagent skills validate <file.yaml>` |
| Routing error suggesting `--workflow-name` | The task matched no intent (or tied). Name the workflow, add a `default:` or sharpen the `intent` lines |
| `skills_dir: ...` or missing skill | `skills_dir` is relative to the workflow file, not the current directory |
| A fenced example in a skill was read as its inputs or check (before the Unreleased fix) | Upgrade; lines inside ```` ``` ```` or `~~~` fences are now ignored by the skill parser |
| A `check` fails with "not found" | `check` is argv run without a shell, in `--cwd`. Use `[sh, -c, "..."]` for pipes |
| `error: --workflow can't be combined with -X` | Drop that flag; see [Interaction](#interaction-with-other-features) |
| Stage keeps ending unverified | Have the stage run tests, or pass `--verify-on-stop=false` |
| Run ends `Blocked` (exit 5) | The task can't be met as written. Fix the task or acceptance criteria and re-run; add `--workflow-observe` to get a proposed rule |

## Examples

```sh
# Route by intent, sandboxed, with a cost cap forwarded to every stage
tinyagent --workflow docs/workflows/workflow.yaml -t "add a --since flag to log" --sandbox --cost-limit 2

# Run one skill on its own
tinyagent --workflow docs/workflows/workflow.yaml --workflow-name plan-tasks -t "add CSV export"

# CI: machine-readable events, record, and proposed rules on failure
tinyagent --workflow .tinyagent/workflows/ci.yaml -t - --json -o run.json --workflow-observe < task.txt

# Pre-commit: validate every workflow file and skill
tinyagent skills validate .tinyagent/workflows/
```

A minimal single-stage workflow:

```yaml
version: 1
default: fix
workflows:
  fix:
    intent: Fix a failing test.
    stages:
      - skill: fix-test
        check: [go, test, ./...]
        retries: 2
```

**Trust:** skills are prompt input, and `check` commands run with your permissions. Only run workflow files you trust.
