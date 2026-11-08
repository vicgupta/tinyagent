# Self-tuning

Self-tuning turns repeated workflow failures into one small, reviewed improvement to a project-owned [workflow skill](workflows.md) or its success check. It is **off by default**, never runs in the background and never changes a file without your explicit approval of the exact change.

It builds on [Learning from failures (`--workflow-observe`)](workflows.md#learning-from-failures---workflow-observe): failures are grouped by the same observer failure signature, and a candidate links the observer's ledger entry and any [project rule](ratchet.md) applied for it. There is no second learning store.

- [Turning it on](#turning-it-on)
- [The loop](#the-loop)
- [What a candidate can change](#what-a-candidate-can-change)
- [Validation](#validation)
- [Approval, apply and revert](#approval-apply-and-revert)
- [Model-phrased proposals](#model-phrased-proposals)
- [Safety](#safety)
- [Files and events](#files-and-events)

## Turning it on

Set `self_tuning.enabled: true` in the project config. Without it every `tinyagent tune` command exits 2 before it reads a run, calls a model or writes a file.

```yaml
self_tuning:
  enabled: true
  inference: false          # true: phrase proposals with the auxiliary model (task tuning)
  min_evidence: 2           # distinct failed runs with the same failure signature; 2..20
  max_tokens: 2000          # one inference call, input + output; 1..20000
  max_cost_usd: 0.25        # all inference calls of one proposal; at most 5
  max_attempts: 1           # inference calls per proposal; 1..3
  validation_runs: 6        # workflow runs per validation (baseline + candidate); 2..20
  validation_cost_usd: 2    # stop a validation once its runs cost this much; at most 50
```

The bounds have hard caps, so a repository's config can lower them but not lift them past the caps. See [Configuration](config.md#self-tuning).

## The loop

Every step is one explicit command, with `--cwd` as the project. Paths are relative to `--cwd` and must stay inside it.

```sh
# 1. Keep the record of each workflow run.
tinyagent --workflow wf.yaml -t "add CSV export" -o runs/csv.json --workflow-observe
tinyagent --workflow wf.yaml -t "add TSV export" -o runs/tsv.json --workflow-observe

# 2. Propose one change from the runs you pick (passed runs become regression controls).
tinyagent tune propose --workflow wf.yaml --run runs/csv.json --run runs/tsv.json --run runs/ok.json
# proposed t-3f2a9c1b7d4e: check change to skills/impl.md for stage impl from 2 failed runs

# 3. Review it: evidence, rationale, expected effect and the diff.
tinyagent tune show t-3f2a9c1b7d4e

# 4. Validate it in isolated copies of the project.
tinyagent tune validate t-3f2a9c1b7d4e
# t-3f2a9c1b7d4e accepted: 2 failed task(s) passed with the change, no regressions
#   tinyagent tune apply t-3f2a9c1b7d4e --digest 9b1c0e4d2a7f

# 5. Apply exactly that change, or later undo it.
tinyagent tune apply t-3f2a9c1b7d4e --digest 9b1c0e4d2a7f
tinyagent tune revert t-3f2a9c1b7d4e

tinyagent tune list
```

The flag forms are `--tune-propose` (with `--tune-run`, `--tune-workflow`, `--tune-infer`), `--tune-show`, `--tune-validate`, `--tune-apply` (with `--tune-digest`), `--tune-revert` and `--tune-list`; see the [CLI reference](subcommands.md). One tune command runs at a time, and run flags such as `-t` are refused.

**Evidence.** `propose` reads the `-o` records of `--workflow` runs (at most 20, each at most 8 MB). It groups the `Failed` and `Blocked` runs by observer failure signature and takes the largest group. That group needs at least `min_evidence` (default and minimum 2) **distinct** runs: a copy of the same file counts once, and one failure alone is never enough. A run must have used a workflow in the `--tune-workflow` file, and its failed stage must be a stage there.

**Candidate.** The proposal records the source runs (path, SHA-256, status, redacted and bounded reason, check method and argv, output excerpt up to 1,000 bytes, task), the observer reference, a rationale, the expected effect, the unified diff and a 12-character **digest** that names exactly this change (the target and the content before and after). Proposing the same failure again returns the open candidate instead of a new one.

## What a candidate can change

Only the failed stage's skill file, as the current workflow file defines it, and only in one place:

- **Skill lesson** (`kind: skill`), for a stage with a `check:` command or `behavior:` file: a bullet under `## Lessons from past runs` at the end of the skill, for example "Before you finish this stage, run `go test ./...` yourself and fix everything it reports until it passes".
- **Stricter success check** (`kind: check`), for a stage judged by the model's `SUCCESS CHECK:` verdict: a clause appended to the skill's `Success check:` line that names the earlier failure. A skill without that line gets a lesson instead.

Each change ends with a `(tune <signature>)` tag. Applied changes only affect **future fresh runs** of the stage: runs in progress and resumed runs keep their prompts, so the [append-only history](long-runs.md#append-only-history-and-prompt-caching) invariant holds.

## Validation

`validate` reruns the candidate's failed tasks first, then its control tasks, each twice: once without the change (baseline) and once with it (candidate). Every run uses a fresh copy of `--cwd` in the OS temp directory, removed afterwards. The copy has regular files only: no `.git`, no symlinks and none of `.tinyagent/` except `ratchet.toml`. The project itself is never touched. Projects over 50,000 files or 512 MB are refused.

Stages run through the normal workflow machinery (`harness`): same stage checks, behavior cases, retries and stage limits. The before/after outcome of every run is stored with its cost.

| Verdict | When | Status |
|---|---|---|
| `accepted` | No regression, and at least one failed task passed with the change on a fresh rerun | `validated` |
| `regression` | A task that passed without the change failed with it | `rejected` |
| `no_improvement` | No failed task was fixed, including when the failure did not reproduce at baseline | `rejected` |
| `incomplete` | `validation_runs` or `validation_cost_usd` ran out, or the run was interrupted | `rejected` |

The original failure alone never counts as an improvement: only fresh reruns do. Validating needs the target skill unchanged since the proposal. Exit status is 0 for `accepted` and 1 otherwise.

## Approval, apply and revert

`apply` needs all of these, or it changes nothing:

- the candidate is `validated` with an `accepted` verdict;
- `--tune-digest` (`--digest`) equals the digest `tune show` printed, so you approve the exact change you reviewed;
- the skill file is still byte-for-byte what the candidate was proposed against;
- the stored candidate is intact: its content matches its hashes, digest and id (an edited candidate file is refused).

It writes the reviewed content atomically and records the step in the candidate's history. `revert` restores the content from before the change, but only while the file still holds exactly the applied change. If you edited it since, revert refuses and you undo the change by hand.

**Recurrence.** If the same failure signature shows up again after a candidate for it was applied, `propose` warns `gate did not hold`, emits `tune.recurrence` and writes no new candidate. Revert the change or edit the skill yourself.

## Model-phrased proposals

With `self_tuning.inference: true` or `--tune-infer`, `propose` asks the [auxiliary model](models.md#model-roles) for task `tuning` (`--route tuning=`, `models.auxiliary.tasks.tuning`, `--aux-model`, `models.auxiliary.model`, else the main model) to phrase the instruction text. The model only writes that text. The target file, the kind and the place in the file are still chosen by tinyagent. The calls stay within these bounds:

- `max_tokens`: a prompt estimated over it makes no call; a reply over it is not used. Where the provider supports it, it is also the output token limit.
- `max_cost_usd`: the reply is not used once the cost goes over it, and no more attempts are made.
- `max_attempts`: unsafe or malformed replies are retried up to this many calls.

A reply must be one line of at most 400 bytes with no code fence and nothing that looks like instructions about files, config, permissions, sandboxing, hooks or credentials. Otherwise, or on an error or a spent budget, the deterministic text is kept with a warning. The candidate records model, attempts, tokens, cost and status (`used`, `rejected`, `over_budget` or `error`). Without inference there is no model call and no cost.

## Safety

- **Runs are untrusted data.** A run only names a failure. The file a candidate may change comes from the project's workflow file, never from the run. A failed stage that isn't in the workflow is refused. Text taken from runs is redacted (secret values from the environment and token-shaped strings), flattened to one line, capped and stripped of code markup. Text that looks like instructions (prompt-injection phrases, instruction or config files, permissions, sandboxing, hooks, credentials, destructive commands) is never written into a skill. The model prompt fences run data as untrusted.
- **Everything stays inside `--cwd`.** The workflow file, its `skills_dir`, run files, targets and the candidates directory are resolved with symlink checks (`internal/cwdpath`). A path with `..`, an absolute path elsewhere, or a symlink leading outside is refused, and so is a target that is itself a symlink. There is no home-folder, global or cross-project state.
- **Protected files.** `AGENTS.md`, `CLAUDE.md`, `tinyagent.yaml`, `memory.toml`, anything under `.tinyagent/` or `.git/`, and non-Markdown files are never targets. Self-tuning never edits project instructions (tinyagent reads [`AGENTS.md` only](project-context.md#project-instructions-agentsmd)) and never touches permissions, lockdown, hooks or sandbox settings.

## Files and events

- `.tinyagent/observer/candidates/<id>.json` in `--cwd`: one file per candidate with its status (`proposed`, `validated`, `rejected`, `applied` or `reverted`), evidence, diff, before and after content, validation and history.
- With `--json`, the commands print JSONL events: `tune.proposed`, `tune.recurrence`, `tune.validated`, `tune.applied` and `tune.reverted` (see [JSON output](json-output.md#machine-readable-output)). `tune list --json` prints a JSON array and `tune show --json` prints the full candidate.
- Exit status: 0 on success, 2 for usage errors or self-tuning off, 1 for a refused or failed proposal, validation, apply or revert.
