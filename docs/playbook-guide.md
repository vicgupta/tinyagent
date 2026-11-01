# Setting up playbooks: a guide with examples

This page shows how to set up [playbooks](playbooks.md) from scratch. It has three worked examples: easy, medium and complex. [playbooks.md](playbooks.md) is the reference; for multi-stage pipelines see [Setting up workflows](workflow-guide.md).

- [Overview](#overview)
- [Setup](#setup)
  - [Where the files live](#where-the-files-live)
  - [Required and optional fields](#required-and-optional-fields)
  - [Turning playbooks on](#turning-playbooks-on)
- [Examples](#examples)
  - [1. Easy: a short checklist](#1-easy-a-short-checklist)
  - [2. Medium: a longer checklist with skills](#2-medium-a-longer-checklist-with-skills)
  - [3. Complex: a checklist that hands off to a workflow](#3-complex-a-checklist-that-hands-off-to-a-workflow)
- [Playbooks or workflows?](#playbooks-or-workflows)
- [How a playbook is chosen](#how-a-playbook-is-chosen)
- [Handing off to a workflow](#handing-off-to-a-workflow)
- [Resume](#resume)
- [Limits worth knowing](#limits-worth-knowing)

Every file on this page is checked by `TestPlaybookGuideExamples` (`internal/playbooks/guide_test.go`). A block preceded by `<!-- file: ... -->` is written to a temporary `--cwd`. The test then requires that:

- every playbook is found by the real discovery and parser with no warnings
- every skill it names loads, and is a valid Agent Skill
- the handoff workflow loads with the real workflow loader
- each `playbook: ...` line under a `<!-- route -->` block is what the keyword router prints for that task

## Overview

A playbook is soft guidance inside **one** agent run. It is a Markdown file with:

- a short YAML header
- a checklist of steps
- a `## Done when` section

When a playbook is chosen:

- its steps go into the system prompt as a checklist the model works through
- the skills it names are added after the checklist
- every line of the done section that starts with `$ ` becomes a command check for [verify before stop](verify.md)

Nothing forces the model to follow the steps in order. The model reads them as instructions; only the `$ ` commands are run by TinyAgent itself. A [workflow](workflows.md), by contrast, runs separate agent runs stage by stage, with hard checks between them.

## Setup

### Where the files live

Playbooks live in `./playbooks` inside `--cwd`, and the skills they name live in `./skills`:

```text
playbooks/
  fix-lint.md                 # <name>.md
  add-endpoint/PLAYBOOK.md    # or <name>/PLAYBOOK.md
skills/
  http-handlers/SKILL.md      # skills: [http-handlers]
```

- Nothing else is read: not your home directory, the config directory, plugin directories or absolute paths.
- A path with `..`, or a symlink that leaves `--cwd`, is refused with a warning.
- `README.md` in `./playbooks` is ignored.
- A file that doesn't parse is skipped with a warning, and the other playbooks still load.

To start from the six samples (`bug-fix`, `feature`, `refactor`, `open-pr`, `babysit-pr`, `investigate`), run:

```sh
tinyagent playbooks install samples     # existing files are kept; add --force to overwrite
tinyagent playbooks list                # name, path, intent, format
tinyagent playbooks validate playbooks/fix-lint.md
```

None of these make a model call. `validate` exits 0 when the file is valid, 1 when it is invalid, and 2 when it is unreadable or outside `--cwd`. It warns about any named skill that is missing.

### Required and optional fields

**Frontmatter.** The file must start with `---`. Unknown keys are an error.

| Key | Required | Rule |
|---|---|---|
| `name` | yes | lowercase letters, digits and hyphens; must equal the file stem (`fix-lint.md`) or the directory name (`add-endpoint/PLAYBOOK.md`) |
| `intent` | yes | one sentence; it is scored by the router and shown to the model |
| `match` | no | extra routing words or phrases, e.g. `[lint, vet, gofmt]` |
| `skills` | no | skill names (no paths), each loaded from `./skills/<name>/SKILL.md`; a missing one is a warning |
| `read_only` | no | `true` turns [read-only mode](runs.md) on for the run (default `false`) |
| `workflow` | no | a workflow YAML path relative to `--cwd` (no absolute path, no `..`); hands the task to that workflow (needs `--trust-config`) |

**Body.**

- **Steps:** at least one numbered (`1.`) or bulleted (`-`, `*`) item before the done section. `- [ ]` checkboxes are fine. An indented line continues the step above it.
- **Done section:** a heading whose text starts with "Done", e.g. `## Done when`. It is required. It ends at the next heading of the same or a higher level.
- **Done-when commands:** lines in the done section that start with `$ ` (dollar, space). Every other line is prose for the model.

### Turning playbooks on

| How | Effect |
|---|---|
| `--playbooks` or config `playbooks: true` | route each run's task to a playbook (off by default) |
| `--playbook <name>` | follow this playbook; it implies `--playbooks` and skips routing |
| `--no-playbook-fallback` or config `playbook_fallback: false` | no model call when keywords tie or miss |
| `--verify-on-stop` or config `verify_on_stop.enabled: true` | needed for the `$ ` done-when commands to run |

The done-when commands are appended to `verify_on_stop.checks`. They run only when [verify before stop](verify.md) is on and the run changed files. They run in order, as `bash` tool calls, and stop at the first failure, which sends the model back. With the guard off, they are part of the prompt but are never run. Like any configured checks, they replace the built-in "looks like a test command" patterns.

## Examples

### 1. Easy: a short checklist

**When to use it:** a small, repeatable chore with one or two commands that prove it's done.

<!-- file: playbooks/fix-lint.md -->
```markdown
---
name: fix-lint
intent: Fix go vet and gofmt findings without changing behavior.
match: [lint, vet, gofmt, format]
---

1. Run `go vet ./...` and `gofmt -l .` to list the findings.
2. Fix each finding with the smallest edit; don't refactor.
3. Run both again until they are clean.

## Done when

`go vet` reports nothing and no file needs formatting.
$ go vet ./...
$ test -z "$(gofmt -l .)"
```

<!-- route -->
```text
$ tinyagent --playbooks --verify-on-stop -t "fix the go vet warnings and gofmt the code"
playbook: fix-lint (keywords: "fix", "gofmt", "vet")
```

The task shares three content words with the intent and `match`, so the keyword router picks it with no model call. When the model stops after editing, TinyAgent runs the two `$ ` commands. If either fails, the model is sent back with its output.

### 2. Medium: a longer checklist with skills

**When to use it:** a recurring kind of change with house rules worth writing down once, such as how handlers and tests look here, and several checks.

<!-- file: playbooks/add-endpoint/PLAYBOOK.md -->
```markdown
---
name: add-endpoint
intent: Add or change an HTTP API endpoint with a handler, routing, table tests and docs.
match: [endpoint, handler, route, api, http]
skills: [http-handlers, go-table-tests]
---

1. Read `internal/api/routes.go` and one existing handler to copy its shape.
2. Write the handler in `internal/api/`, following the http-handlers skill.
3. Register the route in `internal/api/routes.go`.
4. Add table tests next to the handler, following the go-table-tests skill:
   one success case, one bad-input case and one not-found case.
5. Document the endpoint in `docs/api.md` (method, path, request, response, errors).
6. Run the checks below and fix anything they report.
7. Finish with a short summary: the route, the files changed and the tests added.

## Done when

The new route answers as documented and every check passes.
$ go build ./...
$ go vet ./...
$ go test -count=1 ./internal/api/...
```

<!-- file: skills/http-handlers/SKILL.md -->
```markdown
---
name: http-handlers
description: House style for HTTP handlers in internal/api - errors, JSON and status codes.
---

# HTTP handlers

- A handler is a method on `*Server` named after the route, e.g. `getUser`.
- Decode JSON with `decodeJSON(r, &req)`; on error reply `400` with `writeError`.
- Reply with `writeJSON(w, status, v)`; never call `w.Write` directly.
- Return `404` via `writeError(w, http.StatusNotFound, "...")` when a lookup misses.
- No package-level state; take dependencies from `*Server`.
```

<!-- file: skills/go-table-tests/SKILL.md -->
```markdown
---
name: go-table-tests
description: How to write table-driven Go tests with httptest in this repository.
---

# Table tests

- One `TestXxx` per handler with a `tests := []struct{ name string; ... }` table.
- Use `httptest.NewRecorder()` and `httptest.NewRequest`; don't start a server.
- Assert the status code first, then decode and compare the body.
- Call `t.Parallel()` in the test and in each `t.Run`.
```

<!-- route -->
```text
$ tinyagent --playbooks --verify-on-stop -t "add a GET /users/{id} endpoint with a handler and tests"
playbook: add-endpoint (keywords: "add", "endpoint", "handler", "test")
```

How it runs:

- The seven steps go into the system prompt as `- [ ] 1. ...` checkboxes, followed by the done section.
- The two skills are loaded with the playbook. Their `SKILL.md` text, without its frontmatter, is appended under `### Skill <name> (loaded by the playbook)`. You don't need `--skills` for this.
- The files also sit in `./skills`, so they are valid [Agent Skills](project-context.md#agent-skills). When `--skills` is on, other runs can load them through the `skill` tool.
- The three `$ ` commands run in order at stop, cheapest first.

### 3. Complex: a checklist that hands off to a workflow

**When to use it:** a task needs hard gates between steps, such as tests before a changelog or a version bump, so that one run can't skip ahead. You still want it found the same way as your other playbooks.

The playbook routes the task. Its `workflow:` key then hands the whole task to a [workflow](workflow-guide.md), which runs each stage as its own agent run with its own check.

<!-- file: playbooks/release.md -->
```markdown
---
name: release
intent: Prepare a release - green tests, changelog, version bump and release notes, gated stage by stage.
match: [release, version, changelog, tag, ship]
workflow: .tinyagent/release.yaml
---

1. Make sure the working tree is clean and on main.
2. Run the full test suite with the race detector; stop if anything fails.
3. Move the `## Unreleased` changelog entries under a new version heading.
4. Bump the version constant in `internal/version/version.go`.
5. Write release notes in `dist/RELEASE_NOTES.md` from the changelog section.
6. Check that the notes mention every entry in the section.
7. Don't tag, push or publish; a person does that.

## Done when

Tests pass, the changelog has the new version and the notes exist.
$ go test -race -count=1 ./...
$ grep -q '^## \[v' CHANGELOG.md
$ test -s dist/RELEASE_NOTES.md
```

<!-- file: .tinyagent/release.yaml -->
```yaml
version: 1
default: release
defaults:
  retries: 1
workflows:
  release:
    intent: Prepare a release with green tests, a changelog section, a version bump and release notes.
    stages:
      - skill: release-test
        check: [go, test, -race, -count=1, ./...]
        retries: 2
        check_timeout: 900
      - skill: release-changelog
        check: [grep, -q, '^## \[v', CHANGELOG.md]
      - skill: release-notes
        check: [test, -s, dist/RELEASE_NOTES.md]
```

<!-- file: .tinyagent/skills/release-test.md -->
```markdown
# Skill: release-test
> Intent: Get the full test suite green with the race detector before a release.

**Inputs**
- task
- repo_path

**Success check:** `go test -race -count=1 ./...` passes.

Fix real failures only; don't skip or delete tests.
```

<!-- file: .tinyagent/skills/release-changelog.md -->
```markdown
# Skill: release-changelog
> Intent: Move the Unreleased changelog entries under the new version and bump the version constant.

**Inputs**
- task
- previous_output

**Success check:** CHANGELOG.md has a `## [vX.Y.Z]` heading for the new version.
```

<!-- file: .tinyagent/skills/release-notes.md -->
```markdown
# Skill: release-notes
> Intent: Write dist/RELEASE_NOTES.md from the new changelog section.

**Inputs**
- previous_output

**Success check:** dist/RELEASE_NOTES.md exists and covers every entry in the section.
```

<!-- route -->
```text
$ tinyagent --playbooks --trust-config -t "prepare the v1.4.0 release"
playbook: release (keywords: "release", "prepare", "release")
```

How it runs:

- The task names the playbook (`release`), and a name in the task wins routing outright. The router's keyword list then shows the name first, followed by the shared words.
- Because of `workflow:`, the run doesn't start an agent with the checklist. It hands the task to `.tinyagent/release.yaml`, resolved relative to `--cwd`. The workflow routes it with its own intent or `default`, and runs the three stages with their checks (exit 0 Passed, 1 Failed, 5 Blocked).
- The playbook's steps, done section and `$ ` commands are **not** used in a handoff, and `read_only` and `skills` don't apply either. They still have to parse, so the file is valid. Keep them in step with the stages so that they document what the workflow does.
- `--trust-config` is required because a workflow runs commands. Without it, the run prints `error: playbook release hands off to workflow .tinyagent/release.yaml, which runs commands; pass --trust-config to allow it` and exits 2.
- In chat, `/playbook release` is refused, because chat can't run a workflow. Use `tinyagent --playbook release --trust-config -t ...`.

## Playbooks or workflows?

| | Playbook | Workflow |
|---|---|---|
| What runs | one agent run with a checklist in its prompt | one agent run per stage, in order |
| Order of steps | guidance; the model may reorder or merge steps | enforced; a stage starts only after the previous one passed |
| Checks | `$ ` done-when commands at stop (needs `--verify-on-stop`); a failure sends the model back, up to `max_reprompts` | `check:` / `behavior:` / `Success check:` per stage, with `retries` (0–5) |
| Files | one Markdown file (plus optional skills) | YAML file plus a skill file per stage |
| Choosing | keywords, then one aux-model call | `--workflow-name`, or routing by intent or `default` |
| Cost | one run | one run per stage, plus retries |
| Resume | `--resume` re-applies it (see [Resume](#resume)) | refused; re-run from stage 1 |
| Chat | `/playbook <name>` for one message | not available |
| Trust | `--playbooks` is your consent to run its `$ ` commands | `--workflow` needs no `--trust-config`; a playbook handoff does |

Use a **playbook** when one run can do the job and you mainly want it done your way every time: a checklist, house rules in skills, and a check at the end. Use a **workflow** when a step must not start before another is proven, when steps need separate budgets or retries, or when you want a pass/fail exit code per stage. To get both, route with a playbook and hand off with `workflow:`, as in example 3.

## How a playbook is chosen

With `--playbooks` (or `playbooks: true`), the router uses the same scoring as [workflow routing](workflows.md#routing-and-matching) (`internal/harness`):

1. **Keywords.** The task and each playbook's `intent` plus `match` words are reduced to content words. The router lowercases them, drops words shorter than three letters and stop words (`the`, `and`, `with`, `use`, ...), and strips simple suffixes (`tests` → `test`, `planning` → `plan`). The score is the number of shared words. If the playbook's `name` appears as a whole word in the task, it gets +100, so naming a playbook wins. A single best score of at least 2 wins. A tie, or a best score below 2, is a miss.
2. **Model fallback (#326).** On a tie or a miss, TinyAgent makes one call to the auxiliary model (the `aux` role; see [models](models.md)). The call lists every playbook's name and intent (intent cut to 300 characters) and the task, and asks for a name or `none`. The call times out after 2 minutes. Fallback is on by default; `--no-playbook-fallback` or `playbook_fallback: false` turns it off.
3. **No playbook.** If the fallback is off, fails, answers `none`, or answers a name that isn't a playbook, the run goes on as a normal run. It prints why, e.g. `playbook: none (no clear keyword match; model fallback is off)`.

`--playbook <name>` skips all of this, and an unknown name exits 2 with the list of playbooks. The choice is always shown:

- on stderr: `playbook: fix-lint (keywords: ...)`, `(model fallback)` or `(--playbook)`
- with `--json`: as a `playbook.selected` event
- with `-o`: in the trajectory's `playbook` field

Tip: give each playbook a distinct `intent` and a few `match` words that real tasks use. Run `tinyagent playbooks list` to see what the router will consider.

## Handing off to a workflow

There are two ways a playbook and a workflow meet.

**1. A playbook with `workflow:` (#332).** When that playbook is chosen, the run goes to the workflow instead of starting an agent. Other flags you set are forwarded to the stages as with `--workflow`.

- It needs `--trust-config`; without it the run exits 2 before the agent or any stage starts. Only the routing fallback call, if one was needed, has been made by then.
- The path is relative to `--cwd`. A path that is absolute, has `..`, or leaves `--cwd` is refused. The parser rejects the first two, and the handoff refuses anything that resolves outside `--cwd`.

**2. `--workflow file.yaml` together with playbooks on.** If a playbook clearly matches by keywords and the workflow also routes the task by intent, TinyAgent asks:

```text
Use playbook "add-endpoint" or workflow "change"? [p/w]
```

- `p` follows the playbook and drops `--workflow`; `w` runs the workflow.
- Without a terminal (piped stdin, `-t -` or `--json`) it doesn't ask. It exits 2 before any model call with `error: both playbook ... and workflow ... match this task; pass --playbook <name> or --workflow-name <name> to choose`.
- Passing `--playbook` or `--workflow-name` skips the question. Passing both is an error.

## Resume

A run saved with `-o run.json` records the playbook's name, how it was chosen, its path, its format and a content hash. The hash is SHA-256 over the playbook file's bytes and the bytes of each skill it loaded.

- `--resume run.json` finds the playbook by name in `./playbooks`, recomputes the hash, and re-applies the checklist, skills and done-when commands. It prints `playbook: <name> (from checkpoint)`.
- If the playbook or one of its skills changed (even whitespace), resume refuses with `playbook "<name>" (or a skill it loads) changed since this checkpoint; restore it or start a new run`.
- If the playbook is gone, resume refuses and says so. Routing doesn't run again on resume.
- A handoff run has no playbook record, and workflows can't be resumed.

## Limits worth knowing

These follow from the code (`internal/playbooks`, `cmd/tinyagent/playbooks.go`):

- **Soft order.** The checklist is prompt text. Only the `$ ` commands are run by TinyAgent, and only with verify-on-stop on and files changed.
- **One playbook per run.** Playbooks don't stack. In chat, `/playbook` applies to the next message only, and chat never routes automatically.
- **Sizes.** A playbook file over 256 KiB is skipped. A loaded skill is cut to 32 KiB with a warning.
- **Skills by name only.** A `skills:` entry is a name, not a path. Only `./skills/<name>/SKILL.md` is read (a pstack checkout uses its own `skills/`).
- **Done commands are one line each.** Each `$ ` line is one check, run through `bash` in `--cwd` under the run's permission policy, sandbox and hooks.
- **Keyword routing is literal.** There are no synonyms beyond simple suffix stripping; anything else falls to the model fallback, if it is on.
- **A handoff ignores the playbook body.** Steps, done commands, `skills` and `read_only` only apply to playbooks without `workflow:`.
