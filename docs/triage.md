# Issue triage

`tinyagent triage <issues.json>` (or `--triage <issues.json>`) ranks a list of open issues by value against effort and writes a **next-up list** that an outside loop can work from, best first. It is deterministic and offline: fixed rules, no model call, no credentials and no GitHub calls. Every entry carries the reasons for its rank, and nothing is dropped: likely duplicates, stale, blocked items and pull requests stay in the list with flags.

## Usage

```bash
gh issue list --state open --limit 500 \
  --json number,title,body,labels,createdAt,updatedAt > issues.json
tinyagent triage issues.json --out next-up.json        # table on stderr
gh issue list --state open --json number,title,body,labels,createdAt,updatedAt | tinyagent triage -
```

| Flag | Subcommand option | Default | Meaning |
|---|---|---|---|
| `--triage <file>` | the first word after `triage` | | Issues JSON to rank; `-` reads stdin |
| `--triage-out <path>` | `--out` | stdout | Where to write the next-up JSON |
| `--triage-stale-days <n>` | `--stale-days` | `30` | Flag issues not updated for more than `n` days as `stale`; `0` turns the check off |

The next-up JSON goes to stdout (or `--triage-out`), and a Markdown table with rank, issue, score, value, effort, flags and title goes to stderr, followed by `N issues ranked`. Exit status is 0 on success (an empty list included), 2 for a missing or malformed file, a negative `--triage-stale-days` or a conflicting flag (`--triage` runs no task, so it can't be combined with `-t`, `--resume`, `--workflow`, `--eval`, `-i` and similar), and 1 when the output file can't be written. Since the first word after `triage` is always the file, start a task with that word using `-t "triage ..."`.

## Input

A JSON array of issues in the shape `gh issue list --json number,title,body,labels,createdAt,updatedAt` prints. `labels` may be objects with a `name` or plain strings. Every issue needs a positive, unique `number`. Other fields are ignored. TinyAgent never calls GitHub itself; closed or deleted issues simply don't appear in what you pass.

## Rules

- **Value** is the highest of the issue's labels (case-insensitive): `security` 4; `bug`, `regression` 3; `enhancement`, `feature` 2; `documentation`, `docs` 1; `question` 0. An issue with none of these labels gets 1.
- **Effort** is 1 to 5: start at 1; +1 for a body over 2000 characters, or +2 over 6000; +1 for more than 5 list items under an `Acceptance criteria` heading (the largest such section counts); +1 when there is no `Implementation notes` heading, since an unscoped issue costs more.
- **Score** is value / effort, rounded to two decimals.
- **Stale**: not updated (or, with no `updatedAt`, created) for more than `--triage-stale-days` days. Flagged `stale`; the rank is unchanged.
- **Likely duplicate**: two titles share enough content words (Jaccard index of lowercased, stemmed words without stop words, at least 0.6, and both titles have at least two such words). The newer issue (higher number) is flagged `duplicate-of:#N` for the oldest match and ranked after the unique issues.
- **Blocked**: an `amf:blocked` or `blocked` label (see [Blocked tasks](project-context.md#blocked-tasks)). Flagged `blocked` and ranked last.
- **Pull requests**: items with a non-null `pull_request` field (the GitHub REST issues API includes pull requests) or `isPullRequest: true` are flagged `pull-request` and ranked last.

Order: unique issues, then likely duplicates, then blocked items and pull requests; within each group, higher score first; ties by issue number ascending. The same input and day give byte-identical output.

## Next-up JSON

```json
{
  "schema_version": 1,
  "stale_days": 30,
  "items": [
    {
      "rank": 1,
      "number": 11,
      "title": "Crash on empty config file",
      "score": 3,
      "value": 3,
      "effort": 1,
      "reasons": [
        "score 3.00 = value 3 / effort 1",
        "value 3: label bug",
        "effort 1: short body, at most 5 acceptance criteria, has implementation notes"
      ],
      "flags": []
    }
  ]
}
```

| Field | Meaning |
|---|---|
| `schema_version` | `1`; bumped on an incompatible change |
| `stale_days` | The stale threshold used (`0` = off) |
| `items[].rank` | 1-based position |
| `items[].number`, `title` | From the input |
| `items[].score`, `value`, `effort` | See [Rules](#rules) |
| `items[].reasons` | Why the issue ranks where it does; never empty |
| `items[].flags` | Zero or more of `stale`, `duplicate-of:#N`, `blocked`, `pull-request`; never `null` |

`items` has exactly as many entries as the input. An outside loop picks the first item without a `blocked`, `duplicate-of:` or `pull-request` flag, or skips flagged items by its own rules.

## Not included

Model-based value estimates, writing labels or comments back to GitHub, closing duplicates and a workflow triage stage are out of scope for this version.
