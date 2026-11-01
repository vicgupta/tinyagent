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
make build    # ./tinyagent
make test     # go test ./...
make dist     # linux/darwin/windows x amd64/arm64 in dist/
```

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

```
cmd/tinyagent/        CLI entrypoint, flag parsing, main loop glue
internal/agent/       the run loop, compaction, tool dispatch; build an Agent with agent.New (it validates the options)
internal/config/      YAML config + built-in defaults
internal/model/       providers, price table, context windows; CLI handoff providers (claudecode.go, codexexec.go, opencode.go) share handoff.go
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
the time of the slowest job. Reproduce the race job locally with
`go test -race -count=1 ./...`.

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

## Pull request checklist

Before opening a PR, confirm:

- [ ] Rebased onto current `main`; no merge commits.
- [ ] `gofmt -l .` is empty.
- [ ] `go build ./...`, `go vet ./...`, `go test -count=1 ./...` pass.
- [ ] `GOOS=linux go build ./... && GOOS=linux go vet ./...` pass if build-tagged
      files changed.
- [ ] `README.md` or `docs/` updated for any user-visible change (flag, config key, env var,
      provider, hint).
- [ ] Tests cover the new behavior, including any golden-output expectations.
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
Only run it on code you trust. If you find a security issue, please report it
privately rather than opening a public issue.

## Releasing (maintainers)

Releases are cut by pushing a tag — the `release.yml` workflow builds all
platforms and attaches `SHA256SUMS`:

```bash
git tag v0.x.y && git push origin v0.x.y
```

`make dist` reproduces the exact artifacts locally. `install.sh` verifies
checksums, so tag only after the full suite is green on `main`.
