# Project memory

### Project memory

Project memory (experimental; see [#170](https://github.com/vicgupta/tinyagent/issues/170)) keeps short notes that runs save about a project so later runs don't relearn them. By default the store is a `memory/` folder in `--cwd` with one Markdown file per entry (`memory/<id>.md`); set `backend = "sqlite"` to keep them in one SQLite database (default `memory/memory.sqlite`) instead. Entries record the run, model and whether untrusted content was in context, can carry tags, and may be held as *pending* until you approve them; pending entries are never loaded into a prompt. The model saves entries with the `remember` tool when memory is on, and can look them up with the read-only `memory_search` (keywords and/or a tag) and `memory_list` (most recent) tools, which return only active entries. Add `memory/` to `.gitignore` unless you want to share notes through the repository.

An entry file looks like this (YAML front matter, the note as the body):

```markdown
---
id: m7529e1f6
status: active
created_at: "2026-10-29T14:03:11.52Z"
updated_at: "2026-10-29T14:03:11.52Z"
source_session: run-7c1e
model: anthropic/claude-sonnet-4-5
version: 0.9.0
untrusted_context: false
tags: [build, ci]
---
Run `make test-fast` before pushing; the full suite needs Docker.
```

The SQLite backend stores the same fields as real columns in a `memories` table (`id`, `content`, `status`, `created_at`, `updated_at`, `source_session`, `model`, `version`, `untrusted_context`, `tags`, `replaces`, `seq`) and records its schema version in `PRAGMA user_version`; a database from a newer tinyagent is reported as corrupt rather than changed. Both backends write atomically under a lock, so concurrent runs keep every entry.

**Settings (memory.toml).** Memory settings come from, highest first: `TINYAGENT_MEMORY_<KEY>` environment variables, a `memory.toml`, the `memory:` section of the YAML config, and the defaults. The `memory.toml` is the first that exists of `$TINYAGENT_MEMORY_CONFIG`, `./memory.toml` in `--cwd`, and `tinyagent/memory.toml` in the user config directory (`~/.config` on Linux). `--memory`/`--memory=false` still overrides `auto_write` for one run. `tinyagent --memory-help` prints every key with its type, default and variable:

```toml
backend = "sqlite"                 # markdown (default) or sqlite
sqlite_path = "./memory/memory.sqlite"
auto_write = true                  # same as --memory / memory.enabled
require_approval = true            # false = memory.approval: auto
max_entry_size = 500               # characters per entry
max_entries_per_project = 50       # memory.max_entries
default_tags = ["backend"]
import_legacy = true
```

The keys are `backend`, `path`, `sqlite_path`, `auto_write`, `require_approval`, `max_entry_size`, `max_entries_per_project`, `default_tags` and `import_legacy` (environment: `TINYAGENT_MEMORY_BACKEND`, `..._PATH`, `..._SQLITE_PATH`, `..._AUTO_WRITE`, `..._REQUIRE_APPROVAL`, `..._MAX_ENTRY_SIZE`, `..._MAX_ENTRIES_PER_PROJECT`, `..._DEFAULT_TAGS` as a comma-separated list, `..._IMPORT_LEGACY`). The YAML `memory:` section keeps its old keys and also accepts `backend`, `path`, `sqlite_path`, `max_entry_size`, `default_tags` and `import_legacy`. Relative paths resolve against `--cwd`. Unknown keys and invalid values exit 2.

**Repository-local memory.toml and stores.** A `./memory.toml` in `--cwd` comes from the repository, so it may only make memory safer: `auto_write = true` and `require_approval = false` are ignored with a warning, `max_entry_size` and `max_entries_per_project` can only be lowered, and `path`/`sqlite_path` must be relative and stay inside `--cwd`. Turn memory on with `--memory`, the user `memory.toml`, the user YAML config or the environment. A store committed to the repository is still read when *you* turn memory on, so its active entries reach the prompt like AGENTS.md does; review `memory/*.md` in repositories you don't trust (or point `path` elsewhere in your config) before using `--memory` there.

**Legacy store.** Earlier builds kept one JSON file per project outside the repository (`$XDG_STATE_HOME/tinyagent/memory/<project>.json`, else `~/.local/state/tinyagent/memory/`). With `import_legacy = true` (the default), the first run with memory on, or the first `--memory-list`/`--memory-approve`/`--memory-forget`, copies those entries (statuses, tags and provenance included) into the new store when the new store doesn't exist yet, and says so on stderr. The JSON file is never changed or deleted, and once the new store exists nothing is imported again, even after `--memory-forget all`. `--check` reports a pending import; `--memory-path` never imports.

**Loading.** Memory is off by default. With `--memory` (or `memory.enabled: true` in `-c` or a trusted `./tinyagent.yaml` (`--trust-config`), or `auto_write = true` in the user `memory.toml` or environment; never from the repository), a run loads the project's *active* entries into a "Project memory" system prompt section right after AGENTS.md, newest first, one line each as `- [id, date] text`. The section says the notes come from earlier runs and that AGENTS.md and the task outrank them. Only the newest entries that fit within `memory.max_bytes` (default 8 KiB) are loaded; when entries are left out, the section says how many and tinyagent warns once on stderr. `-v` prints how many entries were loaded and from which store. A missing store loads nothing and creates nothing, so memory on with no entries sends exactly the same prompt as memory off. A corrupt or unreadable store warns once and the run continues without memory. Invalid `memory` settings exit 2.

**Resume.** The trajectory records whether memory was on and the exact entries loaded (`memory_enabled`, `memory`, `memory_omitted`). `--resume` rebuilds the section from that snapshot and never re-reads the store, so approving, forgetting or adding entries doesn't block a resume; passing a `--memory` value that conflicts with the checkpoint exits 2. Checkpoints from before this feature resume with memory off. Delegated subagents and workers don't load project memory.

**ACP sessions.** With memory on, an `--acp` session loads the store for the session's cwd on its first prompt, the same way a fresh run does, and later prompts in the session replay that snapshot like follow-ups, so the session never sees its own writes. The `remember` tool is offered on every prompt unless the session is read-only. One writer serves the whole session, so `max_writes_per_run` counts writes across all of the session's prompts. A session that started client-supplied MCP servers (`acp.client_mcp_servers`) saves every entry as pending, as does any session once a `web_fetch` or MCP result enters its context.

**Trust:** loaded entries and `memory_search`/`memory_list` results are prompt input that can steer the agent, and they go to your model provider like any other prompt. Only approved (active) entries are loaded or returned. `--check` prints the backend, the approval policy, where the settings came from and the store path.

There is no `tinyagent memory ...` subcommand: tinyagent takes only flags, and a positional argument is rejected ("pass the task with -t/--task"), so memory management uses `--memory-*` flags.

You can already inspect and edit the store for `--cwd` without credentials or a model request:

```sh
tinyagent --memory-path                     # print the store path (folder or .sqlite file)
tinyagent --memory-help                     # print every memory.toml key, its default and env variable
tinyagent --memory-list [--json]            # id, status, date, source and text
tinyagent --memory-approve <id|all>         # approve one or every pending entry
tinyagent --memory-forget <id>              # delete one entry (and any pending replacement of it)
tinyagent --memory-forget all --memory-yes  # delete every entry
```

These commands accept only `--cwd`, `-c`/`--config` and `--profile` (and `--json` with `--memory-list`) and use the same memory settings as a run; combining them with `-t`, `--resume` or any other run or mode flag exits 2, as does an unknown id. A corrupt store is reported, exits 1 and is left unchanged.

### Safety notes

Saved entries are shown to the model as data from earlier runs, not as instructions: the prompt section and the `memory_search`/`memory_list` output both say so. The markdown backend refuses a memory folder that is a symlink and treats symlinked entry files as corrupt. Memory stays off by default and new entries stay pending until approved.
