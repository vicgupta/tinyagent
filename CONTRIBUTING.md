# Contributing to tinyagent

Thanks for your interest in contributing. tinyagent is a minimal coding agent in a
single static Go binary — it gives an LLM six tools and keeps a linear message
history until the model submits. We keep the codebase small and the review bar
high, so a little coordination up front goes a long way.

## Ground rules

- **MIT license.** By contributing you agree your changes are under the same
  [MIT license](LICENSE).
- **Small, focused pull requests.** One change per PR. Refactors and behavior
  changes go in separate PRs, never the same one.
- **Merge, don't rebase.** To update a branch, merge the latest `main` into it;
  no rebasing and no force-pushing. Resolve conflicts while keeping both sides'
  changes. PRs are merged with a merge commit only after CI is green.
- **Two PRs that touch the same file merge serially, never in parallel.** See
  [REVIEWPR.md](REVIEWPR.md) for the triage workflow.

## Getting started

```bash
git clone https://github.com/vicgupta/tinyagent && cd tinyagent
make build    # ./tinyagent
make test     # go test ./...
make dist     # linux/darwin/windows x amd64/arm64 in dist/
```

You need Go 1.27 (see `go.mod`). The only dependency is `gopkg.in/yaml.v3`; keep
it that way unless there is a very good reason to add one.

## Layout

```
cmd/tinyagent/        CLI entrypoint, flag parsing, main loop glue
internal/agent/       the run loop, compaction, tool dispatch; build an Agent with agent.New (it validates the options)
internal/config/      YAML config + built-in defaults
internal/model/       providers, price table, context windows
internal/cwdpath/     the shared path guard: resolves paths inside --cwd, refusing absolute paths, .. and symlink escapes
internal/playbooks/   playbook discovery and parsing (TinyAgent and pstack formats), routing, the embedded sample pack
internal/harness/     skill-workflow routing and keyword matching (match.go, shared with playbooks)
internal/resume/      what a --resume must match in its checkpoint (flag table, root, tool fingerprint, tool names)
internal/tools/       the six tools (read_file, write_file, edit_file, list_files, grep, bash)
internal/trajectory/  the -o JSON output
internal/skills/      Agent Skills discovery (./skills only) and the skill tool
internal/update/      --update self-update
embed/prompts/        system prompt templates
```

`cmd/tinyagent/playbooks.go` holds the playbook flags, the `tinyagent playbooks`
subcommand and the hooks into a run; samples live in
`internal/playbooks/samples/` with identical copies in `docs/playbooks/`.

**Paths for playbooks and skills.** Playbooks and skills are read only from
inside the current working directory: `./playbooks` and `./skills` in `--cwd`,
never the home folder, parent folders or plugin directories outside `--cwd`.
Resolve every such path through `internal/cwdpath`, which cleans it, follows
symlinks and refuses an absolute path, `..` or a symlink that escapes `--cwd`
(with a warning). Don't read these files with a plain `os.ReadFile` on a
joined path.

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

6. Merge the latest `main` into your branch (`git fetch origin && git merge origin/main`),
   resolve any conflicts keeping both sides' changes, re-run the checks above,
   and push (never force-push).

## Code style

- Format with `gofmt`; keep packages small and idiomatic.
- Follow existing naming and error-handling patterns. Errors should be actionable
  and name the file or key involved (see the config loader and provider code).
- Prefer the standard library; add dependencies only with justification.

## Adding an opt-in tool

The tool set for a run is owned by `tools.Selection` (`internal/tools/selection.go`).
Its `Specs()` is what the model is offered and its `Signature()` is the fingerprint a
checkpoint must match to `--resume`; both come from the same list, so add the new
tool's flag and spec there (plus a signature prefix if it is a built-in) and its
behavior in the executor. Don't build a second list elsewhere. The signature of
existing selections must not change, or older checkpoints stop resuming;
`TestSelectionSignaturePinned` guards this.

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

## Adding or changing a model / provider

`internal/model/` holds the provider and model metadata. When you touch it:

- Add or update the price and context-window tables so `--cost-limit` and
  compaction keep working. Unknown models cost $0, which silently defeats
  `--cost-limit`.
- Update the flag tables in `docs/subcommands.md` and the troubleshooting table in `docs/troubleshooting.md` if the change
  is user-visible (new provider, new hint, new env var).
- Add a `hint:` for common setup failures, matching the existing troubleshooting
  table. Hints name environment variables without printing their values.

## Pull request checklist

Before opening a PR, confirm:

- [ ] Latest `main` merged in (no rebase, no force-push); conflicts keep both sides.
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
