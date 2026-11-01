# Setting up workflows: a guide with examples

This page shows how to set up a [skill workflow](workflows.md) from scratch. It has three worked examples: simple, medium and complex. [workflows.md](workflows.md) is the full reference. For single-run checklists, and for when to use a playbook instead of a workflow, see [Setting up playbooks](playbook-guide.md); a playbook can also hand a task to a workflow with `workflow:`.

- [Setup](#setup)
  - [Where the files live](#where-the-files-live)
  - [Required and defaulted fields](#required-and-defaulted-fields)
  - [Running a workflow](#running-a-workflow)
- [Examples](#examples)
  - [1. Simple: one stage](#1-simple-one-stage)
  - [2. Medium: a few stages with checks and retries](#2-medium-a-few-stages-with-checks-and-retries)
  - [3. Complex: plan, code, vet, test, verify behavior](#3-complex-plan-code-vet-test-verify-behavior)
  - [Which level to use](#which-level-to-use)
- [Limits worth knowing](#limits-worth-knowing)

Every file on this page is checked by `TestWorkflowGuideExamples` (`internal/harness/guide_test.go`): each workflow must load with the real loader, and the behavior file must parse. A block preceded by `<!-- file: ... -->` is one of those files.

## Setup

### Where the files live

A workflow is one YAML file plus a directory of skill files. You can put the YAML anywhere and pass it with `--workflow <file>`. Skills are read from `skills_dir`, which is resolved relative to the workflow file and defaults to `skills`. A skill is `skills/<name>.md` or `skills/<name>/SKILL.md`, and its name is the file stem or folder name.

```text
.tinyagent/
  workflow.yaml
  skills/
    fix-bug.md
```

### Required and defaulted fields

**Workflow file.** Unknown keys are an error. All problems are reported at once, before any model call.

| Key | Required | Default / rule |
|---|---|---|
| `version` | yes | must be `1` |
| `workflows.<name>.stages` | yes | 1 to 20 stages. Names are lowercase letters, digits, `.`, `_` or `-`, and can't match a skill name |
| `stages[].skill` | yes | must name a skill in `skills_dir` |
| `skills_dir` | no | `skills` |
| `default` | no | the workflow or skill to use when routing finds no match |
| `intent` | no | text for [routing](workflows.md#routing-and-matching) |
| `check` | see below | argv that decides the stage (exit 0 means pass); runs without a shell |
| `behavior` | no | path under `--cwd` for behavior cases; can't be combined with `check` |
| `gate` | no | only `spec` |
| `inputs` | no | fixed `name: value` strings handed to the stage |
| `retries` | no | `1` (0–5) |
| `step_limit` | no | `50` |
| `check_timeout` | no | `300` seconds |
| `cost_limit`, `token_limit`, `time_limit`, `model`, `profile` | no | inherited from the run |

The settings rows (from `retries` down) can also go under top-level `defaults:`. A stage value overrides `defaults:`.

**Skill file.** A skill needs three things:

- a `# Skill: <name>` heading (or frontmatter `name:`)
- a `> Intent: ...` line (or frontmatter `description:`)
- an `**Inputs**` bullet list with at least one item. Each item is a name from: `task` or `spec` (the task), `repo_path` or `cwd`, `previous_output`, the previous stage's skill name, or a key from the stage's `inputs:`

A stage also needs a way to decide pass or fail. Use one of:

- a `**Success check:** ...` line in the skill. The model then ends its output with `SUCCESS CHECK: PASS` or `SUCCESS CHECK: FAIL - <reason>`.
- `check:` on the stage
- `behavior:` on the stage

`task` and `previous_output` are always handed to the stage, even when not listed.

### Running a workflow

```sh
tinyagent skills validate .tinyagent/workflow.yaml     # validate only: no model call, no credentials
tinyagent --workflow .tinyagent/workflow.yaml -t "..."  # route by intent (or use default)
tinyagent --workflow .tinyagent/workflow.yaml --workflow-name fix -t "..."   # pick one workflow
tinyagent --workflow .tinyagent/workflow.yaml -t "..." -o run.json            # keep stage logs in run.json.stages/
tinyagent --workflow .tinyagent/workflow.yaml -t "..." --workflow-observe     # propose a project rule on failure
```

- `-t` is required.
- Stages run in `--cwd`, and every other flag you set (for example `--sandbox` or `--cost-limit`) is forwarded to each stage.
- `--resume`, `--ask-user`, `--worktree`, `--dry-run` and `--review` can't be combined with `--workflow`.
- `--workflow` doesn't need `--trust-config`.
- Exit codes: 0 Passed, 1 Failed, 5 Blocked. See [Output, JSON events and exit status](workflows.md#output-json-events-and-exit-status).

## Examples

### 1. Simple: one stage

**When to use it:** you want one skill run with a hard pass/fail check, and you don't want a pipeline.

<!-- file: simple/workflow.yaml -->
```yaml
version: 1
workflows:
  fix:
    stages:
      - skill: fix-bug
        check: [go, test, ./...]
```

<!-- file: simple/skills/fix-bug.md -->
```markdown
# Skill: fix-bug
> Intent: Fix the bug described in the task with the smallest change.

**Inputs**
- task

**Success check:** `go test ./...` passes.

Reproduce the bug with a failing test, fix the cause, and run the tests again.
```

Run it with `tinyagent --workflow simple/workflow.yaml -t "the parser panics on empty input"`. With a single workflow you don't need `--workflow-name`, as long as the task matches its intent or the workflow is the `default`.

Even smaller: delete the `check:` line. The model's `SUCCESS CHECK:` line then decides the stage, based on the skill's `Success check:` line.

### 2. Medium: a few stages with checks and retries

**When to use it:** a short sequence where each step has its own command check, such as code, then vet, then test, with more retries on the flaky step.

<!-- file: medium/workflow.yaml -->
```yaml
version: 1
default: change
defaults:
  retries: 1
  step_limit: 60
workflows:
  change:
    intent: Make a code change, then vet and test it.
    stages:
      - skill: write-code
        step_limit: 100
      - skill: vet
        check: [go, vet, ./...]
      - skill: test
        check: [go, test, -count=1, ./...]
        retries: 2
        check_timeout: 600
```

<!-- file: medium/skills/write-code.md -->
```markdown
# Skill: write-code
> Intent: Implement the change the task asks for.

**Inputs**
- task
- repo_path

**Success check:** the change is implemented and `go build ./...` succeeds.

Make the smallest change that does what the task asks. End with a short summary of the files you changed.
```

<!-- file: medium/skills/vet.md -->
```markdown
# Skill: vet
> Intent: Fix every go vet finding in the change.

**Inputs**
- write-code

**Success check:** `go vet ./...` reports nothing.
```

<!-- file: medium/skills/test.md -->
```markdown
# Skill: test
> Intent: Run the tests and fix failures caused by the change.

**Inputs**
- vet

**Success check:** `go test -count=1 ./...` passes.
```

How it runs:

- `write-code` has no `check:`, so its own `SUCCESS CHECK:` line decides it.
- The `vet` and `test` stages are decided by their commands.
- `test` gets 3 attempts in total, and each retry sees why the previous attempt failed.
- The `- write-code` input under `vet` receives the previous stage's output.

### 3. Complex: plan, code, vet, test, verify behavior

**When to use it:** you're implementing a feature from a spec and want planning checked before any code is written, plus checks that the built program behaves as specified, not only that the tests pass. This is a trimmed copy of the shipped [`docs/workflows/workflow.yaml`](workflows/workflow.yaml); see [its skills](workflows/skills) for the full instructions.

<!-- file: complex/workflow.yaml -->
```yaml
version: 1
default: implement
defaults:
  retries: 1
  step_limit: 50
workflows:
  implement:
    intent: Implement a feature from a spec, then validate, test and verify its behavior.
    stages:
      - skill: plan-tasks
        gate: spec                  # reject plans without acceptance criteria / Verify lines
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

<!-- file: complex/skills/plan-tasks.md -->
````markdown
# Skill: plan-tasks
> Intent: Break a spec into small, ordered tasks before writing any code.

**Inputs**
- spec
- repo_path

**Success check:** every task has acceptance criteria and a verification step, in dependency order.

Write each task in this shape:

```markdown
Task 1: <short title>
Acceptance criteria:
- <an observable outcome>
Verify: `go test ./...`
```
````

<!-- file: complex/skills/generate-code.md -->
```markdown
# Skill: generate-code
> Intent: Implement the planned tasks in order.

**Inputs**
- plan-tasks
- repo_path

**Success check:** every planned task is implemented and its Verify command passes.
```

<!-- file: complex/skills/validate-diff.md -->
```markdown
# Skill: validate-diff
> Intent: Review the diff and fix vet findings.

**Inputs**
- generate-code

**Success check:** `go vet ./...` reports nothing.
```

<!-- file: complex/skills/run-tests.md -->
```markdown
# Skill: run-tests
> Intent: Run the Go test suite and fix failing tests.

**Inputs**
- validate-diff

**Success check:** `go test -count=1 ./...` passes.
```

<!-- file: complex/skills/verify-behavior.md -->
```markdown
# Skill: verify-behavior
> Intent: Run the built program and check its observed output against the task's behavioral acceptance criteria.

**Inputs**
- task
- repo_path

Write one case per behavioral acceptance criterion. If there are none, write no file and the stage is skipped. Don't change product code.
```

The `verify-behavior` stage writes `.tinyagent/behavior.yaml` (relative to `--cwd`) while it runs. The harness then runs each case without a shell. A run of it might produce:

<!-- file: complex/behavior.yaml -->
```yaml
version: 1
cases:
  - name: greets-by-name
    criterion: the CLI prints hello for --name world
    run: [go, run, ./cmd/app, --name, world]
    timeout: 60
    expect:
      exit_code: 0
      stdout_contains: ["hello world"]
      stdout_not_contains: [panic]
```

How it runs:

- The spec gate checks `plan-tasks`' output. If the plan is rejected, the stage is re-planned once with a full retry budget, and then the run stops.
- A stage whose check keeps failing on its merits ends the run **Blocked** (exit 5).
- See [Spec gate](project-context.md#spec-gate) and [Verifying behavior](project-context.md#verifying-behavior).

### Which level to use

| Level | Use when | Cost |
|---|---|---|
| Simple | one task type, one check | one agent run (plus retries) |
| Medium | a fixed code → vet → test sequence | one run per stage |
| Complex | specs that need checked planning and behavior evidence | five runs or more; set per-stage `cost_limit` |

## Limits worth knowing

These follow from the code on main (`internal/harness`):

- **Linear only.** Stages run in order. There are no branches, loops, conditions or parallel stages. The only repeats are retries and the spec gate's single re-plan.
- **Agent stages only.** Every stage is a model run. `check:` and `behavior:` only decide pass or fail; there is no shell-only stage.
- **Previous output only.** A stage sees the task, `--cwd`, its fixed `inputs:` and the previous stage's output. It can't read the output of earlier stages, except through files they wrote in `--cwd`.
- **Stops at the first failed stage.** Later stages don't run.
- **No resume.** `--resume` is refused. Re-running starts again from stage 1 in the same `--cwd`, so earlier file changes are still there.
- **Per-stage limits only.** `step_limit`, `cost_limit`, `token_limit` and `time_limit` apply to each stage's run. The total `cost_usd` is reported but not capped, and there is no timeout for the whole workflow. Ctrl-C ends the run as `Interrupted`.
- **No human approval step.** `--ask-user` is refused; stages run unattended.
