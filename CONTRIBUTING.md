# Contributing to tinyagent

Thanks for your interest in contributing. tinyagent is a minimal coding agent in a
single static Go binary — it gives an LLM ten base tools (`read_file`,
`write_file`, `edit_file`, `undo_file`, `list_files`, `grep`, `bash`,
`start_process`, `read_process`, `stop_process`), plus opt-in tools, and keeps a
linear message history until the model submits. We keep the codebase small and the review bar
high, so a little coordination up front goes a long way.

## Ground rules

- **MIT license.** By contributing you agree your changes are under the same
  [MIT license](LICENSE).
- **Small, focused pull requests.** One change per PR. Refactors and behavior
  changes go in separate PRs, never the same one.
- **Linear history.** Rebase onto `main`; don't merge `main` into your branch.
  Merge commits that carry conflict-marker residue will be sent back.
- **Two PRs that touch the same file merge serially, never in parallel.** See
  [REVIEWPR.md](REVIEWPR.md) for the triage workflow.

## Getting started

```bash
git clone https://github.com/vicgupta/tinyagent && cd tinyagent
make build      # ./tinyagent
make test       # go test -count=1 ./...  (same as CI)
make test-race  # go test -race -count=1 ./...  (same as the CI race job)
make fmt        # fails and lists files if gofmt -l . is not empty (same as CI)
make lint       # go vet ./..., then staticcheck ./... if staticcheck is installed
make dist       # linux/darwin/windows x amd64/arm64 in dist/, plus Windows zips
```

`make lint` skips staticcheck with a hint when it isn't on your `PATH`; install it
with `go install honnef.co/go/tools/cmd/staticcheck@latest`. It is not a module
dependency. `make fmt` only checks; run `gofmt -w .` to fix files.

You need Go 1.27 (see `go.mod`). Keep dependencies to a minimum and don't add one
unless there is a very good reason. The direct dependencies in `go.mod` are:

<!-- direct-deps:start -->
- `github.com/BurntSushi/toml`: reads TOML (`memory.toml`, plugin data modules, ratchet files).
- `github.com/extism/go-sdk`: runs WebAssembly plugins.
- `github.com/modelcontextprotocol/go-sdk`: the MCP client used for MCP tools and resources.
- `github.com/tetratelabs/wazero`: the WebAssembly runtime the plugin host configures.
- `golang.org/x/sys`: terminal (TTY and line editor) system calls.
- `gopkg.in/yaml.v3`: reads YAML config, profiles, skills, workflows and eval suites.
- `modernc.org/sqlite`: pure-Go SQLite (no cgo) for the memory store and event log.
<!-- direct-deps:end -->

If you add or remove a direct dependency, update this list too;
`internal/tools/docs_sync_test.go` fails when it drifts from `go.mod`.

## Layout

[ARCHITECTURE.md](ARCHITECTURE.md) has the full package map, the request
flow and the invariants every change must keep (cwd-only state, AGENTS.md
only, no user config, tool signature pinning, append-only history). Update it
when you add a package or change one of those rules.

```
cmd/tinyagent/        CLI entrypoint, flag parsing, main loop glue; help.go holds --help and help topics (help workflows)
cmd/tinyagent/web/    tinyagent web's page (HTML/CSS/JS, hand-written, embedded with go:embed; no build step, never commit generated files); web.go serves it, chat_session.go is the session core shared with chat
internal/agent/       the run loop, compaction, tool dispatch; build an Agent with agent.New (it validates the options)
internal/config/      YAML config (./.tinyagent/tinyagent.yaml, located in locate.go; the old ./tinyagent.yaml only gets a hint) + built-in defaults; memory.toml is read only from --cwd
internal/dotenv/      loads ./.env from --cwd at startup (real env wins, values never logged); tests that write a .env should unset its keys with t.Setenv+os.Unsetenv so they're restored
internal/cwdpath/     Resolve confines a config or env path to --cwd (refuses "..", absolute paths elsewhere and symlinks that leave it)
internal/memory/      project memory stores (markdown and SQLite), always under ./.tinyagent/memory in --cwd; confine.go refuses paths that leave --cwd
internal/model/       providers, price table, context windows; CLI handoff providers (claudecode.go, codexexec.go, opencode.go) share handoff.go
internal/tinygo/      pinned TinyGo toolchain manager; caches only in ./.tinyagent/cache/toolchains in --cwd (the tinygo / TINYAGENT_TINYGO executable override is used as given)
internal/termmd/       streaming Markdown-to-terminal renderer used by chat (no dependencies; golden tests in testdata/)
internal/resume/      what a --resume must match in its checkpoint (flag table, root, tool fingerprint, tool names); loads the checkpoint and pins its recorded settings onto the run config
internal/tools/       the base tools listed above, plus the opt-in tools; docs_sync_test.go keeps the docs' base tool list in sync with tools.Specs(false)
internal/trajectory/  the -o JSON output
internal/update/      --update self-update
embed/prompts/        system prompt templates
```

## Development workflow

1. Open an issue first if the change is anything beyond a one-line fix, so we can
   agree on approach before you write code.
2. Branch from current `main` (`git checkout main && git pull --ff-only`).
3. Make the change with tests.
4. Run the full suite before pushing:

   ```bash
   gofmt -l .                     # must print nothing
   go build ./...
   go vet ./...
   go test -count=1 ./...
   ```

5. **If you touched a `//go:build linux` file**, also verify Linux, because
   macOS `go test` does not compile them:

   ```bash
   GOOS=linux go build ./...
   GOOS=linux go vet ./...
   ```

6. Rebase onto the latest `main` (`git fetch origin && git rebase origin/main`),
   resolve any conflicts, and force-push. Keep history linear.

## Code style

- Format with `gofmt`; keep packages small and idiomatic.
- Follow existing naming and error-handling patterns. Errors should be actionable
  and name the file or key involved (see the config loader and provider code).
  **Tool errors the model sees** must state the cause and a concrete next step
  (joined by `; `, like `edit_file`'s unique-match message). Use
  `tools.Actionable` / `tools.Actionablef` (or the MCP `actionable` helpers);
  add a wording assertion for each new key error. See
  [Tool error messages](docs/tools.md#tool-error-messages).
- **Every tool call path must emit a tool result.** A denial, error, timeout,
  lockdown refusal, format error, ask_user answer/rejection, or resume close of
  an interrupted call still needs a `tool.result` event and a matching tool
  message in history before the next model request. A missing result breaks
  every later request. Prefer `recordToolResult` / `recordResult` (or the same
  fields the tool loop emits) over appending a bare tool message. See
  [Tool results](docs/json-output.md#tool-results).
- **Permission judge is allow-only.** The optional AI judge
  (`permissions.judge`, task `permission`) may auto-allow on high confidence
  only; it must never silently deny. Low confidence, risky, parse/model error
  and timeout fall back to the human prompt (or `non_interactive`). Explicit
  deny rules and lockdown must still win before the judge runs. Record the
  judge's verdict on the `permission` event. Tests use a fake judge model only
  (see `internal/agent/permission_judge_test.go`). See
  [AI permission judge](docs/permissions.md#ai-permission-judge).
- **Self-tuning never applies on its own.** `internal/selftune` (`tinyagent
  tune`, off unless `self_tuning.enabled`) may only write a candidate under
  `.tinyagent/observer/candidates/`; the skill file changes only through
  `tune apply` with the reviewed digest, and only while the file is unchanged.
  The target always comes from the project's workflow file, never from a run;
  treat run content (and model replies) as untrusted, redact and bound it, and
  keep `AGENTS.md`, `CLAUDE.md`, config, `.tinyagent/` state and safety
  settings out of reach. Keep every path inside `--cwd` with `internal/cwdpath`.
  Validation runs in throwaway copies outside the project, within the
  `self_tuning` bounds. Tests use fake stage runners and models only (see
  `internal/selftune/selftune_test.go` and `cmd/tinyagent/tune_cmd_test.go`).
  See [Self-tuning](docs/self-tuning.md).
- **History is append-only.** Never edit, reorder or drop a message that has
  already been sent to the model, and never rebuild the system prompt or tool
  definitions mid-run: add new context (notices, resume notes, results) as a new
  message at the end. Per-request extras (like the pinned plan block) must be
  added to a copy in `Agent.request`, never to the view or `t.Messages`. Only
  compaction may replace part of the view. `internal/agent/append_only_test.go`
  hashes the request prefix around every step and fails on any rewrite; extend
  it when you add a new kind of message. See
  [Append-only history and prompt caching](docs/long-runs.md#append-only-history-and-prompt-caching).
- Prefer the standard library; add dependencies only with justification.
- Terminal output must degrade cleanly without color: with `--color never`,
  `NO_COLOR` or a non-terminal, keep the structure (plain ASCII markers such
  as `#`, `-`, `|`) and write no escape codes. Chat UI pieces live in
  `cmd/tinyagent/chat_ui.go`; the Markdown renderer in `internal/termmd`.
  Update its goldens with `go test ./internal/termmd -update` and review the diff.

## Adding an opt-in tool

The tool set for a run is owned by `tools.Selection` (`internal/tools/selection.go`).
Its `Specs()` is what the model is offered and its `Signature()` is the fingerprint a
checkpoint must match to `--resume`; both come from the same list, so add the new
tool's flag and spec there (plus a signature prefix if it is a built-in) and its
behavior in the executor. Don't build a second list elsewhere. The signature of
existing selections must not change, or older checkpoints stop resuming;
`TestSelectionSignaturePinned` guards this.

Fresh runs, `--resume` and chat/ACP turns all build their `tools.Executor` with
`newRunExecutor` (`cmd/tinyagent/run_tools.go`), so a new executor field or a change
to MCP and command-tool wiring goes there once and reaches every entry point.
Don't construct a `tools.Executor` literal anywhere else in `cmd/tinyagent`;
`TestExecutorBuiltInOnePlace` guards this.

Optional per-task tool scoping (`--tools`, config/profile `tools:`, workflow
stage/skill `tools:`, and per-call `delegate`/`worker` `tools`) is off by
default so `Executor.Only` stays nil unless opted in. When active, validate
names against `Selection` (`ValidateScopedTools`), apply the filter to both
`Executor.Only` and loop-handled tools (`internal/agent/scope.go`), record
`ScopedTools` in `trajectory.Trajectory` for `--resume`, and strip
`WithToolsParam` in `Selection.fingerprintSpecs` so `Selection.Signature()`
stays unchanged.

## Testing

- Unit tests live next to code as `_test.go` files. Run `go test ./...`.
- **Golden/snapshot tests** (e.g. `--help` output) should be regenerated from the
  real binary, not hand-aligned. Run the command, capture stdout, and paste it
  into the expected string — column alignment is derived from flag lengths and is
  error-prone to count by hand.
- Keep an eye out for **cross-file redeclarations**: a textual merge is per-file,
  so two `_test.go` files each defining `func runCLI(...)` will only fail at
  `go vet`/`go test`, not at merge time.
- When resolving conflicts, keep **both** semantic behaviors — never drop an
  exit path, error branch, or test helper while taking one side.
- **Web UI** (`cmd/tinyagent/web.go`, `web/`): handler tests in `web_test.go`
  drive the real server through `httptest` with the fake provider
  (`newACPProvider`), so no network is used. The browser-side Markdown renderer is
  checked by `web/ui_test.js`, which `TestWebUIScripts` runs when `node` is installed.
  Keep the page dependency-free (the Markdown renderer is embedded; no CDN) and
  escape everything model-provided before it reaches the DOM. Tool calls, live
  tool output and tool results (`webToolKind`) are sent to the browser and kept
  in `/api/state` in either mode; the page's **Collapse tool calls** checkbox
  (`toolsCollapsed`/`setCollapsed` in `web/app.js`, default on, saved in
  `localStorage` as `tinyagent-collapse-tools`) only decides whether the
  `<details>` cards start open. Reasoning and the other internal events in
  `webQuietKind` are sent as `detail` events only with `-v`/`--verbose`;
  without it the stream also carries one `activity` event (`phase: working`)
  per call. `TestWebToolDataWithAndWithoutVerbose` covers both modes (SSE and
  `/api/state`), and `web/tool_output_test.js` covers the collapsed and
  expanded card states and the checkbox toggle. Keep new tool-card behaviour
  working in both states.
- **Chat line editor** (`cmd/tinyagent/lineedit.go`): drive it through the fake
  terminal in `lineedit_redraw_test.go` (`termEditor`), which models CR/LF,
  cursor moves, clears, rune widths and autowrap. Assert on the screen and on
  `lineFeeds`, not on raw bytes, and cover a colour prompt, a `NO_COLOR` prompt
  and multibyte input.
- **Chat Ctrl-C** must exit with status 130, whether it arrives as SIGINT
  (cooked input, Windows, or `kill -INT`) or as the raw-mode key (byte 3 on
  Linux and macOS). At the prompt it restores the terminal and leaves
  immediately; during a turn it cancels the run so the model stream, tools and
  child processes stop, saves a resumable checkpoint, then exits. Do not
  require a second press, clear the line and stay, or return to the prompt
  after cancelling a turn. Tests live in `TestChatInterruptAndResume`,
  `TestChatPromptCtrlCExits` and `TestChatPromptSIGINTExits`.
- **Slash commands** live in `chatCommands` (`cmd/tinyagent/chat_commands.go`)
  and are printed by `/help`. The web UI's list is `webCommands` in
  `cmd/tinyagent/web.go`, served at `/api/commands`, which fills the slash menu
  and the help dialog; handle the command in `apiCommand` too. Document a new
  command in `docs/chat.md` and, when the web UI has it, `docs/web.md`, and
  mention it in the README feature list. `/context` (the breakdown in
  `internal/agent/context_usage.go`, rendered by `formatContext`) must keep
  using the compaction character estimate and label estimates as estimates.
  `/model` (status via `modelStatusText`, switch via `session.switchModel`)
  must keep validating the spec like `-m`, preserve history, and leave role
  models that have their own config alone; cover show/switch/invalid in tests
  with the fake provider only.
  `/compact` (via `session.compactNow` and `agent.CompactNow`) must run
  immediately in chat and the web UI, print progress and the token count
  before → after (or that nothing could be compacted), emit the existing
  `compaction` event, leave stored messages unchanged, and on error report
  a next step without rewriting history. Do not defer compaction to the
  next message.
- **Goal mode is a flag wrapper.** `tinyagent goal` rewrites to
  `--plan --plan-first --verify-on-stop` (and `--check` to `--verify-check`) in
  `expandSubcommand`. Do not add a separate goal loop or engine. Tests for
  composition and help live in `cmd/tinyagent/goal_test.go`. There is no
  `/goal` chat command: those settings are start-of-run flags; start chat with
  `tinyagent goal -i` or pass the flags to `tinyagent chat`. See
  [Goal mode](docs/tasks.md#goal-mode).

### Continuous integration

`.github/workflows/ci.yml` runs three jobs in parallel on every pull request and
on pushes to `main`:

| Job | Runner | Runs |
| --- | --- | --- |
| `build-vet-test` | `ubuntu-latest` | `gofmt -l .`, `go build`, `go vet`, then `go test -count=1 ./...` with Fish installed and `TINYAGENT_REQUIRE_FISH=1` |
| `build-vet-test-macos` | `macos-latest` | `go build`, `go vet`, `go test -count=1 ./...` (Fish tests skip themselves when `fish` is missing) |
| `race` | `ubuntu-latest` | `go test -race -count=1 ./...` |

All three must pass before a merge. Each takes a few minutes; the macOS and
`-race` jobs are the slowest, so expect the whole workflow to finish in roughly
the time of the slowest job. Reproduce the jobs locally with `make fmt`,
`make test` and `make test-race` (the race job is `go test -race -count=1 ./...`).
`makefile_test.go` fails if the Makefile targets drift from these CI commands.

Both test jobs also fail if the suite created tinyagent's real user config
directory (`~/.config/tinyagent` on Linux, `~/Library/Application Support/tinyagent`
on macOS). Tests must use `t.TempDir()` and point `HOME` / `XDG_CONFIG_HOME` (or
the package's path helpers) at it instead of writing under the real home
directory.

On macOS `os.UserConfigDir()` ignores `XDG_CONFIG_HOME` and returns
`$HOME/Library/Application Support`, so setting only `XDG_CONFIG_HOME` is not
enough. Set both `HOME` and `XDG_CONFIG_HOME` to temp dirs and derive expected
paths from `os.UserConfigDir()` (in `cmd/tinyagent`, use `userConfigDir(t)` /
`userConfigPath(t)`) rather than joining `XDG_CONFIG_HOME` by hand. Temp dirs
on macOS also live under `/var`, a symlink to `/private/var`; compare against
`filepath.EvalSymlinks` results where the code canonicalizes paths.

**Cwd-only paths.** State that tinyagent keeps for a project (memory, chats,
handoff, tool output, worktrees and the like) lives under `./.tinyagent/` in
`--cwd`, never in the home folder or an `XDG_*` / user config directory, and
moving it is a hard cut: no silent migration, at most one stderr `hint:` line
naming the new location. An old location outside `--cwd` may only be stat'ed
for that hint, never read (and per-project data outside `--cwd`, like the old
memory JSON files, isn't looked at at all). Paths from config must be relative,
stay inside `--cwd` and not escape through `..` or symlinks (see
`internal/memory/confine.go`, or `internal/cwdpath` for new code). This covers
config files too: the project config is `./.tinyagent/tinyagent.yaml`
(`config.ProjectFile`; the old `./tinyagent.yaml` is only stat'ed for a hint;
tests that write a project config create `.tinyagent/` first), `memory.toml` (`./memory.toml` or `$TINYAGENT_MEMORY_CONFIG`
inside `--cwd`), plugin `PLUGIN_DATA` (`./.tinyagent/plugin-data/<name>`)
and the TinyGo toolchain cache (`./.tinyagent/cache/toolchains`) are never
read from the user config or cache directory. Tests for such a feature point `HOME`, `XDG_*`
and the user config dir at a temp dir that holds data in the old locations and
assert none of it is used. Agent Skills follow the same rule: only `./skills`
under `--cwd` is scanned (no parents, no `~/.agents/skills`).

Features that load repository content into the prompt (like Agent Skills) should
be on by default only in interactive runs and off in hardened modes (non-TTY,
`CI`/`GITHUB_ACTIONS`, `--eval`, `--attempts`, lockdown, `--read-only`), with
precedence flag > config > mode default and a cwd-config opt-in ignored in
hardened modes without `--trust-config`. Reuse `resolveSkills` and
`hardenedReason` in `cmd/tinyagent/skills_default.go`, report the decision in
`--check`, and in tests clear `CI`/`GITHUB_ACTIONS` and stub `runIsInteractive`
so they pass both locally and on CI.

## Adding or changing a model / provider

`internal/model/` holds the provider and model metadata. When you touch it:

- Add or update the price and context-window tables so `--cost-limit` and
  compaction keep working. Unknown models cost $0, which silently defeats
  `--cost-limit`.
- The HTTP client, retry policy, price table and price override live on a
  `model.Runtime` (`internal/model/runtime.go`), passed through
  `model.Options.Runtime`; nil means the process-wide `model.DefaultRuntime`.
  HTTP adapters read them from their `Runtime` field, never from package
  globals. In tests, build an isolated runtime (`model.NewRuntime()` or a
  literal `&model.Runtime{...}`) and use `t.Parallel()` instead of changing
  `DefaultRuntime` and restoring it with `t.Cleanup`.
- Update the flag tables in `docs/subcommands.md` and the troubleshooting table in `docs/troubleshooting.md` if the change
  is user-visible (new provider, new hint, new env var).
- Add a `hint:` for common setup failures, matching the existing troubleshooting
  table. Hints name environment variables without printing their values.
- CLI handoff providers (`claude-code`, `codex-exec`, `opencode`) only run the
  user's installed binary through `runCLI`/`runCLIEnv` in `handoff.go`: never
  read, copy or proxy the CLI's credential files or tokens, and don't add
  reads of the home or config folders. Register a new one in `provider.go`,
  the built-in provider lists (`internal/model/custom.go`,
  `internal/config/config.go`), `hint.go`, the `--check` line in
  `cmd/tinyagent/check.go`, its `<name>.path` config key (`config.go`,
  `locate.go`), and test it with a fake binary on PATH.
- Keep handoff CLI flags to ones every supported version of that CLI accepts.
  `--no-stream` (a nil text callback) must keep today's flags. Streaming may
  add flags that CLI's docs require for a live JSONL stream (`claude
  --output-format stream-json --verbose --include-partial-messages`); if the
  CLI rejects one, retry down to the non-streaming invocation. `codex exec
  --json` and `opencode run --format json` are already streams: read them as
  they arrive, don't add flags. Set the working directory with `cmd.Dir`,
  not a CLI flag (OpenCode v2 dropped `run --dir`), and fall back when an
  optional flag is rejected. Streamed assistant text goes through the model's
  text callback once; CLI tool and command chunks go through
  `tools.NewLivePreview` (secret redaction and the 256 KiB cap) and
  `tool.output`. Don't read, copy or proxy the CLI's credentials.

## Pull request checklist

Before opening a PR, confirm:

- [ ] Rebased onto current `main`; no merge commits.
- [ ] `gofmt -l .` is empty.
- [ ] `go build ./...`, `go vet ./...`, `go test -count=1 ./...` pass (`make fmt lint test`).
- [ ] `GOOS=linux go build ./... && GOOS=linux go vet ./...` pass if build-tagged
      files changed.
- [ ] `README.md` or `docs/` updated for any user-visible change (flag, config key, env var,
      provider, hint).
- [ ] Tests cover the new behavior, including any golden-output expectations.
- [ ] Workflow changes (a `--workflow*` flag, a workflow file key, a stage or
      observer event) update all three together: the `tinyagent help workflows` topic
      (`helpWorkflows` in `cmd/tinyagent/help.go`), [`docs/workflows.md`](docs/workflows.md),
      and the reference rows in `docs/subcommands.md`, `docs/json-output.md` and
      `docs/project-context.md`. A workflow file or skill-format change also updates the
      examples in [`docs/workflow-guide.md`](docs/workflow-guide.md);
      `TestWorkflowGuideExamples` (`internal/harness/guide_test.go`) loads them.
      `TestWorkflowHelpCoversEveryFlag`
      (`cmd/tinyagent/workflow_help_test.go`) fails when a workflow flag or event is
      missing from the topic or the page.
- [ ] PR body follows [`.github/pull_request_template.md`](.github/pull_request_template.md):
      three sections, under ~200 words, no transcripts or run logs.

See [REVIEWPR.md](REVIEWPR.md) for how PRs get triaged and merged, and
[LESSONS.md](LESSONS.md) for hard-won gotchas (rebases, build tags, golden tests,
cross-file redeclarations) before you start.

## Issue reports

Good issue reports make bugs easy to fix. Include:

- tinyagent version (`tinyagent -V`) and OS/arch.
- The exact command you ran (redact API keys).
- The full error, including any `hint:` line.
- With `-v` and `-o run.json`, the verbose tool trace and trajectory make most
  agent-behavior bugs trivial to reproduce.

Please do not paste API keys, tokens, or other secrets into issues or PRs.

## Security

The `bash` tool runs in a new process and **is not sandboxed**; path sandboxing
for the file tools is lexical and symlinks inside the root can point outside it.
`web_fetch` is on by default for public hosts, and fetched pages are untrusted
input that may carry prompt injection. Keep its non-public-address check
(`sandboxnet.IsNonPublic`) on every path, including with no allowlist, and keep
it off by default under lockdown and in the GitHub Action. Tests that assert a
run's first stderr line must allow the `web_fetch: any public host` startup line
(or pass `--no-web-fetch`). The same goes for `web_search` (startup line
`web_search: duckduckgo → exa`, `--no-web-search`): it must stay keyless, keep
the non-public check on every provider except the configured SearXNG host, be
refused after lockdown taint, and its tests must use `httptest` fakes, never
the real DuckDuckGo or Exa endpoints. Retry tests replace the searcher's
`retry.sleep` and `retry.jitter` (see `noSleep` in
`internal/tools/websearch_retry_test.go`) so they never wait; a caller's
cancellation must stop a search without retrying or falling back.
Only run it on code you trust. If you find a security issue, please report it
privately rather than opening a public issue.

## Releasing (maintainers)

Releases are cut by pushing a tag. The `release.yml` workflow builds all
platforms, attaches `SHA256SUMS`, and then publishes WinGet when it can.
The same steps are written up in [docs/release.md](docs/release.md).

```bash
git tag v0.x.y && git push origin v0.x.y
```

`make dist` reproduces the artifacts locally. It needs the `zip` command.
Unix and macOS builds are bare binaries. Windows builds are
`tinyagent-windows-amd64.zip` and `tinyagent-windows-arm64.zip` only. Each
zip contains one file, `tinyagent.exe`. The bare `.exe` is removed before
`SHA256SUMS` is written, and the release workflow rejects a `dist/` that
still has one. `tinyagent --update` on Windows downloads the zip. An older
release that only has `tinyagent-windows-<arch>.exe` still installs from
that file. `install.sh` verifies checksums, so tag only after the full
suite is green on `main`.

### WinGet

The `winget` job runs after the GitHub release is published. It opens a pull
request to [microsoft/winget-pkgs](https://github.com/microsoft/winget-pkgs)
for package `VicGupta.TinyAgent` using
[vedantmgoyal9/winget-releaser](https://github.com/vedantmgoyal9/winget-releaser)
(komac under the hood). The release job does not wait on that pull request.

One-time setup, on the `vicgupta` account:

1. Fork `microsoft/winget-pkgs` to `vicgupta/winget-pkgs`. Leave the fork
   named `winget-pkgs`. The action pushes the manifest branch there
   (`fork-user` defaults to the repository owner, `vicgupta`).
2. Create a **classic** personal access token on that same account
   (Settings → Developer settings → Personal access tokens → Tokens
   (classic)). Fine-grained tokens can commit but cannot open the pull
   request. Scopes:
   - `public_repo` (required): read the fork and open a public pull request.
   - `workflow`: so fork sync can update workflow files from upstream. Without
     it, `komac sync-fork` fails with an `UpdateRef` error until someone
     syncs the fork in the browser.
3. On `vicgupta/tinyagent`, add the token as an Actions repository secret
   named `WINGET_GITHUB_TOKEN` (Settings → Secrets and variables → Actions).
   Do not put the token in the workflow file.

When `WINGET_GITHUB_TOKEN` is unset, the job logs a notice and succeeds, and
the GitHub release is already published. The same skip happens for a tag that
is not `vX.Y.Z` (for example a release candidate) and when
`VicGupta.TinyAgent` is not in winget-pkgs yet.

The releaser only updates a package that already exists. The first submission
is a separate pull request, opened by hand, and **Microsoft reviews that
first submission manually** (later version bumps are usually validated by the
winget-pkgs automation). After the zips for `vX.Y.Z` are on the GitHub
release, run this locally in a terminal (komac prompts; it will not finish
unattended):

```bash
komac new VicGupta.TinyAgent \
  --version X.Y.Z \
  --urls \
    "https://github.com/vicgupta/tinyagent/releases/download/vX.Y.Z/tinyagent-windows-amd64.zip" \
    "https://github.com/vicgupta/tinyagent/releases/download/vX.Y.Z/tinyagent-windows-arm64.zip" \
  --publisher "Vic Gupta" \
  --publisher-url "https://github.com/vicgupta" \
  --package-name "TinyAgent" \
  --package-url "https://github.com/vicgupta/tinyagent" \
  --license "MIT" \
  --license-url "https://github.com/vicgupta/tinyagent/blob/vX.Y.Z/LICENSE" \
  --short-description "A minimal coding agent in a single static Go binary" \
  --moniker "tinyagent" \
  --release-notes-url "https://github.com/vicgupta/tinyagent/releases/tag/vX.Y.Z" \
  --submit
```

Set `GITHUB_TOKEN` to the same classic PAT before running it, or pass
`--token`. At the prompts, select the nested file `tinyagent.exe` for each
zip and set the portable command alias to `tinyagent`. The installer type is
`zip` and the nested installer type is `portable`. amd64 is x64 and arm64 is
arm64. Accept or skip the other installer prompts. Later tags copy this
shape, so the alias and the `tinyagent.exe` path have to match what `make
dist` puts in the zip.

### Live command-output tests

Tool preview coverage lives in `internal/tools/stream_test.go`, CLI/JSONL and persistence tests in `cmd/tinyagent/tool_output_test.go`, and the live web-card tests in `cmd/tinyagent/web/tool_output_test.js` (also run by `node cmd/tinyagent/web/ui_test.js`). Handoff streaming coverage lives in `internal/model/handoff_stream_test.go` and `internal/agent/handoff_stream_test.go`: fake CLI scripts emit events line by line, and a fifo holds the script open until the first live chunk is observed. Use pipes or channels to hold a command open until its first live chunk is observed; do not use timing sleeps to prove streaming. Keep `tool.output` ephemeral: it must not enter SQLite, OpenTelemetry or saved trajectories. Preserve `streamTo`/`outputTo` routing so `--output-schema` stdout stays a single compact JSON object.
