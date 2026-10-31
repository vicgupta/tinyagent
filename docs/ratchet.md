# Project rules (ratchet)

When an agent makes a mistake in your project, record a rule so later runs don't repeat it. Each rule keeps the incident that caused it and an expiry date or a review condition, so the rule set stays explainable and can be pruned instead of growing forever.

```sh
tinyagent ratchet add "Never push directly to main" \
  --incident "a run pushed an untested commit to main on 2026-10-29" \
  --deny-command "git push * main"
tinyagent ratchet add "Run go vet before submitting" \
  --incident "CI failed on a vet error the agent didn't check" \
  --review "when CI runs vet as a pre-commit hook"
tinyagent ratchet list            # every rule with status, incident and expiry
tinyagent ratchet list --json     # the same as a JSON array
tinyagent ratchet remove r1       # prune a rule
tinyagent ratchet path            # print the rules file path
```

The flag forms are `--ratchet-add`, `--ratchet-incident`, `--ratchet-expires`, `--ratchet-review`, `--ratchet-deny-tool`, `--ratchet-deny-command`, `--ratchet-deny-path`, `--ratchet-deny-host`, `--ratchet-list`, `--ratchet-path` and `--ratchet-remove`; see the [CLI reference](subcommands.md#flags). These commands make no model request and need no credentials. They accept only `--cwd` (and `--json` with `list`); anything else exits 2.

## The rules file

Rules live in `.tinyagent/ratchet.toml` at the project root (`--cwd`). It's a plain TOML file you can review and edit; commit it so everyone (and CI) gets the same rules. If your `.gitignore` excludes `.tinyagent/`, add `!.tinyagent/ratchet.toml`.

```toml
[[rule]]
id = "r1"
rule = "Never push directly to main"
incident = "a run pushed an untested commit to main on 2026-10-29"
created = "2026-10-30"
expires = "2027-01-28"
[rule.deny]
command = "git push * main"
```

| Key | Required | |
|---|---|---|
| `id` | yes | Unique id; `ratchet add` assigns `r1`, `r2`, ... |
| `rule` | yes | The instruction given to the agent |
| `incident` | yes | What went wrong that led to the rule |
| `created` | yes | `YYYY-MM-DD` |
| `expires` | one of `expires`/`review` | `YYYY-MM-DD`; from this date the rule is skipped |
| `review` | one of `expires`/`review` | When to reconsider the rule, e.g. "when the default model changes" |
| `deny` | no | Enforcement, below |

`ratchet add` without `--expires` or `--review` sets `expires` to 90 days after today, so no rule lives forever by accident. Unknown keys, a missing required key, a bad date or a duplicate id make the file invalid.

## What a run does with the rules

A fresh run loads the file automatically when it exists; without a file nothing changes and nothing is created. `--no-ratchet` skips it for one run.

- **Guidance.** Active rules are added to the system prompt as a "Project rules" section after [project memory](memory.md#project-memory), each with its id and incident.
- **Expired rules** are skipped, and the run prints one warning per expired rule (`project rule r2 expired on 2026-06-01 and was skipped; review it, then remove it (--ratchet-remove r2) or re-add it with a new expiry`) so stale rules get pruned or renewed.
- **Enforcement.** A rule with a `deny` table is also added as a deny rule to the [permission policy](permissions.md#permissions), with the message `blocked by project rule <id>: <rule> (incident: ...)`. Deny wins over every allow, so the model sees the block and the call doesn't run. Set at most one matcher: `command` (shell command glob), `path` (file edits, `file_access` pattern syntax) or `host` (`web_fetch` host). `tool` overrides which tools it applies to (`|`-separated globs); by default that's `bash|start_process` for `command`, `write_file|edit_file|apply_patch` for `path` and `web_fetch` for `host`. A `tool` alone denies those tools outright. Patterns are checked when the rule is added.
- **Events.** With `--json`, a `ratchet.loaded` event reports the loaded ids, expired ids and how many rules are enforced; see [JSON output](json-output.md#machine-readable-output). `--verbose` prints a one-line summary.
- **Invalid file.** A rules file that can't be parsed or validated stops the run with exit 2 before any model request, rather than silently dropping enforcement. Fix the file or pass `--no-ratchet`.

`--resume` and follow-up runs replay the system prompt saved in the checkpoint, so rules added afterwards apply to the next fresh run. [ACP](integrations.md#editor-integration-acp) sessions don't load project rules yet.

## Observer proposals

A `--workflow` run with `--workflow-observe` turns a failed or blocked run into a proposed rule in `.tinyagent/observer/proposals.jsonl`, with a ready-to-run `tinyagent ratchet add ... --incident ...` command; see [Learning from failures](project-context.md#learning-from-failures). Proposals are never added to `ratchet.toml` automatically: review the rule, edit it if needed (add `--deny-command` or `--review` for example), then run the command. The incident ends with an `observer <signature prefix>` tag; keep it, so when the same failure happens again the observer can report that this rule didn't prevent it (`gate did not hold`).
