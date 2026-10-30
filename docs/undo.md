# Undoing changes

## undo_file

The first time `write_file`, `edit_file` or `apply_patch` changes a file in a session, its contents (or the fact that it did not exist) are saved under `.tinyagent/undo/<session>/`. The `undo_file {path}` tool restores that copy, deleting the file if the session created it. It goes through the same path, symlink, additional-directory, `file_access` and `--read-only` checks as other writes. There is one snapshot per file per session (the state before the session's first change), and changes made by `bash` are not covered; use `--rewind` (below) or git for those. The snapshot folder has its own `*` `.gitignore`, and `/.tinyagent/` is in the repository's `.gitignore`.

### Rewind workspace changes

With `--snapshots` (or `snapshots: true` in the config), tinyagent records the workspace so you can put files back the way they were before the agent changed them. It is off by default and needs `-o`, because the trajectory file is the index of the snapshots.

```sh
tinyagent -t "refactor the parser" --cwd . --snapshots -o run.json
tinyagent --rewind run.json --cwd . --dry-run          # preview: M/A/D lines and a count
tinyagent --rewind run.json --cwd .                     # back to the start of the run
tinyagent --rewind run.json --cwd . --to 4              # the state before step 4's tool batch
```

**When snapshots are taken.** A `baseline` after setup and before the first model request; a `step` snapshot before each tool batch that can change files (`write_file`, `edit_file`, `apply_patch`, `bash`, `start_process`, `stop_process`, and any MCP tool; batches of only read-only tools take none); and a `final` one when the run ends, whatever its status. Each is listed in the trajectory's `snapshots` array (`snapshots_enabled` is `true`), emitted as a `workspace.snapshot` event in `--json` and `--log-db`, and printed as one line with `-v`. With `--read-only` nothing can change files, so no snapshots are taken and stderr says so once.

**What is in scope.** Regular files and symlinks (stored as links, not followed) under `--cwd`, with their mode bits. Left out, and never modified or deleted by a rewind:

- `.git/`, the `.tinyagent/` directory (the store and the event log), and the `-o` trajectory and its temp files;
- paths ignored by `.gitignore` files and `.git/info/exclude`, when `--cwd` is inside a Git repository (your global excludes file is not read);
- files larger than `snapshots.max_file_bytes` (default 2 MiB). The snapshot records count them and list up to 20; a rewind warns that they were "not restored".

**Rewind.** `--rewind <trajectory> --cwd <root>` needs `--cwd` given explicitly, resolving to the trajectory's root as for `--resume`. For a [`--worktree`](runs.md#worktrees) run, pass the worktree path. `--to` is `start` (the default: the baseline), a step number, or a snapshot id. A step number means the state before that step's tool batch; if that step has no snapshot (it only read files), the latest snapshot at or before it is used and the output says which. The plan prints one line per path, `M` (content or mode restored), `A` (created later, will be removed) and `D` (deleted later, will be recreated), then a count; `--dry-run` stops there. Otherwise tinyagent first takes a `pre-rewind` snapshot, records it in the trajectory file, and prints its id with the command that undoes the rewind (`--rewind run.json --cwd . --to <pre-rewind-id>`); then it restores each path and removes directories that only exist because of later changes. It makes no model request and needs no API key.

A rewind exits 2 before writing anything when `--cwd` is missing or doesn't match, `--to` names no snapshot, the trajectory has no snapshots, or the store is missing or corrupt. A rewind isn't atomic across files: if a write fails it stops, lists what was and wasn't restored and the `pre-rewind` id, and exits 1.

A rewind doesn't touch Git state (HEAD, the index, branches, stashes), so uncommitted changes you made before the run come back as they were. It restores files only: not processes, services, databases outside `--cwd`, or anything a command did elsewhere. With `--env container`, `--cwd` is mounted into the container, so files the container writes there are covered.

**Resume.** `--resume` keeps the source trajectory's snapshots in the new trajectory, so either file can be rewound, including to a step of the original run. Whether the resumed run takes new snapshots depends on this invocation's `--snapshots`/config.

**Storage.** Snapshots live in `<cwd>/.tinyagent/snapshots/`: file contents are stored once each by SHA-256, plus one small manifest per snapshot. Directories are created `0700` and files `0600`, and the store has its own `.gitignore` so it is never committed. It holds full copies of in-scope files, secrets included, so treat it like trajectory files. Nothing is pruned automatically; deleting `.tinyagent/snapshots` is safe and only makes old trajectories un-rewindable. Each snapshot walks the tree, which can take a moment on large trees; `duration_ms` in the event shows how long.
