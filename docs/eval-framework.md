# TinyAgent eval framework (`tinyagent --eval`)

> **Status: experimental; v1 implemented.** `tinyagent --eval suite.yaml --eval-out dir` runs the suite sequentially (code in `internal/eval/` and `cmd/tinyagent/eval.go`). What v1 ships and how it differs from the target design below:
>
> - **Shipped:** strict `version: 1` suite files (`version`, `models`, `profiles`, `trials`, and per task `id`, `repo`, `base`, `task`/`task_file`, `model`, `profile`, `checks`, `checks_on_limit`, `limits`, `tags`); a detached worktree per trial at `base`, removed afterwards (also on Ctrl-C) unless `--eval-keep`; one child tinyagent run per trial with `-o` written to `<eval-out>/trajectories/<trial_id>.json` and its output to `logs/<trial_id>.log`; `results.jsonl`, `report.json`, `report.md` (pass rate, pass@k and pass^k, cost, tokens, steps, wall time, outcome counts, per-task passes); suite-level `--cost-limit` / `--time-limit`; `--dry-run`; `--eval-min-pass-rate` (exit 1); per-task `limits.token_limit`, forwarded to each trial as `--token-limit`. Other flags on the command line (e.g. `-c`, `--no-stream`, `--ask-user`, `--step-limit`) are forwarded to every trial.
> - **GitHub Action:** experimental `eval`, `eval-out`, `eval-min-pass-rate`, `cost-limit` and `time-limit` inputs use the verified installer and expose report paths; see §9.
> - **Differences:** `trials` is required. `--time-limit` takes seconds (as in single runs), not `30m`. Check output kept in `results.jsonl` is capped to the last 4096 bytes. Checks run on the host in the worktree. A trial's `--profile` is passed only for names other than `default` (unless the config defines a profile called `default`).
> - **Not yet implemented:** `--eval-jobs` (trials are sequential), `--json` eval events, `output_schema` / `expected_json` (rejected as unknown keys), `profile_digest`.

## Contents

1. [What it does and why](#1-what-it-does-and-why)
2. [Concepts and components](#2-concepts-and-components)
3. [Quick start](#3-quick-start)
4. [CLI flags](#4-cli-flags)
5. [Suite file reference](#5-suite-file-reference)
6. [Results schema (`results.jsonl`, schema_version 1)](#6-results-schema-resultsjsonl-schema_version-1)
7. [Reports and metrics](#7-reports-and-metrics)
8. [Writing good tasks and checks](#8-writing-good-tasks-and-checks)
9. [CI usage and pass-rate gating](#9-ci-usage-and-pass-rate-gating)
10. [Cost control, security and privacy](#10-cost-control-security-and-privacy)
11. [Troubleshooting](#11-troubleshooting)
12. [Implementation guide for contributors](#12-implementation-guide-for-contributors)
13. [Open questions](#13-open-questions)

---

## 1. What it does and why

Agent runs are not deterministic, so one run tells you little. Today, answering "does model X, profile Y, or my new AGENTS.md / skill / hook make tinyagent better or worse on *my* repo?" means hand-writing a shell loop around `tinyagent`, `git worktree` and `jq`.

The planned eval mode, `tinyagent --eval <suite.yaml>`, turns tinyagent's existing single-run primitives (`--worktree`, `-o` trajectories, `--json`, `--output-schema`, stop hooks, `--profile`, the price table and the GitHub Action) into a measurement tool:

- It runs a list of **tasks** from a declarative suite file.
- Each **trial** runs in an isolated Git worktree at a pinned commit.
- After each run, the task's **check** commands (for example, tests) grade the result.
- It repeats the **matrix** you ask for (models × profiles × N trials).
- It writes one trajectory per trial, a JSONL results file, and a Markdown/JSON report with pass rate, pass@k, cost, tokens, steps, wall time and exit status for each configuration.

**Use cases**

- **Pick a model.** An individual developer compares `anthropic/...`, `openai/...` and an Ollama model on their own repo, or checks whether a cheaper model is good enough (cost per solved task).
- **Catch regressions before release.** A tinyagent maintainer runs a small fixed suite to catch prompt, tool or compaction changes that quietly make the agent worse.
- **Gate configuration changes.** A team runs evals in CI with the existing GitHub Action after editing AGENTS.md, skills, `command_tools` or hooks, and fails the job below a pass-rate threshold.

**Non-goals (v1)**

- Bundling or downloading public benchmarks such as SWE-bench. An adapter could be a separate follow-up.
- LLM-as-judge grading. Checks are commands only, so grading is deterministic and costs nothing.
- Remote or cloud execution, a leaderboard service, or a web UI.
- Interactive approval. Eval mode takes no input; an `ask_user` question ends the trial as `NeedsInput`, which counts as not passed.
- Changing single-run behavior. Without `--eval`, nothing changes.

## 2. Concepts and components

| Concept | Meaning |
|---|---|
| **Suite file** | A YAML file (`version: 1`) with strict keys, like the tinyagent config. It declares the matrix and the tasks. |
| **Task** | One unit of work: a `repo` (local path), a `base` commit, the task text (`task` or `task_file`), `checks`, and optional overrides (`profile`, `model`), `output_schema` / expected-JSON assertions, and per-task limits. |
| **Matrix** | `models` × `profiles` × `trials`, applied to every task. 2 models × 1 profile × 3 trials × 2 tasks = 12 trials. |
| **Configuration** | One (model, profile) pair. Metrics are reported per configuration (and per task). |
| **Trial** | One tinyagent run of one task under one configuration, identified by a trial id. |
| **Worktree isolation** | Each trial gets its own Git worktree checked out at the task's `base`. Your working tree and current branch are never touched. Worktrees are removed after the trial (also on Ctrl-C) unless `--eval-keep` is set. |
| **Checks** | Argv commands (no shell) with timeouts, run after the agent's run ends, in the trial's worktree. Every check must exit 0 for the trial to pass. Checks are separate from stop hooks: stop hooks still run *inside* the trial as usual. |
| **Grading** | A trial **passes** when the run ends `Submitted` or `Completed` **and** every check exits 0 (and, if configured, the expected-JSON assertions hold). |
| **Budgets** | Suite-level `--cost-limit` / `--time-limit`. Once spent, no new trials start, in-flight trials finish, and remaining trials are marked `skipped`. |
| **Thresholds** | `--eval-min-pass-rate`: the process exits non-zero if any configuration's pass rate is below it, and the report names the failing configuration. |

### Outcomes

Every trial gets exactly one outcome:

| Outcome | When | Counts as passed? |
|---|---|---|
| `passed` | Run ended `Submitted` or `Completed` and all checks exited 0. | Yes |
| `failed_check` | Run ended `Submitted`/`Completed` but at least one check exited non-zero (or timed out). The check's exit code and capped output are recorded. | No |
| `limit` | The run hit a limit (`StepLimitExceeded`, `CostLimitExceeded`, `TimeLimitExceeded`). Checks still run and are recorded when `checks_on_limit` is true (the default). | No |
| `NeedsInput` | The agent asked the user a question (`ask_user`). Eval mode never answers. | No |
| `error` | Any other non-success end (for example a non-retryable API error, or other tinyagent exit statuses such as `Interrupted`, `RepeatedFormatError`, `InvalidOutput`, `VerificationFailed`, `OutputTruncated`), or a harness failure such as worktree creation failing. The raw exit status is always recorded too. | No |
| `skipped` | Not started because a suite budget was exhausted (or the run was interrupted first). | No; reported separately |

> The draft names `passed`, `failed_check`, `limit`, `NeedsInput` and `skipped` explicitly and says exit status, limits and errors are recorded as distinct outcomes. Mapping every other tinyagent exit status to `error` is a **Recommendation**, not settled design.

### Outputs

All under `--eval-out <dir>`:

- **Per-trial trajectory**: one trajectory JSON per trial (the same format as `-o`), named by trial id so parallel trials never collide.
- **`results.jsonl`**: one stable-schema record per trial (see [§6](#6-results-schema-resultsjsonl-schema_version-1)).
- **`report.md`** and **`report.json`**: per-configuration pass rate, pass@k, mean and total cost, tokens, steps and time, plus `usage_by_role` (and a "Usage by role" table in `report.md`) totalling the trials' per-role usage when any trial routed work to a helper or child.

On stderr, eval mode prints one line per finished trial and a final summary table.

### `--json` events

With `--json`, eval mode emits three new event types alongside the existing per-run events, each per-run event tagged with its trial id:

- `eval.trial.started`
- `eval.trial.result`
- `eval.summary`

### `--dry-run`

Validates the suite, prints the expanded matrix and an upper-bound cost estimate (from the price table), and exits. It creates no worktree and makes no provider request.

## 3. Quick start

*(Target workflow; does not work yet.)*

1. **Pick a commit** where the problem exists, e.g. a commit just before a bug fix: `git rev-parse --short HEAD~1` → `1a2b3c4`.
2. **Write the task text** in `evals/tasks/fix-parse-bug.md`, describing the problem as a user would, without revealing the fix.
3. **Write a check** that passes only when the problem is solved, e.g. `go test ./internal/config/...`. Verify it fails at `base` and passes on the known fix.
4. **Write the suite** `evals/suite.yaml`:

   ```yaml
   version: 1
   models: [anthropic/claude-sonnet-4-5, openai/gpt-5-mini]
   profiles: [default, ci]
   trials: 3
   tasks:
     - id: fix-parse-bug
       repo: .
       base: 1a2b3c4
       task_file: evals/tasks/fix-parse-bug.md
       checks:
         - argv: [go, test, ./internal/config/...]
           timeout_seconds: 300
   ```

5. **Dry-run** to validate and see the matrix and cost upper bound:

   ```sh
   tinyagent --eval evals/suite.yaml --dry-run
   ```

6. **Run** with a budget:

   ```sh
   tinyagent --eval evals/suite.yaml --eval-out evals/out --eval-jobs 2 --cost-limit 5
   ```

7. **Read** `evals/out/report.md`; dig into failures through `results.jsonl` and the per-trial trajectories.

### More example commands

```sh
# Gate in CI: non-zero exit if any configuration passes < 80%
tinyagent --eval evals/suite.yaml --eval-out out --eval-min-pass-rate 0.8

# Keep worktrees for debugging failing trials
tinyagent --eval evals/suite.yaml --eval-out out --eval-keep

# Machine-readable event stream
tinyagent --eval evals/suite.yaml --eval-out out --json > events.jsonl

# Wall-clock budget for the whole suite
tinyagent --eval evals/suite.yaml --eval-out out --time-limit 30m
```

## 4. CLI flags

| Flag | Value | Default | Meaning (target design) |
|---|---|---|---|
| `--eval` | path | — | Run the suite file in eval mode. Without it, nothing changes. |
| `--eval-out` | dir | not specified by the draft | Output directory for trajectories, `results.jsonl`, `report.md`, `report.json`. **Recommendation:** require it (exit 2 if missing) rather than guessing a location. |
| `--eval-jobs` | int | `1` | Maximum trials running concurrently. Worktrees and trajectories never collide. |
| `--eval-keep` | bool | off | Keep trial worktrees instead of removing them. |
| `--cost-limit` | USD | — | In eval mode: suite-level budget. When reached, no new trials start; in-flight trials finish; the rest are `skipped`. |
| `--time-limit` | duration | — | In eval mode: suite-level wall-clock budget, same semantics. |
| `--eval-min-pass-rate` | 0–1 | — | Exit non-zero if any configuration's pass rate is below this. |
| `--dry-run` | bool | off | Validate, print matrix and upper-bound cost estimate; no worktree, no model request. |
| `--json` | bool | off | Emit `eval.trial.started` / `eval.trial.result` / `eval.summary` alongside existing per-run events, tagged with a trial id. |

Per-trial limits such as `--step-limit` are set per task in the suite (see `limits` below). How single-run `--cost-limit`/`--time-limit` semantics coexist with the suite-level meaning is an [open question](#13-open-questions).

Exit codes follow tinyagent's convention (2 is reserved for usage errors): **2** for an invalid suite or flags, before any model request; non-zero when the `--eval-min-pass-rate` threshold is not met. The exact code for a threshold failure is not fixed by the draft (**Recommendation:** 1).

## 5. Suite file reference

Strict keys: any unknown key, at any level, is an error. The suite is fully validated before any worktree is created or model request is made; failures exit 2 and name the offending key.

### Top level

| Key | Type | Required | Default | Notes |
|---|---|---|---|---|
| `version` | int | yes | — | Must be `1`. |
| `models` | list of strings | yes* | — | Model names as accepted by `--model` (e.g. `anthropic/claude-sonnet-4-5`). *A task-level `model` override can stand in. |
| `profiles` | list of strings | no | **Recommendation:** `[default]` | Profile names from your tinyagent config. |
| `trials` | int ≥ 1 | **yes** | none | Must be explicit; trials multiply spend. |
| `tasks` | list of task | yes | — | At least one. |

### Task

| Key | Type | Required | Default | Notes |
|---|---|---|---|---|
| `id` | string | yes | — | Unique within the suite. **Recommendation:** `[a-z0-9-]+`, since it appears in file names and trial ids. |
| `repo` | path | yes | — | Local Git repository, relative to the suite file or absolute. Non-Git directories are an open question. |
| `base` | commit-ish | yes | — | Commit each trial's worktree is checked out at. **Recommendation:** a full or short SHA, not a branch. |
| `task` | string | one of | — | Inline task text. |
| `task_file` | path | one of | — | File containing the task text. Exactly one of `task` / `task_file`. |
| `profile` | string | no | matrix | Overrides the matrix profile(s) for this task. |
| `model` | string | no | matrix | Overrides the matrix model(s) for this task. |
| `checks` | list of check | yes (**Recommendation**) | — | Without checks, "passed" would only mean "the run ended". |
| `checks_on_limit` | bool | no | `true` | Run checks even when the trial ended on a limit. |
| `output_schema` | path | no | — | JSON Schema for the final result (as `--output-schema`). |
| `expected_json` | object | no | — | Expected-JSON assertions on the structured result. Key name and matching semantics are not fixed by the draft (**Recommendation:** subset match). |
| `limits` | map | no | profile/config | Per-task limits. **Recommendation:** keys mirroring existing flags: `step_limit`, `cost_limit`, `token_limit`, `time_limit`. |

### Check

| Key | Type | Required | Default | Notes |
|---|---|---|---|---|
| `argv` | list of strings | yes | — | Executed directly, **no shell**. `argv[0]` is the program. |
| `timeout_seconds` | int | no | not specified (**Recommendation:** 300) | A timeout counts as a failing check. |

### Annotated example

```yaml
# evals/suite.yaml — target format (v1 does not support output_schema yet)
version: 1                       # required; only 1 is valid

models:                          # every task runs on each model...
  - anthropic/claude-sonnet-4-5
  - openai/gpt-5-mini
profiles: [default, ci]          # ...under each profile...
trials: 3                        # ...this many times. Required, no default.
# => per task: 2 models x 2 profiles x 3 trials = 12 trials

tasks:
  - id: fix-parse-bug            # unique; used in trial ids and file names
    repo: .                      # local Git repo
    base: 1a2b3c4                # pinned commit for every trial's worktree
    task_file: evals/tasks/fix-parse-bug.md
    checks:
      - argv: [go, test, ./internal/config/...]   # argv, no shell
        timeout_seconds: 300
      - argv: [go, vet, ./...]
        timeout_seconds: 120
    checks_on_limit: true        # default; still grade trials that hit a limit

  - id: list-exported-funcs
    repo: .
    base: 1a2b3c4
    task: "List the exported functions in internal/config as JSON."
    profile: default             # override: this task ignores the matrix profiles
    output_schema: evals/schemas/funcs.json
    checks:
      - argv: [git, diff, --exit-code]           # read-only task: no edits allowed
        timeout_seconds: 30
```

### Validation rules

- Unknown key anywhere → exit 2, naming the key, before any model request.
- `version` must be `1`; `trials` must be present and ≥ 1.
- Exactly one of `task` / `task_file`; `task_file` and `output_schema` must exist.
- Task `id`s unique.
- Models must be known to the provider configuration; profiles must exist in the config.
- `checks[].argv` non-empty.
- **Recommendation:** resolve every `base` with `git rev-parse` during validation so a typo fails before spending money.

## 6. Results schema (`results.jsonl`, schema_version 1)

One JSON object per line, one line per trial (including skipped trials). The schema is experimental but versioned: new fields may be added; existing ones change only with a new `schema_version`.

The draft requires the record to carry the fields below; exact field names are a **Recommendation**.

| Field | Type | Description |
|---|---|---|
| `schema_version` | int | Always `1`. |
| `trial_id` | string | Unique trial id, also used in `--json` events, trajectory file names and (planned) `--log-db` rows. |
| `task_id` | string | Task `id`. |
| `trial` | int | Trial index within the configuration (1..`trials`). |
| `model` | string | Requested model. |
| `fallback_model` | string \| null | Fallback model actually used (via `fallback_models`), if any. |
| `provider` | string | Provider that served the run. |
| `profile` | string | Profile name. |
| `profile_digest` | string | Digest of the resolved profile, so changed config is detectable. |
| `tinyagent_version` | string | tinyagent version that ran the trial. |
| `base_commit` | string | Full SHA the worktree was created at. |
| `outcome` | string | `passed`, `failed_check`, `limit`, `NeedsInput`, `error` or `skipped`. |
| `exit_status` | string | Raw tinyagent exit status (e.g. `Submitted`, `StepLimitExceeded`). |
| `checks` | list | Per check: `argv`, `exit_code`, `timed_out`, `duration_ms`, capped `output`. |
| `cost_usd` | number | Run cost from the price table. |
| `input_tokens` / `output_tokens` | int | Token totals (cache read/write when non-zero, as in trajectories). |
| `usage_by_role` | list | The trajectory's per-role split (`role`, `task`, `model`, `requests`, tokens, `cost_usd`); only when a helper or delegated child made requests. See [Usage by role](models.md#usage-by-role). |
| `steps` | int | Model steps. |
| `wall_time_ms` | int | Trial wall time. |
| `trajectory` | string | Path of the trajectory, relative to `--eval-out`. |
| `error` | string \| null | Harness error message for `error` outcomes. |

Example (illustrative):

```json
{"schema_version":1,"trial_id":"fix-parse-bug.claude-sonnet-4-5.ci.2","task_id":"fix-parse-bug","trial":2,"model":"anthropic/claude-sonnet-4-5","fallback_model":null,"provider":"anthropic","profile":"ci","profile_digest":"sha256:…","tinyagent_version":"v0.x.y","base_commit":"1a2b3c4…","outcome":"failed_check","exit_status":"Submitted","checks":[{"argv":["go","test","./internal/config/..."],"exit_code":1,"timed_out":false,"duration_ms":8123,"output":"--- FAIL: …"}],"cost_usd":0.21,"input_tokens":48213,"output_tokens":3104,"steps":12,"wall_time_ms":102000,"trajectory":"trajectories/fix-parse-bug.claude-sonnet-4-5.ci.2.json","error":null}
```

Records contain no message content by default.

## 7. Reports and metrics

`report.md` (human) and `report.json` (machine) aggregate per configuration (model × profile) and per task:

- **Pass rate**: passed / attempted trials. **Recommendation:** exclude `skipped` from the denominator and report the skipped count beside it.
- **pass@k**: probability that at least one of k trials passes. Estimator choice is an open question.
- **Cost**: mean and total; **Recommendation:** also cost per passed trial.
- **Tokens, steps, wall time**: mean and total.
- **Outcome counts / exit status** breakdown.
- **Threshold result**: which configurations failed `--eval-min-pass-rate`.

## 8. Writing good tasks and checks

**Do**

- Pin `base` to a SHA where the task is genuinely unsolved; verify every check **fails at `base`** and **passes on a reference solution**.
- Write the task like a real request: the symptom and constraints, not the fix.
- Make checks objective and narrow: a specific package's tests or a dedicated regression test.
- Give every check a `timeout_seconds`.
- Use several trials (≥ 3) before drawing conclusions; runs are not deterministic.
- Keep suites small and focused; a few representative tasks you rerun often beat a large stale suite.
- Change one variable at a time (model, profile, or AGENTS.md), with everything else fixed.

**Don't**

- Don't put the answer, or the hidden test, in the task text.
- Don't write checks that pass at `base` (they measure nothing) or that depend on network, wall-clock or randomness.
- Don't use shell syntax in `argv` (`&&`, pipes, globs, `$VAR`) — there is no shell. Wrap complex logic in a script committed at `base` and call it: `argv: [./evals/check-foo.sh]`.
- Don't point `base` at a moving branch.
- Don't compare configurations from different suite versions or base commits.

## 9. CI usage and pass-rate gating

The experimental Action `eval` input runs the suite through the checksum-verified
installer and gates CI with `eval-min-pass-rate`. Use an Action ref and binary
release containing eval support (`v0.10.3` supports the CLI); `version: latest`
selects the latest binary when running the Action from `main`. Pin both for
reproducible CI after this Action change is released.

```yaml
on: pull_request
permissions:
  contents: read
jobs:
  eval:
    if: github.event.pull_request.head.repo.full_name == github.repository
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with: { fetch-depth: 0 }           # base commits must be reachable
      - uses: vicgupta/tinyagent@main
        id: eval
        with:
          version: latest
          eval: evals/suite.yaml
          eval-out: evals/out
          eval-min-pass-rate: '0.8'
          cost-limit: '5'
          time-limit: '600'
          job-summary: 'true'
        env:
          ANTHROPIC_API_KEY: ${{ secrets.ANTHROPIC_API_KEY }}
      - uses: actions/upload-artifact@v4
        if: always()
        with: { name: eval-results, path: evals/out }
```

Use `fetch-depth: 0` so every `base` exists; always set `cost-limit` (omitting it
allows unlimited spend). Do not give secrets to untrusted fork PRs or run their
suites with `pull_request_target`. Review suite changes before running them:
checks execute commands on the runner.

`eval` cannot be combined with `task`, `task-file`, `review-base`,
`review-commit`, `attempts`, `model` or `profile`; define the model/profile matrix
in the suite. `config` and trial flags in `args` are forwarded by the CLI.
`working-directory` selects the directory where the eval command runs, so
relative suite/output paths resolve there. `cost-limit` and `time-limit` are
suite budgets; both inputs are also available to ordinary task/review runs.

`eval-out` defaults to a fresh directory under `RUNNER_TEMP`. The `eval-out`
output gives its absolute path; `eval-report` gives `report.json` when present.
The `eval-results` output provides compact JSON with per-configuration
pass rates and costs (`configs`) and failing configurations (`below_threshold`). The directory also contains `results.jsonl`, `report.md`, logs
and trajectories. Artifact upload is the workflow's responsibility. Ordinary
result-event outputs (`status`, `submission`, `steps`, `cost-usd`, etc.) are empty
for eval runs. `exit-code` preserves the CLI exit status, including threshold
failure (1); `fail-on-error: false` tolerates failures except usage errors (2).

`job-summary: true` appends `report.md`, including on threshold failure, up to
1 MiB. Report text can contain suite-authored content; treat it as untrusted.
Use `args: --dry-run` to validate and print the trial matrix without model
requests; dry runs do not create report files. A missing or invalid suite fails
with exit 2 before any model request.

## 10. Cost control, security and privacy

**Cost.** Trials multiply spend: tasks × models × profiles × trials. Hence `trials` is required, `--dry-run` shows an upper-bound estimate from the price table, and `--cost-limit` caps the suite. Start with one model, one trial, `--dry-run`, then scale up. Watch provider rate limits (429s) and `codex` plan limits with `--eval-jobs > 1`; they interact with `fallback_models`, and the fallback actually used is recorded per trial.

**Security.**

- The **suite file is trusted**, like hooks: its checks run commands on your machine. Never run a suite you haven't reviewed.
- Checks run as **argv with no shell**.
- **Task text is untrusted model input**, as in any run.
- `redact_secrets`, `command_env`, `file_access`, `--sandbox` / `--env container` apply inside trials exactly as for single runs.
- Whether checks run on the host or in the configured execution environment is an open question.

**Privacy.** `results.jsonl` and reports contain no message content by default. Per-trial trajectories follow the existing trajectory rules (they do contain the conversation); treat `--eval-out` accordingly before sharing.

## 11. Troubleshooting

*(Expected behavior of the target design.)*

| Symptom | Likely cause / fix |
|---|---|
| Exit 2 immediately, naming a key | Unknown or misspelled suite key (strict keys). Fix the key. |
| Every trial is `failed_check`, even good-looking ones | Check is wrong or environment-dependent. Run it by hand in a worktree at `base` plus the reference fix. Use `--eval-keep` to inspect. |
| Every trial is `passed` | Check also passes at `base`; it doesn't measure the task. |
| Many `limit` outcomes | Per-task limits too tight, or the task is too large. |
| `NeedsInput` outcomes | The task is ambiguous; the agent asked a question. Make the task self-contained. |
| Trailing trials `skipped` | Suite `--cost-limit` / `--time-limit` reached. Raise it or shrink the matrix. |
| `error` with HTTP 429 | Provider rate limits; lower `--eval-jobs` or configure `fallback_models`. |
| Worktree creation fails | `base` not present locally (shallow clone) or `repo` isn't a Git repository. |
| Leftover worktrees | Expected only with `--eval-keep`; otherwise a bug (cleanup must also happen on Ctrl-C). `git worktree list` / `git worktree prune`. |

## 12. Implementation guide for contributors

### Where code goes

To keep changes focused and limit merge conflicts in busy files:

- **`internal/eval/`** (new package): suite parsing/validation, matrix expansion, trial scheduling, grading, pass@k, results and report writers. Most of the code and tests live here.
- **`cmd/tinyagent/main.go`**: only a thin dispatch: parse the new flags, call into `internal/eval`. Alternate top-level modes already exist in `cmd/tinyagent/review.go` and `cmd/tinyagent/check.go` as examples.
- **`cmd/tinyagent/suggest.go`** and **`cmd/tinyagent/completion.go`**: add the new flags.

Existing code to reuse (all present on `main`):

| Path | Reuse for |
|---|---|
| `cmd/tinyagent/worktree.go` | Existing `--worktree` creation, per trial. |
| `internal/agent/loop.go` | Run loop; must be callable N times in one process without shared mutable state, or trials run as child processes (open question 1). |
| `internal/trajectory/trajectory.go` | Exit status and trajectory save, per-trial output. |
| `internal/outschema/outschema.go` | Expected-JSON assertions / `output_schema`. |
| `internal/hooks/` | Stop hooks still run inside trials; checks are separate and run after the run ends. |
| `internal/config/` (`config.go`, `profiles.go`) | Strict-key YAML loading and profiles; mirror for the suite schema and profile digest. |
| `internal/model/` | Price table for cost estimates and totals; fallback handling (`fallback.go`). |
| `internal/eventlog/` | `--log-db` should record the trial id. |

Constraints (CONTRIBUTING.md): the only dependency is `gopkg.in/yaml.v3` — keep it that way; PRs small and focused; sandboxing isn't supported on Windows.

### Suggested PR split

1. Suite schema + sequential runner + `results.jsonl`.
2. Report + pass@k + thresholds.
3. Parallel jobs (`--eval-jobs`) + budgets.
4. GitHub Action input.

Plus docs per the draft's rollout: README section "Evaluating tasks", flags table entry, completions, CHANGELOG, optional `--init-eval`. Ship as experimental with `schema_version: 1`.

### Test expectations

- **Unit tests** for suite parsing and validation, matrix expansion, grading, pass@k and the report schema.
- **Integration tests** for pass, failing check, limit, NeedsInput, interrupt cleanup, `--eval-jobs` isolation and budget stop. `cmd/tinyagent/main_test.go` already has a fake OpenAI-compatible server built on `net/http/httptest` (`fakeOpenAI`, "serves one canned reply that submits, counting requests"); extending that pattern is the natural starting point, though it currently only covers a single canned submit reply and will need scripted variants (failing edits, step-limit loops, `ask_user`).
- Verify on Linux and macOS, per CONTRIBUTING (`//go:build linux` caveat).

### Acceptance criteria

- Suite with an unknown key → exits 2 naming the key, before any model request.
- 2 models × 1 profile × 3 trials × 2 tasks → exactly 12 trials, each with its own worktree at the task's `base` and its own trajectory; `results.jsonl` has 12 records.
- Run submits but one check exits 1 → `failed_check`, with the check's exit code and capped output; not counted as passed.
- Trial hits `--step-limit` → outcome `limit`; checks still run and are recorded (`checks_on_limit`, default true).
- `--eval-jobs 2` → at most 2 concurrent trials; worktrees and trajectories never collide.
- Suite `--cost-limit` reached → no new trials start, in-flight trials finish, report marks skipped trials.
- `--eval-min-pass-rate 0.8` with a configuration below it → non-zero exit; report names the configuration.
- `--dry-run` → no worktree created, no provider request.
- User's working tree and current branch unchanged after any eval run — pass, fail, or Ctrl-C (worktrees cleaned up on interrupt).
- `redact_secrets`, `command_env`, `file_access`, `--sandbox` / `--env container` apply inside trials exactly as for single runs.

### Related work

Builds on #113 (worktrees), #72 (output schema), #81 (stop hooks), #126 (profiles), #117 (fallback models), #60 (JSONL events), #70 (event log) and #145 (GitHub Action). Complementary to #153 / PR #154 (OpenTelemetry): spans could carry the trial id.

## 13. Open questions

1. **In-process or child processes?** **Recommendation:** child `tinyagent` processes — strongest isolation, every flag reused unchanged, no shared mutable state in `internal/agent`.
2. **Where do checks run** — the configured execution environment (sandbox/container) or always the host? **Recommendation:** the same environment as the trial, so checks see what the agent saw.
3. **Non-Git `repo`** (plain directory copy) in v1? **Recommendation:** no; Git only in v1.
4. **pass@k estimator**: unbiased (HumanEval paper) or simple any-of-k? **Recommendation:** unbiased estimator, with `k ≤ trials`.
5. *(Raised by this guide, not the draft.)* Default for `--eval-out`, exact field names in `results.jsonl`, the expected-JSON key name, and how suite-level `--cost-limit`/`--time-limit` relate to the same flags' per-run meaning — to be settled in PR 1.
