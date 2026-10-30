# Reviews, worktrees, resuming and follow-ups

### Reviewing changes

`--review`, `--review-base <ref>` and `--review-commit <rev>` review one Git change set without giving the model a shell. tinyagent runs fixed, read-only `git` commands itself, puts the diff in the first user message with built-in review instructions, and runs the model with the [read-only](subcommands.md#flags) tool set (plus any of `skill`, `web_fetch`, `lsp` and `delegate` you turned on) so it can read the surrounding code. The model reports findings ordered by severity, each with `file:line`, a severity and a short failure scenario. Nothing in the working tree or index changes.

```sh
tinyagent --review                                  # staged + unstaged + untracked vs HEAD
tinyagent --review-base main                        # merge-base(main, HEAD)..HEAD
tinyagent --review-commit HEAD~1                    # changes introduced by one commit
tinyagent --review -t "focus on error handling in pkg/api"
cat focus.md | tinyagent --review-base origin/main -t -
tinyagent --review-base origin/main --output-schema findings.schema.json > findings.json
```

- **Targets.** `--review` covers tracked changes against `HEAD` (staged and unstaged together) and untracked files that aren't ignored, shown as new files. `--review-base <ref>` covers the commits on `HEAD` since its merge base with `<ref>`; uncommitted changes are left out, with a note on stderr when there are some. `--review-commit <rev>` covers one commit against its first parent (a root commit against the empty tree), with its subject and author. Only one target per run.
- **Scope.** Only changes under `--cwd` are included, with paths relative to it, matching what the file tools can read. Binary files show as `binary file changed`. Paths hidden by `file_access.deny_read` are left out; the prompt, stderr and `review.hidden_paths` give only their count.
- **Size cap.** The diff text is capped at 200,000 bytes. Over the cap every changed file is still listed with its `+`/`-` counts, whole per-file diffs are included in order while they fit, the others are named so the model can read them with `read_file`, and stderr and `review.truncated` report it.
- **Safety.** Refs come only from the command line (a ref starting with `-` is rejected). Git runs with `--no-ext-diff`, `--no-textconv`, `core.fsmonitor=false`, every configured filter driver turned off and `GIT_OPTIONAL_LOCKS=0`, so reviewing an untrusted repository doesn't start programs its config names and doesn't write the index.
- **Privacy.** The diff, including untracked files that aren't ignored, is sent to the selected provider.
- **Errors and exit status.** `git` missing, `--cwd` outside a work tree, an unknown ref, no commits or no merge base exit 2 before any model request. A clean change set prints `nothing to review` and exits 0 without a request. Otherwise the exit status means the review ran, as for any run; it doesn't encode findings. Two review flags, or a review flag with `--read-only=false`, `--resume`, `--rewind`, `--worktree`, `--snapshots`, `--check`, `--init-config` or `--update`, exit 2.
- **Everything else as usual.** The system prompt and AGENTS.md, limits, `--json`, `-o`, `--log-db`, `--output-schema`, profiles and stop hooks behave as for other runs. `run.started`, trajectories and the event log record a `review` object (target, ref, resolved commits, `files_changed`, `diff_bytes`, `diff_bytes_sent`, `truncated`, `hidden_paths`), and `-v` prints one `review:` line. A review run saved with `-o` can be continued with `--resume review.json -t "<follow-up>"` (without a review flag) as an ordinary follow-up; it keeps the recorded read-only mode, so use it for questions about the findings, and start a new run to fix them.

A minimal findings schema for CI:

```json
{"type": "object", "required": ["findings"], "properties": {"findings": {"type": "array", "items": {"type": "object", "required": ["severity", "location", "summary"], "properties": {"severity": {"enum": ["critical", "high", "medium", "low"]}, "location": {"type": "string"}, "summary": {"type": "string"}, "scenario": {"type": "string"}}}}}}
```

### Worktrees

`--worktree` runs a task in a fresh Git worktree, so it never touches your checkout and several runs can work on the same repository at once:

```sh
tinyagent --worktree=fix-login -t "fix the login redirect" --cwd ~/src/project -o run.json
tinyagent --worktree -t "add tests for pkg/api" --cwd ~/src/project/pkg/api
```

- **What it creates.** A worktree at `<repo>/.tinyagent/worktrees/<name>` on a new branch `tinyagent/<name>`, from the current `HEAD` commit of the repository containing `--cwd`. The run's root becomes the matching path inside the worktree, so `--cwd ~/src/project/pkg/api` runs in `.tinyagent/worktrees/<name>/pkg/api`. A `.gitignore` containing `*` keeps `.tinyagent/worktrees` out of `git status`. The location isn't configurable.
- **Names.** `--worktree=<name>` uses 1 to 64 letters, digits, `.`, `_` and `-`, not starting with `.` or `-`. Bare `--worktree` generates `run-YYYYMMDD-HHMMSS-<6 hex>`. An existing worktree directory or `tinyagent/<name>` branch is an error rather than being reused.
- **Uncommitted changes aren't carried over.** The worktree starts from the last commit; if your checkout has uncommitted or untracked changes, tinyagent warns once and continues. Commit first if the task needs them.
- **Setup output and errors.** Setup prints `worktree: <path> (branch tinyagent/<name> from <commit>)`, and the end-of-run summary prints `worktree kept: <path> (branch ...)`. `--cwd` outside a Git work tree, a repository with no commits, `git` missing from `PATH`, an invalid or taken name, a `--cwd` subdirectory missing at `HEAD`, or combining with `--resume`/`--rewind` exits 2 before any model request and leaves nothing behind. Ctrl-C during setup leaves either no worktree or a complete one.
- **Hooks.** Repository hooks (such as `post-checkout`) don't run while the worktree is created. Filters and Git LFS behave as you configured them.
- **Keeping and cleaning up.** The worktree and branch are kept after every run, successful or not; tinyagent never commits, merges or pushes. Review it with `git -C <path> diff`, then remove it with `git worktree remove <path>` and `git branch -D tinyagent/<name>`.
- **Sandbox and container.** Git metadata lives in the main repository's `.git` directory, outside the worktree. With `--sandbox` it isn't writable, so `git commit` inside the worktree fails unless you add that directory to `sandbox_writable_paths` (which also lets commands change hooks and config); with `--env container` it isn't mounted, so `git` commands in the container fail.
- **Resuming and rewinding.** Trajectories and `run.started` record `worktree`. To resume, follow up or rewind, pass the worktree path as `--cwd` (without `--worktree`).

### Best-of-N attempts

`--attempts N` runs one task up to N times (at most 4), each in its own [worktree](#worktrees), grades every finished attempt with your check commands, and keeps a verified winner. `tinyagent --init-config` writes a commented `attempts:` block to start from, and the [GitHub Action](integrations.md#github-actions) takes the same settings as `attempts`, `attempt-models`, `attempt-checks` and `attempt-check-timeout` inputs.

```sh
tinyagent --attempts 3 --attempt-check "go test ./..." -t "fix the flaky retry test" -o run.json
tinyagent --attempts 2 --attempt-model openai/gpt-5 --attempt-model anthropic/claude-sonnet-4-5 \
  --attempt-check "make lint" --attempt-check "go test ./..." --worktree=retry -t "fix the flaky retry test"
```

- **How attempts run.** Attempts run one after another. Attempt *i* is a separate tinyagent run in a new worktree `<repo>/.tinyagent/worktrees/<name>-a<i>` on branch `tinyagent/<name>-a<i>`, where `<name>` is `--worktree=<name>` (at most 61 characters) or a generated `run-...` name. Every other flag you pass, the task among them, is forwarded to each attempt. tinyagent never commits, merges or pushes an attempt.
- **What counts as a pass.** An attempt passes when its run ends `Submitted` or `Completed` and every check exits 0 before its timeout. Checks run in order with `bash -c` in the attempt's working directory (the worktree path that matches `--cwd`); when the run didn't finish successfully its checks are skipped. The winner is the lowest-numbered passing attempt. By default tinyagent stops at the first pass; `attempts.stop_on_pass: false` runs all N attempts and compares them.
- **Checks.** `--attempt-check` (repeatable) with `--attempt-check-timeout` replaces the config's `attempts.checks` list, whose entries each take a `command` and an optional `timeout_seconds` (default 600, up to 3600). At least one check is required. Checks run where the attempts' commands run: with `--env container`, in a fresh container from `container.image` (same `run_args`) with the attempt's worktree and any additional directories mounted, started for that attempt's checks and removed after them; with `--sandbox`, under a sandbox rooted at the attempt's worktree with the same writable paths and network settings; with a `command_env` policy, with that environment. Otherwise they run on the host with your environment. stderr names the place (`attempts: checks run in the container, like the attempts' commands`). If the container or sandbox can't be set up, that attempt's checks fail with the reason and it can't win; they never fall back to the host. The report keeps the last 8 KiB of each check's output, with known secrets redacted.
- **Models.** Without `--attempt-model`, every attempt uses the model a single run would use (`-m`, then `$AGENT_MODEL`, then the config's `model:`). With it, give one `--attempt-model` per attempt. Every attempt's model, provider key and client are checked before the first attempt runs, and a problem exits 2 naming the attempt.
- **Budgets.** `--cost-limit` and `--time-limit` cover the whole invocation: each attempt gets what is left, and no new attempt starts once either is used up (stop reason `budget exhausted`). Without limits, N attempts can cost up to N times a single run, and the setup line says so.
- **Uncommitted changes.** Like `--worktree`, attempts start from the last commit. The dirty-checkout warning prints once for the invocation.
- **Output.** Progress goes to stderr: `attempt i/N: <model>`, a summary line per attempt (status, checks, cost, worktree), then `winner: attempt i (branch ...)` with a `git diff` hint, or `no attempt passed (<reason>); worktrees kept for inspection`. The winner's stdout (its submission) is printed to stdout at the end.
- **JSON events.** With `--json`, stdout carries JSONL instead: every event from each attempt's run, with an added `attempt` field (otherwise unchanged), plus `attempt.started` (`attempt`, `attempts_requested`, `model`, `worktree_name`), one `attempt.check` per check (`command`, `exit_code`, `duration_ms`, `timed_out`, `skipped`, `output`, with known secrets redacted from the command and output), `attempt.result` (the attempt's report fields without `checks`) and one final `attempts.result` (`schema_version`, `attempts_requested`, `attempts_run`, `winner`, `winner_branch`, `stop_reason`, `stop_on_pass`, `keep`, `removed`, totals, and `report` when `-o` wrote one). The winner's output isn't printed again at the end; its `result` event already carries the submission.
- **Log DB and traces.** Each invocation gets a random attempts ID, which is in the report and `attempts.result` as `attempts_id`. Every attempt's child run gets it in `TINYAGENT_ATTEMPTS_ID`, with `TINYAGENT_ATTEMPT` (its number) and `TINYAGENT_ATTEMPTS` (N), and its `run.started` event gains `attempt` (`attempts_id`, `index`, `count`). With the [event log](json-output.md#event-log-database) on, all attempts write to the log DB a single run would use (resolved against `--cwd`, not the attempt's worktree), the invocation adds an `attempt_groups` row, and each attempt's `runs` row has `attempts_id` and `attempt`, so `SELECT * FROM runs WHERE attempts_id = ? ORDER BY attempt` lists them. With [`--otel`](json-output.md#opentelemetry-export), the invocation exports one trace: a `tinyagent attempts` root span with every attempt's run under it (passed as `TRACEPARENT`; an inbound `TRACEPARENT` parents the root).
- **Trajectories and report.** With `-o run.json`, attempt *i*'s trajectory goes to `run.a<i>.json` and a report to `run.attempts.json`. The report (`schema_version` 1) has `attempts_requested`, `winner` (the attempt number or `null`), `winner_branch`, `stop_reason` (`passed`, `completed`, `exhausted`, `budget exhausted`, `interrupted` or `error`), `stop_on_pass`, `keep`, `total_cost_usd`, `total_wall_time_ms`, and one entry per attempt with its `model`, `status`, `passed`, steps, tokens, `cost_usd`, `wall_time_ms`, `worktree`, `branch`, `files_changed`, `trajectory`, `checks` (command, exit code, duration, `timed_out`, `skipped`, output), `error` and `removed`. Without `-o`, trajectories go to a temporary directory and no report is written.
- **Keeping and cleaning up.** `attempts.keep` decides which non-winning worktrees and branches survive once an attempt wins: `all` (the default), `failed` (remove passing attempts that didn't win) or `none` (remove every non-winner). The winner is always kept, nothing is removed when no attempt wins, and tinyagent removes only the exact worktree and branch it created for that attempt.
- **Config.** The `attempts:` section (see the [Config](config.md#config) example) is read only from the user config, `-c` or a profile, never from the repository, and has no effect without `--attempts`. Unknown keys or invalid values exit 2.
- **Checking the plan.** `tinyagent --check --attempts N ...` validates the plan the same way a real run would and prints an `attempts:` line with the run count, models, check count and timeouts (never the command text), `stop_on_pass`, `keep` and the worktree paths, without a model request.
- **Exit status and limits.** The exit status is 0 when an attempt wins and 1 when none does (or the report can't be written). Setup problems exit 2 before any model request: an out-of-range `--attempts`, a wrong number of `--attempt-model` values or mixing them with `-m`, no checks, no `-t` task (or `-t -`, since the task can't come from stdin), a `--cwd` outside a Git repository with commits, an attempt worktree or branch that already exists, or a flag that can't be combined with `--attempts`: `--resume`, `--rewind`, `--to`, `--transcript`, `--acp`, review flags, `--read-only`, `--ask-user`, `--snapshots`, `--eval`, `--workflow`, `--follow-up`, `--list-profiles`, `--init-config`, `--update` and `--version`.

### Resume an unfinished task

```sh
# Checkpoint before and after every tool call; this run stops at 20 cumulative model steps.
tinyagent -t "fix the failing test" --cwd ~/src/project --step-limit 20 -o run.json

# Inspect the workspace (for example, git diff) and rerun relevant checks.
# Continue the same task with a ceiling of 40 total steps.
tinyagent --resume run.json --cwd ~/src/project --step-limit 40 -o continued.json
```

When a run with `-o` stops at a step, cost or time limit, is interrupted, or hits `OutputTruncated`, the summary on stderr is followed by a ready-to-paste command with the resolved `--cwd`, the limit that was hit doubled (no limit flag after an interruption or truncation), and a new `-o` path (`-2`, then `-3`, ... before the extension):

```
StepLimitExceeded after 20 steps in 4m12s (48,213 input / 3,100 output tokens, $0.41)
resume: tinyagent --resume run.json --cwd /home/me/src/project --step-limit 40 -o run-2.json
```

It isn't printed with `--json`, without `-o`, or for `--output-schema` runs.

`--resume <trajectory.json>` is noninteractive and uses the saved task and model. Do not pass `--task` for an unfinished checkpoint (it is only for [follow-ups](#continue-a-finished-run-with-a-follow-up-task) of finished runs); an explicitly different `--model` is rejected. The saved model wins over `$AGENT_MODEL` and the config's default model. Credentials and provider endpoints come from the current normal setup, never the checkpoint. Paths to checkpoints are relative to the invoking shell's directory, independently of `--cwd`.

An explicit `--cwd` must resolve to the recorded canonical root (directory symlinks are resolved). For a [`--worktree`](#worktrees) run, that is the path inside the worktree. The effective system prompt must match the saved prompt, including `{{cwd}}`/`{{os}}` substitutions and the recorded AGENTS.md loading mode. Current config is used to reconstruct it without appending AGENTS.md twice. Changed instructions, a conflicting `--no-agents-md`, or changed tool definitions/compatibility require a fresh task. In particular, a task that edits its effective AGENTS.md cannot resume under changed instructions.

Only new-format, unfinished checkpoints are eligible: an in-progress save (including one left by a crash or `kill -9`) or a stop caused by a step/cost/time limit, interruption, or error. Successful `Submitted`/`Completed` runs are not resumed this way; continue them with a follow-up task instead (see below). Legacy logs, unsupported versions/state, and malformed or partial tool turns are refused before provider setup. Checkpoint loading is limited to regular files of at most **64 MiB**. Larger trajectories remain audit logs.

With `-o`, the checkpoint is written before tools run (recording the calls as `pending_calls`) and again after each tool result. If the process dies mid-turn, `--resume` gives every call without a result the error result `[interrupted: tool did not complete before the agent stopped; it may have partially run — verify state before retrying]` and emits `resume.interrupted_call`. **Interrupted calls are never re-executed automatically**: a half-run `bash` or edit may have had side effects, so the model decides what to inspect or retry. Background processes started with `start_process` are recorded with their PID; they do not survive into a new invocation, so resume emits `resume.process_lost` and tells the model which ones are gone.

Compacted runs resume too. The trajectory always keeps the full history; the model's compacted view is rebuilt from the last `compactions` record (its message map, elisions, and summary) plus the messages after it, so the resumed request is the same one the original process would have sent. `--no-compact` is no longer needed to keep a run resumable.

A distinct `-o`/`--output` is required, and any existing destination is refused, including symlink and hard-link aliases of the source. The input stays unchanged. The validated starting state is saved to the new destination before the first request; completed tool turns and termination then replace it using a private temporary file. Initial publication requires filesystem hard-link support to publish atomically without replacing another file. A save failure in resume mode stops further work, prints an error, and exits nonzero, preserving the last complete checkpoint. Ordinary fresh-run save failures retain their existing warning behavior. Each save writes a private temporary file, fsyncs it, renames it over the checkpoint, and fsyncs the directory (where the platform supports it), so a crash or power loss leaves either the previous or the new complete checkpoint, never a partial one.

Steps, cost, token counts, and cache totals remain cumulative. Step/cost limits are cumulative ceilings: if already exhausted, no model request is made; increase the ceiling explicitly (or set it to 0 for unlimited). Runtime limits, tool timeouts, output settings, and compaction settings use normal config/flag precedence. Wall time and the stderr elapsed duration restart for each invocation; stopped time is not charged. Normal streaming, summaries, and success/failure exit codes still apply. The next action is a new model query, which incurs ordinary context/token costs; loading a checkpoint never dispatches historical tool calls.

New JSON keeps the existing top-level trajectory fields and adds versioned metadata: `schema_version` (currently 2; version 1 files still load), `root`, `system_prompt`, `skip_agents_md`, `tools`, and `tool_signature`. Version 2 adds `pending_calls`, `processes`, `plan_enabled`, `plan`, and `plan_history`; runs with skills enabled also record `skills_enabled` and `skills` (name, scope, and path of each discovered skill). `resume` records the input path, previous status/submission, cumulative step, and continuation-message index, plus `follow_up: true` for a follow-up boundary; nested `previous` entries retain earlier resume boundaries. These audit paths are not read as configuration. Metadata excludes credentials and environment dumps. Checkpoints still contain prompts, code, and tool outputs: treat them as private, trusted local execution inputs. New checkpoint and temporary files use owner-only permissions (0600) where supported.

A checkpoint restores **conversation state**, not files (to restore files, record the run with `--snapshots` and use [`--rewind`](undo.md#rewind-workspace-changes)), Git state, shell variables, live processes, or external services. The model receives a continuation notice and stderr warns to inspect and revalidate the current workspace. Commands may have produced unrecorded side effects after the last save, especially during a hard crash in a tool batch. Historical process IDs cannot reattach to children from another invocation. There is no rollback or exactly-once execution guarantee; inspect current state before resuming.

### View a run as a transcript

`--transcript <trajectory>` renders a saved `-o` file as Markdown on stdout, so you can see what an unattended run read, ran and changed, and why, without reading the raw JSON. It reads no config, needs no API key, makes no model or network request and writes no file.

```sh
tinyagent --transcript run.json | less
tinyagent --transcript run.json > run.md
```

**What it shows.** A header with the exit status, model (and the model serving at the end, after a fallback), profile, steps, token totals (cache included), cost, and the read-only, sandbox, environment, worktree and review settings. Then the task, and a `### Step N` section per step, numbered as in `steps`: the assistant's text, each tool call with its arguments pretty-printed, and each result in a collapsed `<details>` block (marked when it was a format-error correction). Tool calls without a result, as in an interrupted checkpoint, are listed as such. Plan updates (with the final plan as a checklist), compactions, model switches, subagent delegations and `--resume`/follow-up boundaries appear where they happened. Every message is shown, including ones compaction removed from the model's view. The footer has the submission (or the `--output-schema` result as JSON) and the snapshot ids for `--rewind`. The system prompt is left out.

**Safety.** All text that came from the task, the model, tools, MCP servers or hooks is written inside fenced code blocks (or inline code) longer than any backtick run it contains, so it can't render as an image, link or HTML wherever the Markdown is shown. Images are never inlined: each is a line like `[image: image/png, shot.png, 48,213 bytes]`. Secrets are masked as `[REDACTED]` with the same redactor as the event log: values of environment variables named `*_KEY`, `*_TOKEN`, `*_SECRET` or `*_PASSWORD` in the environment you run `--transcript` in, and `sk-…`, `ghp_…`, `Bearer …` and `xox…-` shaped tokens. Redaction is best-effort: review a transcript before posting it publicly.

**Size.** Each tool result, tool call's arguments, user message (the task included), plan diff and compaction summary is cut to its first 2,000 characters, with a `[... N characters omitted ...]` notice. Assistant text and the submission are shown whole.

**In CI.** Append it to the GitHub Actions job summary (the [GitHub Action](integrations.md#github-actions) does this with `job-summary: true`). A job summary is limited to 1 MiB per step and fails to upload above that, so very long runs may not fit:

```yaml
- run: tinyagent --profile ci -t "fix the failing test" -o run.json
- if: always()
  run: tinyagent --transcript run.json >> "$GITHUB_STEP_SUMMARY"
```

Any trajectory tinyagent writes renders, finished or not, including old files and ones `--resume` would refuse; fields it doesn't know are ignored. A missing, non-regular, empty or over-64-MiB file, invalid JSON or an unsupported `schema_version` exits 2 with one error line and nothing on stdout. `--transcript` can't be combined with `-t`, `--resume`, `--rewind`, `--check`, `--json`, `-o` or the other mode flags (exit 2).

### Continue a finished run with a follow-up task

A run that finished successfully (`Submitted` or `Completed`) can be continued with a new instruction by passing `-t` with `--resume`. The model sees the whole previous conversation, so it keeps its earlier findings without re-reading the code:

```sh
# Stage 1: review and report.
tinyagent -t "review internal/agent for data races; report them" --cwd . -o review.json

# Stage 2: act on the findings in the same conversation.
tinyagent --resume review.json -t "fix the races you found" --cwd . -o fix.json

# Or read the follow-up from stdin.
cat next.md | tinyagent --resume fix.json -t - --cwd . -o next.json
```

- A finished checkpoint requires a non-empty follow-up; `--resume finished.json` without `-t` exits 2 and says so. An unfinished checkpoint still refuses `-t` (exit 2): continue it with plain `--resume`.
- Every other resume rule applies unchanged: the same model, root, system prompt, tools and loading mode, a distinct new `-o`, the unchanged source file, and every refusal before provider setup. Runs with `--output-schema` or MCP tools, runs continued by `stop` hooks, and `VerificationFailed` runs are not eligible.
- The request is the saved history (or the rebuilt compacted view) followed by one user message: a notice that the previous invocation finished and the workspace may have changed, then the follow-up text. No historical tool call is re-executed. The trajectory's top-level `task` stays the original task; the follow-up text is recorded in that message.
- Steps, cost, and tokens stay cumulative, so `--step-limit`, `--cost-limit` and `--token-limit` count the earlier runs too. If the ceiling is already used up, no request is made and the run ends `StepLimitExceeded`, `CostLimitExceeded` or `TokenLimitExceeded`; raise the ceiling for the follow-up.
- Follow-ups chain: a follow-up's finished result can take another follow-up, and a follow-up stopped by a limit continues with plain `--resume`. Each boundary is recorded in `resume` with `follow_up: true`, and in `--json` `run.started` carries `resumed: true` and `follow_up: true`.
- Checkpoints with a follow-up boundary use the existing schema version; older tinyagent binaries refuse them with an unknown-field error rather than misreading them.
- A `NeedsInput` run is answered the same way, but the answer is delivered differently; see [Asking for input](#asking-for-input).

### Asking for input

`--ask-user` (or `ask_user.enabled: true`) gives the model an `ask_user` tool for headless runs. When information that blocks the task is missing (which of two APIs to change, whether a breaking change is OK), the model can call it with one question instead of guessing. The run then ends cleanly with status `NeedsInput` and exit code 4; nothing waits on a terminal, so nothing can hang in CI. Whoever started the run answers later with `--resume`:

```sh
tinyagent --ask-user -t "add rate limiting to the API" -o run.json
# stderr:
# NeedsInput after 6 steps in 41s (...): Should the limit be per API key or per client IP?
#   options: per API key | per client IP
# answer with: tinyagent --resume run.json --cwd /repo -t "<answer>" -o run-2.json

tinyagent --resume run.json --cwd . -t "per API key" -o run-2.json
```

- **The tool.** `ask_user` takes a `question` (required, non-empty, at most 2,000 characters) and optional `options`: 2 to 6 distinct suggested answers of at most 200 characters each. The person may answer with any text. A short notice in the system prompt tells the model to ask only when it is blocked and the answer isn't in the workspace, and otherwise to proceed and state its assumptions.
- **Ending the run.** A valid `ask_user` call that is the only tool call in its message ends the run. The call is saved in the trajectory as pending (`pending_calls`), with the question in `pending_question` (`tool_call_id`, `question`, `options`). The question goes to stdout (so `q=$(tinyagent ...)` captures it) and, with the options and a ready-to-paste `--resume` command, to stderr. With `--json`, the final `result` event has `status: "NeedsInput"`, `submission` set to the question, and a `question` object; `--log-db` stores the same, and at level `metadata` it keeps only the question's length. No hooks run for `ask_user`, and stop hooks don't run for a `NeedsInput` end.
- **Answering.** `--resume <trajectory> -t "<answer>"` (or `-t -` for stdin) sends the saved history and delivers the answer as the result of the pending `ask_user` call, after a short notice that the workspace may have changed; no user message is added, so the model sees the answer attached to its own question. Without `-t`, or with an empty answer, `--resume` exits 2 and prints the pending question; `--image` can't be attached to an answer. Every other resume rule applies: the same model, root, system prompt and tools (a conflicting `--ask-user` exits 2), a new `-o`, and cumulative steps, cost and tokens. The boundary is recorded in `resume` with `answer: true`.
- **Misuse.** `ask_user` together with other tool calls in one message runs none of them; each gets an error asking the model to call `ask_user` alone. Malformed arguments (missing or empty `question`, wrong types, too few, too many or duplicate `options`) are [format errors](troubleshooting.md#malformed-tool-calls). After `ask_user.max_questions` questions (default 3, counted across the whole resume chain and recorded as `questions_asked`), further calls get an error telling the model to proceed on stated assumptions, and the run continues.
- **Where it applies.** It is offered with `--read-only` and in review mode, never to [subagents](long-runs.md#subagents). `--ask-user` without `-o`, or with `--output-schema`, exits 2 before any request, since the question could never be answered. With it off, tools, prompts, trajectories, events and exit codes are unchanged; older tinyagent binaries refuse `NeedsInput` checkpoints rather than misreading them.

The question is model-written text that can quote workspace files or fetched pages; treat it as untrusted if you forward it to other systems.

## Token and cost budgets

`--token-limit n` (config `token_limit`) caps the run's provider-reported input plus output tokens; prompt-cache tokens are part of input. `--cost-limit usd` (config `cost_limit`) caps its cost, computed from the same usage and the model's price (the built-in table, or `input_price_per_mtok` and `output_price_per_mtok`). Both are off (0) by default, and delegates and workers count against the parent's budget. A model with no known price can't be held to a cost limit: tinyagent warns once at start, and only the token limit is enforced.

When a token or cost budget reaches 80%, the model gets one user message saying how much is used and asking it to wrap up; the JSON stream, log database and OpenTelemetry trace get a `budget.warning` event. At 100% the run stops cleanly with `TokenLimitExceeded` or `CostLimitExceeded` (exit 1), and the printed `--resume` command doubles the limit that was hit.
