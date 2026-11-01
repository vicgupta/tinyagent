# AGENTS.md, skills and workflows

### Project instructions (AGENTS.md)

Unless `--no-agents-md` is set, tinyagent looks for `AGENTS.md` starting at `--cwd` and walking up through its parents, stopping at the repository root (the nearest directory containing `.git`). The nearest file wins, and only that one is added to the system prompt. Without a `.git` above `--cwd`, only `<cwd>/AGENTS.md` is checked. The lookup never goes above the repository root, and `-v` prints which file was loaded. This only changes the prompt: tools are still confined to `--cwd`.

#### Scoped AGENTS.md in subdirectories

Monorepos often keep instructions next to the code they cover, such as `services/api/AGENTS.md`. With `--scoped-agents-md` (or `scoped_agents_md: true`), tinyagent also delivers those files, lazily: the first time a successful built-in file tool call touches a path below `--cwd`, every `AGENTS.md` in a directory between `--cwd` (exclusive) and that path is appended to the call's result, outermost first. The startup file above stays in the system prompt and is never sent again. It is off by default; with it off, prompts, tool results, and checkpoints are unchanged.

```
[AGENTS.md for services/api/ (812 bytes): instructions for files under services/api/; they take precedence over broader project instructions there]
...file contents...
[end AGENTS.md for services/api/]
```

- **Triggers:** `read_file`, `write_file`, `edit_file`, `view_image`, `lsp`, every path of an `apply_patch` (including move targets), and the directory argument of `list_files` and `grep` (so `grep` from `--cwd` itself triggers nothing). `bash`, background processes, MCP tools, and failed or denied calls never trigger delivery.
- **Once per context:** a file is sent once while its block is still in the model's context. If [compaction](long-runs.md#long-runs) elides or summarizes that result, the file is sent again on the next access; it is not pinned. Subagents start with nothing delivered, and `--resume` doesn't resend files the checkpoint already has in context.
- **Limits:** each file is capped at 32 KiB (cut on a UTF-8 boundary, with `[AGENTS.md truncated]`), and a run delivers at most 64 KiB of distinct scoped files; past that, the result gets a one-line notice instead and stderr a warning. Blocks are added after `max_output_chars` truncation and hook feedback, so they are never cut off, and `post_tool` hooks see them.
- **Safety:** files denied by [`file_access`](permissions.md#restricting-file-access) and symlinks resolving outside `--cwd` are skipped (`-v` prints a line). A file modified after the run started (by the model or anything else) is not delivered, so the model can't inject instructions into its own context. Empty, unreadable, or non-UTF-8 files are skipped with a warning; the run continues.
- **Records:** `-v` prints each loaded file, `--json` emits `instructions.loaded` and `instructions.skipped`, and `-o` trajectories record `scoped_agents_md_enabled`, `scoped_agents_md` (files in context) and `scoped_agents_md_loads`. `--resume` keeps the checkpoint's setting; a conflicting `--scoped-agents-md` exits 2. `--no-agents-md` turns scoped delivery off too, and combining the two flags exits 2.

**Trust:** like the startup file, a scoped `AGENTS.md` is prompt input that can steer the agent, so only enable this for repositories you trust. Configs with `scoped_agents_md` fail to load on older tinyagent versions, which reject unknown keys.

### Agent Skills

tinyagent discovers [Agent Skills](https://agentskills.io): folders containing a `SKILL.md` with YAML frontmatter (`name` and `description`) and Markdown instructions, optionally with `scripts/`, `references/`, or `assets/`.

**When skills are on.** Discovery is on by default only in an interactive run (stdin and stdout are both terminals: `tinyagent` chat, or a one-shot `-t` run typed at a terminal). It is off by default in hardened modes, where untrusted repository content is likely:

| Mode | Default |
| --- | --- |
| Interactive terminal (chat or `-t`) | on |
| stdin or stdout not a terminal (piped, scripted, ACP) | off |
| `CI` or `GITHUB_ACTIONS` set (including the GitHub Action) | off |
| `--eval` runs and `--attempts` runs | off |
| `--lockdown` (or `untrusted_content.enabled`) | off |
| `--read-only` | off |

Precedence is flag > config > mode default: `--skills` turns discovery on in any mode and `--no-skills` turns it off; otherwise `skills: true` or `skills: false` in the config wins; otherwise the mode default above applies. `skills` is tri-state, so leaving it out means "use the mode default". In a hardened mode, `skills: true` from a repository `./tinyagent.yaml` is ignored unless you pass `--trust-config` (it must come from `-c`, a trusted config or `--skills`); `skills: false` is always honored. `tinyagent --check` prints the decision and where it came from (for example `skills:    off (hardened default: CI is set)`), `--check --json` has it as `skills` (`enabled`, `source`, `detail`), and `-v` prints it at the start of a run.

- **Locations:** only `./skills/<name>/SKILL.md` under `--cwd`. Parent directories and your home directory are not scanned. Symlinked skill folders are followed. `.agents/skills` and `.claude/skills` are not scanned; when one exists in `--cwd`, a warning suggests moving or symlinking its skills into `./skills`.
- **Locations (plugins):** each configured [plugin](plugins.md#plugins) contributes `<plugin>/skills/<name>/SKILL.md`.
- **Precedence:** on a name clash project skills beat plugin skills (earlier plugins beat later ones); each shadowed skill produces a warning naming both paths.
- **Validation:** a skill with no description or unparseable frontmatter is skipped with a warning. Name problems (wrong format, mismatch with its folder) only warn. A `SKILL.md` over 1 MiB is skipped.
- **Progressive disclosure:** only the names and descriptions go into the system prompt (a "Skills" section, capped at 8,000 characters: descriptions are shortened first, then skills are omitted with a warning). The `skill` tool loads one skill's instructions (frontmatter stripped) wrapped in `<skill_content name="...">`, with its directory and up to 100 bundled file paths. `{"name": ..., "file": "references/x.md"}` reads one bundled text file (1 MiB cap; no absolute paths, `..`, or symlinks that leave the skill folder; binaries refused). Results also honor `max_output_chars`. Loading a skill that is still in context returns a short note instead of a second copy.
- **Other tools are unchanged:** `read_file` and friends stay confined to `--cwd`, so plugin skills outside it are reachable only through the `skill` tool. Run bundled scripts with `bash` as usual.
- **Read-only and hooks:** the `skill` tool only reads, so it is available with `--read-only`. `pre_tool`/`post_tool` hooks match it as `skill`.
- **Compaction:** loaded skill instructions are never elided by stage-1 compaction. When old turns are summarized, the latest instructions of each loaded skill are re-attached after the summary (up to 32 KiB combined; any skill left out is named so the model can reload it).
- **Resume:** the checkpoint records whether skills were enabled and which were found. `--resume` keeps that setting (passing a conflicting `--skills`/`--no-skills` is an error) and refuses to continue if the skills catalog changed. Checkpoints from before this feature resume with skills off.
- **Playbooks:** a [playbook](playbooks.md) can name skills in `skills:`; they load with the playbook from `./skills` without `--skills`.
- With no skills found, the prompt and tool list are exactly as without `--skills`. `-v` prints each loaded skill and its path; warnings go to stderr and, with `--json`, as `warning` events.

**Trust:** skill instructions are prompt input; that is why discovery is off by default in hardened modes. A project skill from a repository you don't trust can steer the agent like a malicious AGENTS.md, and bundled scripts run with your permissions if the agent invokes them. Skill text sent to the model goes to your model provider like any other prompt, and the trajectory records loaded skill content.

### Skill workflows

For a task-oriented guide (quick start, paths, routing, flags, events, troubleshooting and examples), see [Workflows](workflows.md). This section is the detailed reference.

For a lighter option, a [playbook](playbooks.md) puts a checklist for one kind of task in the system prompt of a single run, and can hand off to a workflow with `workflow:`.

`--workflow file.yaml` runs a sequence of small skills on the task (`-t`) as one automatic pipeline, for example plan, then generate, then validate, then test. Each stage is a fresh, unattended tinyagent run in `--cwd` that gets the original task and the previous stage's output. As soon as a stage passes its success check the next one starts; there is no pause or approval between stages. Without `--workflow`, tinyagent behaves exactly as before, and `--skills` Agent Skills are unaffected.

A workflow has two layers: **one YAML workflow file** (the pipeline: stages, checks and limits), passed with `--workflow`, and **one Markdown file per skill** (each stage's instructions), kept in the workflow's `skills_dir`. The two formats don't compete; the file you pass to `--workflow` is always YAML.

**Workflow file (YAML).** The workflow file is always YAML. It names the skills directory and lists each workflow's stages in order, with optional checks and limits:

```yaml
version: 1
skills_dir: skills        # relative to this file
default: implement        # used when routing finds no match (optional)
defaults:                 # apply to every stage
  retries: 1
  step_limit: 50
workflows:
  implement:
    intent: Implement a feature from a spec, then validate and test it.
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

Settings in `defaults` or on a stage: `model`, `profile`, `step_limit`, `cost_limit`, `token_limit`, `time_limit`, `retries`, and `check_timeout` (seconds). A stage can also have `check` (a command) or `behavior` (a behavior file; see [Verifying behavior](#verifying-behavior)), but not both, and `gate: spec` (see [Spec gate](#spec-gate)). The defaults are 1 retry (at most 5), 50 steps, a 300 second check timeout, and otherwise your usual config. Flags you pass with `--workflow` (such as `--sandbox` or `-m`) are forwarded to every stage, and stage settings override them. [Verify before stop](verify.md#default-on-for-workflow-runs) is on by default for every stage; pass `--verify-on-stop=false` to turn it off. Workflows are sequential and have at most 20 stages. A complete example is in [docs/workflows](workflows).

**Skill files (Markdown).** Each stage's skill is a Markdown file with three required parts: a heading, an intent line, and a list of inputs. A one-line success check is required too, unless the workflow gives that stage a `check` command.

```markdown
# Skill: plan-tasks
> Intent: Break a spec into small, ordered tasks before writing any code.

**Inputs**
- spec
- repo_path

**Success check:** every task has acceptance criteria and a verification step, in dependency order.
```

Skills live in the workflow's `skills_dir` (default `skills/` next to the workflow file) as `<name>.md` or `<name>/SKILL.md`. Names are lowercase letters, digits, `.`, `_` and `-`. A `SKILL.md` with `name`/`description` frontmatter can use the description as its intent. The whole file (frontmatter removed) is the stage's instructions. These input names are filled automatically: `task` and `spec` (the `-t` task), `repo_path` and `cwd` (the absolute `--cwd`), `previous_output` or the previous skill's name (the previous stage's output). Any other input comes from the stage's `inputs:` map or is marked as not provided.

**Validating skills.** `tinyagent skills validate <path>...` (same as `--skills-validate <path>`, repeatable) checks skill-workflow files without running anything or calling a model, so you can catch a bad skill in CI or a pre-commit hook. Each path is a skill file (`<name>.md` or `<name>/SKILL.md`), a skills directory, or a workflow file (`.yaml`/`.yml`). Skills are checked with the same parser and rules `--workflow` uses at load time, and a workflow file is fully loaded, so a missing skill or a stage with no success check and no `check` is an error. It prints `ok <name>` per valid skill on stdout; a standalone skill with no success check gets a warning on stderr (`no success check: needs a check: command when used as a stage`) but still passes. Every path is checked, and each invalid one is reported on stderr, naming the skill. Exit status is 0 when all are valid, 1 when a skill or workflow is invalid, and 2 when a path doesn't exist or isn't one of those kinds (2 wins if both happen). This validates workflow skills only; [Agent Skills](#agent-skills) (`--skills`) are a different format and aren't checked.

**Routing.** `--workflow-name name` runs that workflow, or a single skill, directly. Otherwise the router matches the task against each workflow's intent and each skill's intent line. A clear best match runs. A tie or no match falls back to `default`, or exits 2 with an error suggesting `--workflow-name`, before any model call.

**Success checks.** A stage with `check` (an argv list, run in `--cwd` without a shell) passes when the command exits 0. Otherwise the model is told the skill's success check and must end its answer with `SUCCESS CHECK: PASS` or `SUCCESS CHECK: FAIL - <reason>`. The last such line decides, and a missing line fails. A stage run that ends without submitting (for example on a step, cost or time limit) also fails. A failed stage is retried with the failure reason in its prompt. When its retries run out, the workflow stops without running later stages or more attempts, prints the stage and reason, and ends **Blocked** or **Failed** (see [Blocked tasks](#blocked-tasks)). Earlier stages are not re-run.

#### Blocked tasks

When a stage runs out of retries, the workflow tells apart a task that can't be met as specified from a run that broke:

- **Blocked** (exit 5): the last attempt's check failed on its merits: a `check` command ran and exited non-zero, the model reported `SUCCESS CHECK: FAIL`, or behavior cases ran and failed. Retrying again won't help without a human changing the task or its acceptance criteria.
- **Failed** (exit 1): a transient or infrastructure problem: the stage run errored or hit a step, cost, token or time limit, the output had no `SUCCESS CHECK` line, a `check` command timed out or couldn't start, or the behavior file was invalid. A check timeout counts as transient because a slow machine or a hung process can cause it without the task being wrong.

Either way nothing more runs: no further attempts and no later stages, so a blocked task spends no more tokens. The status, stage and reason are in the stderr summary line (`workflow <name> Blocked at stage <stage>: <reason>`, followed by a re-run hint), the `-o` record's `status`, `failed_stage` and `reason`, and the `workflow.finished` event (`status: "Blocked"`, `blocked: true`).

**Re-queueing.** tinyagent keeps no list of blocked tasks; one run handles one task. To re-queue, fix the task or its acceptance criteria and run the same `tinyagent --workflow ... -t ...` command again. An outside loop that feeds tasks to tinyagent can park a task on exit 5; the suggested convention for an issue-label loop is to swap the issue's `amf:ready` label for `amf:blocked`, and have a person re-add `amf:ready` once the issue is fixed. [`tinyagent triage`](triage.md) flags issues with that label as `blocked` and ranks them last. tinyagent itself makes no GitHub calls.

#### PR bodies

A workflow (or an outside loop around it) that ends in a pull request should keep the PR body short and put the detail elsewhere. Write the full record to `--output` (the workflow `Result` JSON) and link or attach that run log; the PR body gets only the three sections of [`.github/pull_request_template.md`](../.github/pull_request_template.md): what changed, how it was verified, and risks or follow-ups, in under about 200 words, with no transcripts, run logs or full diffs. `gh pr create --body ...` ignores the template, so a tool that passes `--body` should follow the same three sections. tinyagent itself opens no PRs.

#### Learning from failures

`--workflow-observe` (off by default) adds an observer step after a `--workflow` run that ends `Failed` or `Blocked`. It turns the failure into one concrete artifact, a proposed [project rule](ratchet.md#observer-proposals), instead of leaving the lesson to be written by hand:

```sh
tinyagent --workflow docs/workflows/feature.yaml -t "add CSV export" --workflow-observe
# observer: proposed project rule p-3f2a9c1b7d4e for stage generate-code in .tinyagent/observer/proposals.jsonl; apply it with:
#   tinyagent ratchet add 'Before finishing the generate-code stage, run `go test ./...` yourself ...' --incident '...' --cwd '...'
```

- **Signature.** Each failure is identified by a SHA-256 of the failed stage, its check argv and the first failing line of the check output (the reason when there is none), with paths, hashes, numbers and durations normalized away, so the same failure in another temp dir or with other timings gets the same signature.
- **Proposal.** The first time a signature is seen, one JSON line goes to `.tinyagent/observer/proposals.jsonl` in `--cwd`: `id`, `signature`, `created`, `workflow`, `status`, `stage`, `reason`, `check_method`, `check_argv`, `check_output_excerpt` (secret values from the environment and token-shaped strings masked, capped at 2,000 bytes), `rule`, `incident` and `suggested_command`. The command is printed on stderr too. The observer never applies the rule: a person reviews it and runs the command.
- **Recurrence.** `.tinyagent/observer/ledger.jsonl` keeps `signature`, `stage`, `first_seen`, `last_seen`, `count` and `artifact_id` (one appended line per observation; the latest line per signature wins). When a signature is seen again, the observer writes no new proposal, bumps the count and warns `gate did not hold`, naming the project rule applied from the proposal if there is one (found by the `observer <signature prefix>` tag in its incident).
- **Safety.** Passed and interrupted runs write nothing, and without the flag nothing is written. A corrupt ledger line is skipped with a warning, and a write error is a warning; neither changes the exit code. Each file write is one `O_APPEND` line, so concurrent runs in the same `--cwd` don't interleave.
- **Events.** With `--json`, `observer.proposed` or `observer.recurrence` follows `workflow.finished`; see [JSON output](json-output.md#machine-readable-output).

The observer makes no model call. Model-written skill edits, generated verify checks and eval-suite generation are not part of it.

#### Spec gate

A thin plan wastes a whole generate-code stage before anyone notices it can't be built or checked. `gate: spec` on a stage (normally `plan-tasks`) checks that stage's output after it passes its success check and before the next stage starts. The plan passes the gate when it has at least one task and every task has:

- a `Task N: <title>` line that starts the task (it may be a heading or bold);
- an `Acceptance criteria:` list with at least one bullet;
- a ``Verify: `<command>` `` line with one command in backticks whose program exists: its first word after any `VAR=value` prefixes is found on `PATH`, or, if it contains a `/`, exists relative to `--cwd`. The command is not run, because the code it checks doesn't exist yet.

The example [`plan-tasks`](workflows/skills/plan-tasks.md) skill asks for exactly this shape. The gate checks structure only; it doesn't judge whether the criteria are good.

When the gate rejects a plan, the stage runs once more (a **re-plan**) with the gate's reason in its prompt. The re-plan gets the stage's normal retry budget again for its success check, so it doesn't use up the retries. If the gate rejects the re-plan too, the workflow stops before the next stage, with status `Failed`, the failed stage, and a reason starting `spec gate:`, and exits 1. It never re-plans in a loop. Each decision is a `stage.gate` event and an entry in the `-o` record's `gates` list, and is shown as a progress line. Without `gate:`, stages behave as before. `gate` is a new workflow key, so older tinyagent versions reject a workflow file that uses it.

#### Contract docs

A workflow keeps three things apart: the **spec** is what you are building this time (the `-t` task), a **skill** is how a stage builds it (a file in `skills_dir`), and a **contract doc** is the stable reference for a part of the target repository that keeps getting built on, such as a component library or an internal API. Without a contract doc, every plan re-researches that surface from the code.

Contract docs live in the target repository, by default in `docs/contracts/<surface>.md`. Each starts with a fixed header and three sections:

```markdown
# Contract: tools-executor
Covers: internal/tools
Last verified: 3f2a9c1

## Public API
## Invariants
## How to extend
```

- **Writing one (on request).** The example workflow file has a `contract` workflow that runs the [`write-contract`](workflows/skills/write-contract.md) skill: `tinyagent --workflow docs/workflows/workflow.yaml --workflow-name contract -t "write a contract doc for internal/tools"`. It reads the code, writes or updates `<contracts_dir>/<surface>.md`, and changes no code. Its `check` passes when a file in `docs/contracts` starts with `# Contract: `; if you set another `contracts_dir`, change the check path to match.
- **Using them.** The example [`plan-tasks`](workflows/skills/plan-tasks.md) skill takes a `contracts_dir` input (the `implement` workflow sets `contracts_dir: docs/contracts`). It reads every contract doc whose `Covers:` line overlaps the spec before researching, lists the ones it used as `Contract used: <path>`, and lists each stable surface it had to research from scratch as `Contract candidate: <surface>`, so you know which contract to write next. With no contracts directory, it plans as before.
- **Staleness.** `Last verified:` holds the commit or date the doc was checked against. When the covered code changed after that, plan-tasks re-checks what it relies on and reports the contract as stale instead of trusting it.
- **Trust.** A contract doc is ordinary repository content, read as reference material. It is never loaded automatically, has no authority over the spec or AGENTS.md, and must not be named `AGENTS.md`.

tinyagent doesn't detect repeated use across runs or refresh contracts by itself; run the `contract` workflow when plan-tasks reports a candidate or a stale contract.

#### Verifying behavior

Tests passing doesn't prove the built program does what the task asked. A stage with `behavior: <path>` checks that from the outside: the stage's run translates the task's behavioral acceptance criteria (what a command prints, which exit code it returns) into cases in that file, and the harness then runs each case itself and decides the stage on the observed output alone, not on the model's opinion. The example `implement` workflow ends with a `verify-behavior` stage that does this after `run-tests`.

The path is relative to `--cwd` and must stay inside it; `.tinyagent/` is a good place, since it is usually ignored by Git. The harness deletes any old file at that path before each attempt, so a file left by an earlier run never decides a new one. The file is YAML (JSON also works):

```yaml
version: 1
cases:
  - name: prints-greeting
    criterion: the CLI prints hello for --name world   # the acceptance criterion it checks
    run: [go, run, ./cmd/app, --name, world]           # argv, run in --cwd without a shell
    stdin: ""                                         # optional
    timeout: 60                                       # seconds, optional
    expect:
      exit_code: 0                 # default 0
      stdout_contains: ["hello world"]
      stdout_not_contains: [panic]
      stdout_matches: ["^hello"]   # Go regular expressions
      stderr_contains: []
```

Each case needs a non-empty `run` and at least one expectation. A case passes when its exit code matches and every string and pattern holds; the stage passes when every case passes. At most 20 cases; a case's timeout defaults to 60 seconds and is capped by the stage's `check_timeout`. An invalid file fails the stage with the reason, and a failing case's unmet expectations are handed to the retry like any failed check.

When the task has no behavioral criteria, the stage writes no file (or `cases: []`) and is **skipped**: it counts as passed, the progress line says `skipped`, and the check records `skipped: true` with the reason. The model decides whether criteria are behavioral, so a skipped stage is worth a look when you expected cases.

**Evidence.** Each case's observed output (argv, exit code, stdout and stderr, each capped at 4 KiB, and its failures) is recorded in the check's `evidence` list in the `-o` workflow record and the `stage.finished` event, and is appended to that attempt's log under `== verify-behavior evidence ==`.

**Output and exit status.** Progress lines go to stderr, and the last stage's output goes to stdout. `-o run.json` writes a workflow record (`"kind": "workflow"`) with the route, status, failed stage and reason, total cost, the spec gate decisions (`gates`), and one entry per attempt with its input, output, exit status, check result, paths to that attempt's trajectory and log under `run.json.stages/`, and `unverified: true` when the stage ended with [unverified changes](verify.md#default-on-for-workflow-runs). Without `-o`, the per-stage files are temporary. `--json` prints JSONL events `workflow.started`, `stage.started` (with the input), `stage.finished` (input, output, `unverified` when the stage ended unverified, and `check`, which for a [behavior stage](#verifying-behavior) has `method: "behavior"`, `skipped` and `evidence`), `stage.gate` (a [spec gate](#spec-gate) decision), and `workflow.finished`. Exit status is 0 when every stage passes, 5 when a stage is [blocked](#blocked-tasks), 1 when a stage fails for another reason, 2 for an invalid skill or workflow file, a routing error or a conflicting flag (all before any model request), and 130 when interrupted. `--workflow` can't be combined with flags for one interactive or resumed run, such as `--resume`, `--review`, `--worktree`, `--eval` or `--ask-user`.

**Trust:** workflow skills are prompt input, and `check` commands run with your permissions, just like a project's AGENTS.md or Agent Skills. Only run workflow files you trust.
