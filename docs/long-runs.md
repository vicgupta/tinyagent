# Long runs and advanced tools

## Long runs

tinyagent resends the whole history on every step, so a long run can outgrow the model's context window. Before each request it estimates the request's size (the previous request's reported input tokens plus about 4 characters per token for anything added since). Above 80% of the window, it compacts:

1. Old tool outputs are replaced with a placeholder such as `[output elided: 12,400 chars]`. The system prompt, the task, and the most recent turns (about 25% of the window, and never fewer than the last 2 assistant turns with their tool results) are kept verbatim. If that brings the estimate under 50% of the window, it stops here.
2. Otherwise, one extra request asks the same model, with no tools, to summarize everything older than the recent turns. The model then sees the system prompt, the task, that summary, and the recent turns.
3. If it still doesn't fit, the run ends as `Error` with `context still exceeds the window after compaction`.

If a provider rejects a request as too long (`prompt is too long`, `context_length_exceeded`, `maximum context length`), tinyagent compacts and retries that step once; a second rejection ends the run as `Error`. This works even for models whose window isn't known.

The window comes from a small built-in table (`internal/model/context.go`), a named provider's per-model `context_window` (see [Named providers](providers.md#named-providers)), or top-level `context_window` in the config, which wins. Without either, only the retry-on-error path runs. Each compaction prints one line to stderr, such as `compacted context: ~182,000 → ~31,000 tokens (summarized 41 messages)`. The summary request's tokens and cost count toward the run's totals and `--cost-limit`, but not `--step-limit`. With `-o`, the trajectory keeps every original message, and `compactions` records each one: the step, the estimates before and after, the stage, the summary, and which messages the model was sent next.

Summaries lose detail. Put instructions that must hold for the whole run in `AGENTS.md`, which is part of the system prompt and never compacted. Scoped `AGENTS.md` files from [`--scoped-agents-md`](project-context.md#scoped-agentsmd-in-subdirectories) live in tool results instead, so they can be compacted away; they are sent again the next time the agent touches their directory. `--no-compact` turns all of this off.

### Images

`--images` (or `images: {enabled: true}`) lets vision-capable models see images. It is off by default; with it off, requests, tools, and checkpoints are unchanged.

- **`view_image`** takes a `path` inside `--cwd` and sends the file to the model. PNG, JPEG, GIF, and WebP are supported, detected from the file's bytes (not its extension); SVG, other formats, directories, paths outside the workspace, and files larger than `max_image_bytes` (default 5 MiB) are errors. It is available with `--read-only`, since it only reads.
- **MCP image pass-through**: image content in an allowed MCP tool's result is sent to the model (up to 8 images per result, each within `max_image_bytes`). Unsupported, oversize, or extra images are replaced by an `[omitted MCP image: ...]` marker. With images off, MCP images are still `[omitted unsupported MCP content]`.

Per provider: `anthropic` sends images as image blocks inside the tool result, and `codex` as `input_image` items in the function call output. The Chat Completions API (`openai`, `openrouter`, `ollama`) cannot carry images in tool results, so those providers get a text note saying the image was not shown; tinyagent does not inject images as a separate user message. (A Responses API transport for `openai`, #80, would allow it.)

Image data never appears in text channels: the model's tool-result text, `--json` events, verbose output, hook input, and the event log see a placeholder like `[image: image/png, 48,213 bytes, sha256:1a2b3c4d5e6f]`. Privacy: the image bytes are sent to the model provider and stored base64 in the trajectory/checkpoint file, so treat those files like the images themselves. For compaction each image counts as about 1,600 tokens, and old images are elided (`[image elided: <path>, N bytes]`) like old tool output; the summarizer never sees image data.

A checkpoint records whether images were enabled, and `--resume` keeps that setting: passing `--images` to resume a run that started without it (or the reverse) is an error, and the config's `images.enabled` is ignored on resume.

#### Attaching images to the task

`--image <path>` attaches a screenshot, design mock, or other image to the task message, so the model sees it with the task text and needs no tool call:

```sh
tinyagent -t "fix the layout regression shown" --image artifacts/diff.png
tinyagent -t "make current.png match reference.png" --image current.png --image reference.png
```

- **Rules.** The flag is repeatable (no comma splitting), keeps its order, and allows up to 10 images. Each file must be PNG, JPEG, GIF, or WebP, detected from its bytes (a PNG named `.jpg` is sent as `image/png`), and at most `max_image_bytes` (default 5 MiB). A missing file, a directory, another format, an oversize file, more than 10 images, or `--image` without `-t` exits 2 before any model request.
- **Paths.** Paths resolve against the directory tinyagent was started in, not `--cwd`. They are your own input, not model file access: the workspace restriction and `file_access` rules do not apply. Only the base name reaches the model.
- **Independent of `--images`.** Attachments don't need `--images`, and they don't turn on `view_image`.
- **Message shape.** The task text gets one label line per image, such as `[attached image 1: mock.png, image/png, 48213 bytes]`, so the model can refer to "image 1". The images come before the text: `anthropic` sends `image` blocks with base64 data; `openai` on the Responses API and `codex` send `input_image` parts with `data:` URLs; Chat Completions (`openai` with `openai_api: chat`, `openrouter`, `ollama`) sends a content array of `image_url` parts with `data:` URLs plus a `text` part. Messages without attachments are sent exactly as before. The model must support vision (for Ollama, use a vision model such as `llava`); otherwise the provider's error ends the run like any other request error, and this applies to fallback models as well.
- **Text channels.** `run.started` (in `--json` and `--log-db`) lists each attachment's index, base name, MIME type, size, and sha256; `-v` prints one `attached image N:` line per file. No text channel carries image data.
- **Resume and follow-up.** The images are stored base64 in the checkpoint with the task message, so `--resume` sends them again without the original files. `--resume <checkpoint> -t "still wrong, see this" --image after.png` attaches images to the follow-up message. `--image` is refused (exit 2) when resuming an unfinished run or a finished one without `-t`. Older tinyagent versions refuse checkpoints that contain attachments.
- **Compaction and cost.** The task message is never compacted, so its images stay attached and are re-sent with every request. For the context estimate each image counts as about 1,600 tokens, and the summarizer sees only the label lines. Images typically cost 1–5k input tokens each per request, so crop or downscale large screenshots. tinyagent doesn't resize them. Base64 adds about a third to the size, so a 5 MiB file can exceed a provider's per-image limit (for example, 5 MB on Bedrock); lower `max_image_bytes` if that matters.
- **Privacy.** Attached images are sent to the model provider and stored in the trajectory/checkpoint file.

### Tool output files

Tool results longer than `max_output_chars` are cut to a head and tail with a `... [N characters elided] ...` marker, and the middle is lost. `--tool-output-files` (or `tool_output_files.enabled: true`) keeps it: the full result text is saved to a file and the model-visible result becomes a shorter head/tail preview ending in one notice, for example:

```
[full output: 184,213 bytes, 2,941 lines, saved to .tinyagent/tool-output/20261025T140307.123Z-a1b2c3/call_abc.txt; use read_file with offset/limit or grep on that path]
```

The preview plus the notice never exceeds `max_output_chars`, and it is cut on UTF-8 boundaries. The model can then `read_file` the path with `offset`/`limit` or `grep` it. It is off by default; with it off, results, events, and checkpoints are unchanged and no directory is created.

- **What is saved:** the exact full text the tool returned (for `bash`, including the `<returncode>`/`<output>` wrapper, so the command's line N is line N+2 of the file), for built-in tools, allowed MCP tools (whose results are already capped at 256 KiB), and error results. Results within the limit are never saved. `read_file` results are not copied, since the file is already on disk: a truncated `read_file` result ends with `[file has N lines; output was truncated, use read_file on <path> with offset/limit to read the rest]` instead. `grep` and `list_files` calls on the output directory itself are not saved again.
- **Where:** one directory per run, `<dir>/<run id>/<tool call id>.txt`, where `dir` is `tool_output_files.dir` (default `.tinyagent/tool-output`, relative to `--cwd`) and the run id is a UTC timestamp plus a random suffix (`20261025T140307.123Z-a1b2c3`). `dir` must be inside `--cwd` and not `--cwd` itself; otherwise startup fails before any model request with an error naming `tool_output_files.dir`. The run directory is created (mode 0700) on the first save, and each file is written atomically with mode 0600. Files are written in `--read-only` mode too, since they are tinyagent's own output, not a workspace edit.
- **Exclusions:** recursive `grep` and `list_files` walks skip the output directory, so saved copies never show up as duplicate matches; a `grep` or `list_files` whose `path` is inside the directory searches it normally. `bash` commands are not filtered.
- **Retention:** at run start, the oldest run directories are deleted so at most `tool_output_files.max_runs` (default 20; 0 keeps all) earlier runs remain. Only directories named like run ids are touched.
- **Errors:** if a file can't be written (for example, `.tinyagent` is not a writable directory), the result falls back to the normal truncation, stderr gets one warning for the run (also a `warning` event with `--json`), and the run continues.
- **Recording:** with `--json`, a saved result's `tool.result` event adds `output_file` and `output_bytes`; `post_tool` hooks get `output_file`; with `-o`, the trajectory lists `tool_output_files` entries (`tool_call_id`, `output_file`, `output_bytes`). The event log stores the path and size as metadata. The setting is not pinned in checkpoints: `--resume` can turn it on or off.

Privacy: saved files can contain anything a command printed, including secrets, just like trajectories and the event log. Add `.tinyagent/` to your `.gitignore`.

### apply_patch

`--apply-patch` (or `apply_patch: true`) adds an `apply_patch` tool that applies one patch in the `*** Begin Patch` ("V4A") format [documented by OpenAI](https://developers.openai.com/api/docs/guides/tools-apply-patch). It is aimed at models trained on that format (the OpenAI GPT-4.1 and GPT-5 families, including `codex/*`) and works with any model that follows the tool description. It is off by default; with it off, requests, tools, and checkpoints are unchanged. `edit_file` and `write_file` stay available, and the tool is never offered with `--read-only` (a call to it gets the read-only denial).

The tool takes one string argument, `input`:

```
*** Begin Patch
*** Add File: docs/notes.txt
+first line
*** Update File: src/app.go
*** Move to: src/main.go
@@ func main() {
 	fmt.Println("hi")
-	return
+	os.Exit(0)
 }
*** Delete File: old.txt
*** End Patch
```

- `*** Add File:` is followed by the file's lines, each prefixed with `+`. `*** Delete File:` takes no body. `*** Update File:` is optionally followed by `*** Move to:` (a rename), then hunks of context (` `), removed (`-`), and added (`+`) lines.
- A hunk starts with `@@` or `@@ <line>`: the search for the hunk moves past the first line matching `<line>` (after the previous hunk), and several `@@` lines may be stacked to narrow it down. `*** End of File` marks a hunk at the end of the file. There are no line numbers.
- Context is matched exactly, then ignoring trailing whitespace, then ignoring surrounding whitespace; a looser match is noted in the result. Context lines keep the file's own text.
- Paths follow the same rules as the other file tools: relative to `--cwd`, or absolute inside it; anything outside is rejected. A shell heredoc wrapper (`apply_patch <<"EOF" ... EOF`) is stripped.

The whole patch is parsed and checked before anything is written: update and delete targets must exist, be regular files, and not be binary; add targets and move destinations must not exist; a path may appear in only one operation; every hunk must match; and the patch must be at most 1 MiB. If any check fails, nothing is written and the error names the operation (by number and path), the reason, and for context failures the lines that were not found. Otherwise the operations are applied in order and the result is `ok` followed by one line per operation: `A <path>`, `M <path> (<n> hunks)`, `D <path>`, or `R <old> -> <new>`. Updated files keep their permission bits, their line endings (CRLF or LF), and a missing trailing newline (a hunk marked `*** End of File` leaves the file ending in a newline); new files are written 0644 with parent directories created.

Writes are not transactional: if writing fails part-way (for example, a permission error), work stops and the error lists the operations that were applied, which are not rolled back, and those that were not. Like `edit_file`, the tool is not covered by `--sandbox`. A missing or non-string `input` is a [malformed tool call](troubleshooting.md#malformed-tool-calls); a patch that does not apply is an ordinary tool error.

A checkpoint records whether `apply_patch` was enabled (`apply_patch_enabled`), and `--resume` keeps that setting: passing a conflicting `--apply-patch` exits 2, and the config's `apply_patch` is ignored on resume.

### Subagents

`--subagents` (or `subagents.enabled: true`) adds a `delegate` tool. A call such as `{"task": "find every caller of LoadConfig and say which ones ignore its error", "label": "callers"}` starts a child agent with a fresh context: the same system prompt and `AGENTS.md`, plus a notice that it is a subagent that must answer first and then give evidence with `file:line` references. The child runs to completion, and the parent gets back only its final report followed by a footer such as `[delegate d1: Submitted, 4 steps, 9200 in / 410 out tokens, $0.0123]`. It is off by default; with it off, requests, tools, and checkpoints are unchanged.

- **Read-only.** The child has only `read_file`, `list_files`, and `grep`, whatever the parent has. It gets no `write_file`, `edit_file`, `apply_patch`, `bash`, process tools, MCP tools, `skill`, `view_image`, `update_plan`, or `delegate`, and calls to them are refused. Paths follow the parent's rules (`--cwd` and `file_access`). The tool is also offered with `--read-only`.
- **Limits.** Each child stops after `subagents.max_steps` model steps (default 30); hitting the cap, or any other unfinished status, returns a tool error such as `error: delegate d1 did not finish: StepLimit` rather than a report. Child steps don't count toward `--step-limit`; they're reported separately as `subagent_steps`. Child tokens and cost do count toward the run's totals and `--cost-limit`, and the child only gets what's left of the budget, so a delegated task can end the whole run as `CostLimit`. `--time-limit` covers children too. Reports longer than `max_output_chars` are truncated like any tool output. Several `delegate` calls in one turn run one after another.
- **Model.** `subagents.model` picks a different (typically cheaper) model; empty uses the parent's. `reasoning_effort` and `max_output_tokens` carry over only when the child uses the same provider as the parent. An unknown provider, or a missing key for a different provider, exits 2 before any request.
- **Tokens.** Delegation saves the parent's context, not tokens overall: the child resends its own history each step, so a task the parent could answer with one `grep` is cheaper done directly. Use it for searches that would otherwise pull many large files into the parent's history.
- **Observability.** `--json` and `--log-db` add `subagent.started` (`id`, `label`, `model`, `tool_call_id`, `parent_step`) and `subagent.finished` (`id`, `status`, `steps`, tokens, `cost_usd`) events, and the child's own events carry `"subagent": "d1"`. `-v` prints a line when each child starts and finishes; the child's model output is not streamed. Hooks run for child tool calls with `subagent` set in their input, and `pre_tool`/`post_tool` hooks also see the `delegate` call itself; stop hooks run only for the parent. With `-o`, the trajectory records `subagents_enabled`, `subagent_steps`, and a `subagents` array with each child's task, status, usage, report, and full message history.
- **Resume.** A checkpoint records whether subagents were enabled, and `--resume` keeps that setting: a conflicting `--subagents` exits 2. A run interrupted mid-delegation resumes with that `delegate` call marked interrupted; the child is not resumed.

Not in this version: children that can write or run commands, parallel children, nested delegation, and streaming the child's output.

### Workers

`--workers` (or `subagents.workers.enabled: true`) adds two tools. `worker` hands a well-scoped implementation subtask to a child agent that may edit files and run commands, but only in its own Git worktree. `apply_worker` then applies the worker's change to your working tree. Nothing is merged, pushed or applied automatically.

```yaml
subagents:
  workers:
    enabled: true
    model: ""            # default: the parent's model
    max_steps: 60        # per worker
    max_concurrent: 1    # 1 to 4 workers from one response at once
    tools: [read_file, list_files, grep, write_file, edit_file, apply_patch, bash]
```

- **How a worker runs.** tinyagent snapshots the working tree (tracked changes plus untracked, non-ignored files) into a commit using a temporary index, so your index, `HEAD` and refs are untouched. It creates a worktree at `.tinyagent/worktrees/<run>-w<n>` on branch `tinyagent/<run>-w<n>` with repository hooks disabled. The child runs there with a fresh context; its file tools, commands and sandbox writable root are the worktree. When it finishes, whether it submitted or stopped early, tinyagent (not the child) commits its changes to the branch. With no changes there is no commit, and the worktree and branch are removed.
- **Result.** The worker's report, a footer such as `[worker w1: Submitted · branch tinyagent/run-…-w1 · commit abc1234 · 3 files changed, 40 insertions(+), 5 deletions(-) · 18 steps · $0.0400]` and the `git diff --stat`.
- **Applying.** `apply_worker` with `{"worker": "w1"}` applies the worker's base-to-commit diff to the working tree (not the index), atomically: on a conflict nothing changes and it lists the conflicting files and the branch. It is refused when the diff touches a [`file_access`](permissions.md#restricting-file-access) `deny_write` path or a file outside `--cwd`. It counts as a file-changing tool for `--snapshots` and runs through `pre_tool`/`post_tool` hooks.
- **Tools.** A worker gets the configured `tools` that the parent itself has, never `delegate`, the worker tools, `ask_user`, `update_plan`, MCP tools or `web_fetch`. Hooks run for its calls with `subagent` set to `w1`, `w2`, … and `worker: true`.
- **Budgets.** Worker usage counts toward the parent's `--cost-limit`; a worker that hits a limit stops, its partial work is still committed, and the run ends with the limit status.
- **Concurrency.** With `max_concurrent` above 1, two or more `worker` calls in one model response run at the same time, at most `max_concurrent` at once. Permission checks, `pre_tool` hooks and worktree creation run first, one call at a time in call order, so every worker starts from the same snapshot; commits, `post_tool` hooks and results then follow in call order. The workers share the remaining `--cost-limit`: once their combined spend reaches it, or the run is interrupted or hits `--wall-time`, every running worker is cancelled, each keeps its partial work committed, and the run ends with that status. `--json` adds a `workers.batch` event (`workers`, `max_concurrent`, `stop`). Calls from different responses, and `apply_worker`, still run one at a time.
- **Requirements.** `--workers` needs a Git repository with at least one commit, and can't be combined with `--read-only` or the `--review` modes. Commits use the repository's identity, or `tinyagent <tinyagent@localhost>` when none is configured.
- **Observability.** `--json` adds `kind: "worker"`, `branch`, `commit` and `files_changed` to `subagent.started`/`subagent.finished`, and a `worker.applied` event. The trajectory records each worker's history, branch, base commit and commit.
- **Cleanup.** Worker branches and worktrees are kept, applied or not; tinyagent never removes them on its own. After the summary line, the run lists every kept worker branch, unapplied ones first, with ready-to-paste commands: `review:` (`git show --stat <commit>`) and `discard:` for unapplied workers, `clean up:` for applied ones (`git worktree remove --force <worktree> && git branch -D tinyagent/<name>`). With `--json`, the `result` event carries the same list as `worker_branches` (`id`, `branch`, `commit`, `worktree`, `applied`, `status`, `cleanup`).
- **Resume.** A checkpoint records whether workers were enabled; a conflicting `--workers` on `--resume` exits 2. `--rewind` affects only your tree, never worker branches.

**ACP.** Workers are also available in editor sessions with `--acp --workers` or the same config. Each session must start in a Git repository with a commit; read-only sessions reject workers. Later prompts can review and apply workers from earlier turns. Worker tool results appear in the editor, and kept branches and cleanup commands appear in the per-turn summary in the agent log.

### Web fetch

`--web-fetch` (or `web_fetch.enabled: true`) adds a `web_fetch` tool that reads one URL and returns it as text, for documentation, changelogs and API references. It is off by default; with it off, requests, tools, and checkpoints are unchanged. It is separate from command networking: it runs inside tinyagent, not through `bash`, so enabling it gives commands no network access, and `--sandbox`, `--sandbox-network` and `sandbox_allowed_domains` neither restrict nor widen it.

```yaml
web_fetch:
  enabled: true
  allowed_domains: [pkg.go.dev, "*.python.org"]
  max_bytes: 2097152      # 2 MiB
  timeout_seconds: 20     # 1 to 120
```

- **Allowlist.** `allowed_domains` (or `--web-fetch-allow-domain <host>`, repeatable, which replaces the config list) uses the same syntax as [`sandbox_allowed_domains`](sandbox.md#network-allowlist): `example.com`, `*.example.com` (subdomains only), and an optional `:port` (without one, ports 80 and 443). A bare `*`, an empty list while enabled, `timeout_seconds` outside 1 to 120, or `max_bytes` below 1 exits 2 before any model request, as does `--web-fetch-allow-domain` without enabling the tool. On start, one line such as `web_fetch: 2 domains (pkg.go.dev, *.python.org)` goes to stderr.
- **What it fetches.** `http` and `https` URLs only, with no user name or password in them, by `GET` with `User-Agent: tinyagent/<version>` and no cookies or credentials. The host is checked before any connection; loopback, private, link-local (including the `169.254.169.254` metadata address) and other non-public addresses are refused unless that literal address is listed, and the connection goes to the address that was checked. Up to 5 redirects are followed, each checked the same way. `HTTP_PROXY` and similar variables are ignored.
- **What it returns.** A header line such as `[web_fetch https://pkg.go.dev/fmt → 200 text/html, 81,234 bytes, 412 lines]` (noting truncation and a redirected final URL), then the body: HTML is converted to readable text (headings, lists, code blocks and `[text](url)` links kept; scripts, styles and navigation dropped), and plain text, Markdown, JSON and XML are returned as is. Other content types, non-2xx statuses, timeouts and refused URLs are tool errors. At most `max_bytes` of the response is read, and the result is then subject to `max_output_chars` and [tool output files](#tool-output-files) like any other tool output. Page content is untrusted input to the model.
- **Where it is offered.** It only reads, so it is offered with `--read-only`. [Subagents](#subagents) never get it. `pre_tool`/`post_tool` [hooks](permissions.md#tool-hooks) match it as `web_fetch` and see `arguments` as `{"url": "..."}`; a denied call makes no request.
- **Observability.** `-v` prints one line per fetch (URL, status, type, bytes, time, or the refusal reason). `--json` and `--log-db` emit a `web_fetch` event per attempt and list `allowed_domains` in `run.started`; `-o` trajectories record `web_fetch_enabled` and `web_fetch.allowed_domains`.
- **Resume.** A checkpoint records whether `web_fetch` was enabled, and `--resume` keeps that setting: passing a conflicting `--web-fetch` exits 2, and the config's `web_fetch.enabled` is ignored on resume. The allowlist and limits come from the current invocation.

### Language servers (LSP)

`--lsp` (or `lsp.enabled: true`) starts language servers you configure, so the model sees compiler errors right after it edits a file instead of on its next build, and can jump to definitions and references. It is off by default; with it off, requests, tools, and checkpoints are unchanged and no server starts.

```yaml
lsp:
  enabled: true
  diagnostics_after_edit: true  # default
  diagnostics_wait_ms: 3000     # 100 to 30000
  max_diagnostics: 20           # per file, 1 to 200
  severity: error               # or warning
  servers:
    - name: go                  # letters, digits, - and _
      command: gopls
      extensions: [.go]
      env: [HOME, GOPATH, GOFLAGS]   # host variables to forward; each must be set
    - name: ts
      command: typescript-language-server
      args: [--stdio]
      extensions: [.ts, .tsx]
      initialization_options: {}
      startup_timeout_seconds: 15    # start and initialize, 1 to 300
```

- **Trust.** Servers are executables you choose, like [MCP servers](config.md#local-mcp-tools): they are read only from the user config or `-c`, never from a repository file, and launched directly (no shell) in `--cwd` with a minimal environment: `PATH` (on Windows also `SystemRoot`, `WINDIR`, `USERPROFILE`, `TEMP`, `TMP`) plus the variables named in `env`. Many servers need more (gopls wants `HOME` or `GOPATH`/`GOCACHE`); forward them by name. A named variable that is unset, an empty `servers` list while enabled, a duplicate name or extension, or a value out of range exits 2 before any model request. Servers run on the host, so `--lsp` with `--sandbox` or `--env container` exits 2.
- **Lifecycle.** A server starts the first time a file with one of its extensions is edited or queried, one process per server per run. A server that can't start (missing binary, failed `initialize`, timeout) is marked unavailable for the run with one stderr warning; the run continues. A server that crashes is restarted once, then marked unavailable. On every exit (submit, limits, errors, interrupt) tinyagent sends `shutdown`/`exit` and then kills the server's process group. Each server's stderr goes to a log under `.tinyagent/lsp` in `--cwd` (1 MiB per server, the newest 50 kept).
- **Diagnostics after edits.** After a successful `write_file`, `edit_file` or `apply_patch` (with `diagnostics_after_edit`), tinyagent syncs each changed file with its server and waits up to `diagnostics_wait_ms` for diagnostics, then appends them to the tool result:

  ```text
  ok
  [lsp go: 2 errors in internal/a.go]
  internal/a.go:12:5: undefined: fooBar
  internal/a.go:30:2: missing return
  ```

  Positions are 1-based lines and columns (columns in characters). Errors only by default; `severity: warning` adds warnings (prefixed `warning:`). Beyond `max_diagnostics` a line such as `[lsp go: omitted 7 more diagnostics in a.go]` follows, and if the server is slow, `[lsp go: diagnostics not ready after 3000 ms for a.go]`. A clean file adds nothing. Notes are appended before `post_tool` [hooks](permissions.md#tool-hooks) run, so hooks, `tool.result` events, [tool output files](#tool-output-files) and trajectories all see them. They report every current error in the file, not only new ones, and a server that hasn't finished indexing may report false positives; treat them as hints and confirm with a build.
- **The `lsp` tool.** `{"operation": "diagnostics" | "definition" | "references", "path": "...", "line": n, "column": n}` (1-based; `line` and `column` only for `definition` and `references`). `diagnostics` returns the file's current diagnostics, or `[lsp go: no errors in a.go]`. `definition` and `references` return up to 100 `path:line:col: <source line>` locations, sorted and deduplicated; locations outside `--cwd` and its [additional directories](permissions.md#additional-directories), or denied by [file access](permissions.md#restricting-file-access) rules, are left out and counted. `path` follows the same rules as `read_file`, and a file with no configured server is a tool error. The tool only reads, so it is offered with `--read-only` (where edits, and so post-edit diagnostics, can't happen); [subagents](#subagents) never get it. Hooks match it as `lsp`.
- **Privacy and cost.** Servers read the files you edit or query (and whatever else they index under `--cwd`); some servers download modules or toolchains or use a lot of memory on large repositories. Files over 4 MiB are not sent.
- **Observability.** `--json` and `--log-db` emit an `lsp.server` event per state change and list `servers` under `lsp` in `run.started`; `-v` prints `lsp: go started` and similar lines; on start, one line such as `lsp: go (.go); ts (.ts, .tsx)` goes to stderr. `-o` trajectories record `lsp_enabled` and `lsp_servers`.
- **Resume.** A checkpoint records whether `lsp` was enabled, and `--resume` keeps that setting: passing a conflicting `--lsp` (including `--lsp=false`) exits 2, and the config's `lsp.enabled` is ignored on resume. Servers and limits come from the current invocation.
