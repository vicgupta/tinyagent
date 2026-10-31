# Playbooks

A playbook is a short Markdown checklist for one kind of task: fix a bug,
build a feature, refactor, open a pull request, babysit a pull request until
it merges, or investigate a question. With playbooks on, TinyAgent picks the
playbook that fits the task, puts its steps in the system prompt as a
checklist, loads the skills it names, and adds its done check to
[verify before stop](verify.md).

Playbooks and skills are read only from inside the current working directory.

Playbooks are off by default.

```sh
tinyagent playbooks install samples      # write six samples to ./playbooks
tinyagent --playbooks -t "the parser crashes on empty input; fix it"
# playbook: bug-fix (keywords: "crash", "fix")
tinyagent --playbook investigate -t "how does compaction decide what to keep?"
```

## Turning it on

| How | Effect |
|---|---|
| `--playbooks` or config `playbooks: true` | Route each run's task to a playbook |
| `--playbook <name>` | Follow this playbook; implies `--playbooks` and skips routing |
| `--no-playbook-fallback` or config `playbook_fallback: false` | Don't ask the model when keywords tie or miss |

`--playbooks` counts as your consent to follow the playbooks in `./playbooks`,
including the commands in their done checks.

## How a playbook is chosen

1. **Keywords.** Each playbook's `intent` and `match` words are scored
   against the task the same way [skill workflows](project-context.md#skill-workflows)
   route (shared content words; naming the playbook wins outright). A clear
   best match with at least two shared words wins.
2. **Model fallback.** On a tie or a miss, TinyAgent makes one call to the
   auxiliary model (the `aux` role, see [models](models.md)) listing the
   playbooks and the task; it answers a name or `none`. Fallback is on by
   default when playbooks are on.
3. **No playbook.** If the call fails, answers `none` or answers a name that
   isn't a playbook, the run goes on without a playbook and says why.

You always see the choice:

- stderr: `playbook: bug-fix (keywords: "crash", "fix")`, `playbook: feature (model fallback)`, `playbook: investigate (--playbook)` or `playbook: none (...)`.
- `--json`: a `playbook.selected` event with `name`, `method` (`named`, `keywords` or `model`), `path`, `format` and the matched `keywords`.
- `-o`: the trajectory's `playbook` field records the name, method, path and content hash.
- `--check`: a `playbooks` row lists the playbooks found.

## What a chosen playbook does

- Its steps go into the system prompt as a checklist, with its intent and done check.
- The skills it lists in `skills:` load with it: their `SKILL.md` instructions are added after the checklist (only those skills; no `--skills` needed). A missing skill is a warning.
- Lines of its done check that start with `$ ` are commands added to `verify_on_stop.checks`; they run when [verify before stop](verify.md) is on.
- `read_only: true` turns [read-only mode](runs.md) on for the run.
- `workflow: <file.yaml>` hands the task to that [skill workflow](project-context.md#skill-workflows) instead. A workflow runs commands, so this needs `--trust-config`; without it the run exits 2 and says so.

## A playbook and a workflow both match

With `--workflow file.yaml` and playbooks on, when a playbook clearly matches
and the workflow routes the task by intent, TinyAgent asks at a terminal:

```
Use playbook "bug-fix" or workflow "fix"? [p/w]
```

Without a terminal (piped stdin, `-t -`, `--json`) it exits 2 before any model
call and asks you to pass `--playbook <name>` or `--workflow-name <name>`.
Naming either one skips the question.

## Writing a playbook

Put `<name>.md` or `<name>/PLAYBOOK.md` in `./playbooks`:

```markdown
---
name: bug-fix
intent: Fix a bug by reproducing it first, fixing the cause and proving it with a test.
match: [bug, crash, repro, regression]
skills: [go-testing]     # from ./skills/<name>/SKILL.md
read_only: false
# workflow: workflows/fix.yaml   # optional handoff; needs --trust-config
---

1. Reproduce the bug with a failing test.
2. Fix the cause.
3. Run the tests.

## Done when

The new test passes and the suite is green.
$ go test ./...
```

Rules: `name` (lowercase letters, digits, hyphens; must match the file or
directory name) and `intent` are required; the body needs a numbered or
bulleted list of steps and a `## Done` section. Unknown frontmatter keys are
errors. Check a file with `tinyagent playbooks validate playbooks/bug-fix.md`.

The six samples are in [docs/playbooks/](playbooks/): `bug-fix`, `feature`,
`refactor`, `open-pr`, `babysit-pr` and `investigate` (`read_only: true`).
`babysit-pr` keeps a pull request up to date by merging main in (never a
rebase or force-push), fixes failing CI up to three tries, and merges with a
merge commit only when every check is green, the merge state is clean and no
changes are requested; it never uses an admin override and asks for no
confirmation. Otherwise it stops and reports.

## pstack playbooks

A checkout of [pstack](https://github.com/cursor/plugins) under `./playbooks`
is read as-is: TinyAgent looks for `skills/poteto-mode/playbooks/*.md` (also
under a `pstack/` subdirectory of the checkout) and loads their skills from
that checkout's `skills/`. A pstack playbook's title and opening section are
its intent, its numbered list is its steps, and its `**Reply:**` line is its
done check. A file it can't parse is skipped with a warning. When a pstack
playbook and one you wrote share a name, yours wins (with a warning).

## Where files are read from

Playbooks and skills are read only from inside the current working directory.

- Playbooks: `./playbooks` in `--cwd`, including pstack checkouts under it. Nothing else: not your home directory, the config directory, plugin directories or absolute paths.
- Skills a playbook names: `./skills/<name>/SKILL.md`; pstack skills come from the checkout's `skills/`.
- A path that is absolute, contains `..`, or resolves through a symlink to somewhere outside `--cwd` is refused with a warning.

## Subcommand

| Command | What |
|---|---|
| `tinyagent playbooks list` | Print the playbooks in `./playbooks` with their format and intent |
| `tinyagent playbooks validate <path>` | Check one playbook file; exit 0 valid, 1 invalid, 2 unreadable or outside `--cwd` |
| `tinyagent playbooks install samples [--force]` | Write the six samples to `./playbooks`; existing files are kept unless `--force` |
| `tinyagent playbooks` | Print this help |

None of them makes a model call.

## Chat and resume

- In [chat](chat.md), `/playbook <name>` applies a playbook to the next message; `tinyagent chat --playbook <name>` applies it to the first. Chat doesn't route automatically.
- A checkpoint records the playbook's name and a hash of the playbook and the skills it loaded. `--resume` re-applies it, and refuses when the playbook or one of its skills changed.
