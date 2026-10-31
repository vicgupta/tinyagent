# Editor integration and GitHub Actions

## Editor integration (ACP)

`tinyagent --acp` speaks the [Agent Client Protocol](https://agentclientprotocol.com) (protocol version 1, newline-delimited JSON-RPC on stdin/stdout), so any ACP client can run tinyagent as an external agent: you type in the editor's agent panel, tinyagent runs its normal loop in the session's directory, and the editor shows the model's text and each tool call live. The mode is **experimental**: the stop-reason mapping and the MCP default below may change based on feedback.

Zed (`settings.json`):

```json
{
  "agent_servers": {
    "tinyagent": {
      "type": "custom",
      "command": "/usr/local/bin/tinyagent",
      "args": ["--acp", "-m", "anthropic/claude-sonnet-4-5"],
      "env": { "ANTHROPIC_API_KEY": "..." }
    }
  }
}
```

JetBrains IDEs (2025.3+, `~/.jetbrains/acp.json`):

```json
{
  "agent_servers": {
    "tinyagent": {
      "command": "/usr/local/bin/tinyagent",
      "args": ["--acp", "--profile", "editor"],
      "env": {}
    }
  }
}
```

Use the full path to the binary. Pass `-m`, `-c`, `--profile` and any policy flags (`--read-only`, `--sandbox`, `--env`, `--command-env`, `--add-dir-*`, `--plan`, `--apply-patch`, `--subagents`, `--workers`, `--web-fetch`, `--lsp`, `--images`, `--tool-output-files`, `--skills`, `--scoped-agents-md`, `--redact-secrets`, `--log-db`, `-v`) in `args`. They mean the same as in a headless run.

**How it works**

- `session/new` takes the editor's absolute `cwd` as the session's root (`AGENTS.md` discovery runs as for `--cwd`) and checks the config, model and provider key there. A relative or missing directory, an invalid config or a missing API key is returned as a JSON-RPC error with the same message and `hint:` a headless run prints. Key values are never printed. A process can run several sessions.
- The first `session/prompt` is the session's task. Each later prompt continues the same conversation, sending the provider exactly what `--resume <trajectory> -t "<follow-up>"` would. This also works after a turn that was cancelled or hit a limit. A second prompt while one is running in the same session gets a JSON-RPC error.
- Prompt content: text blocks are joined; embedded text resources are included as quoted context naming their URI; resource links become a reference line and are not fetched; images are attached like `--image` (PNG, JPEG, GIF or WebP, at most 10, each within `max_image_bytes`). Audio, binary resources and invalid images get an invalid-params error.
- Streaming: assistant text arrives as `agent_message_chunk` (without the submit marker). Each tool call is a `tool_call` with the model's call id, a short title, a `kind` (`read_file` → `read`; `write_file`/`edit_file`/`apply_patch` → `edit`; `grep`/`list_files` → `search`; `bash`/`start_process` → `execute`; `web_fetch` → `fetch`; others → `other`) and absolute `locations` for file tools. It is followed by `tool_call_update`s to `in_progress` and then `completed` or `failed`, carrying the same result text the model sees, after `max_output_chars` and secret redaction. `update_plan` sends `plan` updates, and each model call sends a `usage_update` when the context window is known.
- Stop reasons: `Submitted`/`Completed` → `end_turn`, `StepLimitExceeded` → `max_turn_requests`, `OutputTruncated` → `max_tokens`, `session/cancel` → `cancelled`. Any other status (`CostLimitExceeded`, `TokenLimitExceeded`, `TimeLimitExceeded`, `RepeatedFormatError`, `VerificationFailed`, `Error`) ends with a final message naming it and `end_turn`, and the session stays usable. A provider error a headless run would print a setup `hint:` for is returned as a JSON-RPC error instead.
- `session/cancel` stops the turn the way Ctrl-C does: in-flight requests and commands are cancelled, and background processes and language servers are stopped, as at the end of every turn. Closing stdin cancels running turns, stops MCP servers and exits 0.
- `--step-limit` and `--cost-limit` apply to each prompt (on top of what the session has already used), and `--time-limit` limits each prompt's wall-clock time.
- Every prompt gets the run's full tool set, including your config's [command tools](config.md#command-tools) (not offered in read-only sessions).
- `--log-db` records each prompt as a run, with an `acp.turn` event naming the session id and turn number. No trajectory files are written.
- stdout carries only ACP messages. Warnings, the per-turn summary and `-v` traces go to stderr, which editors show in their agent logs.

**Client-supplied MCP servers** are off by default: tinyagent starts only the `mcp_servers` from your config, and a session's first reply names any servers the editor offered. Set `acp.client_mcp_servers: true` in `-c` file or a trusted `./tinyagent.yaml` (`--trust-config`) to start the editor's stdio servers for that session, with every tool they list exposed as `<name>__<tool>` and their `env` values set (never logged). HTTP and SSE servers are not supported (`mcpCapabilities.http` is `false`). Read-only sessions start none.

**Not supported:** `session/load`, session modes and config options, slash commands, delegating files or commands to the client (`fs/*`, `terminal/*`; tinyagent uses its own tools on disk), audio prompts, `ask_user` (it is turned off in this mode), workspace snapshots and reasoning (`agent_thought_chunk`) streaming. Per-tool approval is available through the [`permissions`](permissions.md#permissions) policy: a rule with `decision: ask` sends `session/request_permission` and the editor's Approve/Reject buttons answer it.

**Trust model:** the editor controls the prompts and the `cwd`, so it has the same power as whoever runs `tinyagent -t` in that directory. Everything that limits what tinyagent may do (the tool set, `--read-only`, `--sandbox`, `file_access`, `command_env`, hooks and limits) comes from your config and flags, and ACP adds no way to loosen it. Repository content stays untrusted prompt input, as in any run.

For a conversation in the terminal instead of an editor, use [`tinyagent chat`](chat.md); it shares the ACP session code, with the same lockdown and MCP handling.

## GitHub Actions

`action.yml` at the root of this repo is a composite action. It installs a tinyagent release on a Linux or macOS runner (amd64 or arm64), checks it against the release's `SHA256SUMS`, runs one task or review, and sets the result as step outputs. It adds no agent behavior: every input maps to an existing flag.

```yaml
permissions:
  contents: read
steps:
  - uses: actions/checkout@v4
  - id: agent
    uses: vicgupta/tinyagent@v0.5.0
    env:
      ANTHROPIC_API_KEY: ${{ secrets.ANTHROPIC_API_KEY }}
    with:
      model: anthropic/claude-sonnet-4-5
      task: Fix the failing test in ./internal/parser.
      job-summary: true
```

**Best-of-N.** Run up to three attempts and keep the first whose checks pass; the winner's branch is left in the checkout's worktrees for a later step to push or open a PR from:

```yaml
  - id: agent
    uses: vicgupta/tinyagent@v0.5.0
    env:
      ANTHROPIC_API_KEY: ${{ secrets.ANTHROPIC_API_KEY }}
    with:
      model: anthropic/claude-sonnet-4-5
      task: Fix the failing test in ./internal/parser.
      attempts: 3
      attempt-checks: |
        go vet ./...
        go test ./...
  - run: git log -1 "$BRANCH"
    env:
      BRANCH: ${{ steps.agent.outputs.attempts-winner-branch }}
```

**Pinning.** Under a `vX.Y.Z` tag the action installs the tinyagent release with the same tag, so `@v0.5.0` runs tinyagent v0.5.0. Under any other ref (a branch or a commit SHA) set `version` (`vX.Y.Z`, or `latest` to opt into the newest release) or `tinyagent-path`, or the step fails. A tag can be moved; for a fixed action pin to the tag's full commit SHA and set `version`: `uses: vicgupta/tinyagent@<sha> # v0.5.0` with `version: v0.5.0`.

**Read-only PR review with a findings schema.** Review flags imply `--read-only`, so the agent can't edit files or run commands:

```yaml
on: pull_request
permissions:
  contents: read
jobs:
  review:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with: { fetch-depth: 0 }
      - id: review
        uses: vicgupta/tinyagent@v0.5.0
        env:
          ANTHROPIC_API_KEY: ${{ secrets.ANTHROPIC_API_KEY }}
        with:
          model: anthropic/claude-sonnet-4-5
          review-base: origin/${{ github.base_ref }}
          output-schema: .github/tinyagent/findings.schema.json
          config: .github/tinyagent/ci.yaml
          job-summary: true
      - run: jq . <<<"$FINDINGS"
        env:
          FINDINGS: ${{ steps.review.outputs.structured-output }}
```

**Inputs.** `task` or `task-file` (one is required unless `review-base` or `review-commit` is set; with a review it adds focus), `model` (`-m`), `profile`, `config` (`-c`), `working-directory` (`--cwd`, default the workspace), `review-base`, `review-commit`, `read-only`, `sandbox`, `lockdown` and `untrusted-task` (`true`/`false`, default `false`; `lockdown: false` passes nothing, so the config decides), `output-schema`, `args` (extra flags, one argv element per line, never shell-evaluated or split), `version`, `tinyagent-path` (use an existing binary instead of downloading), `job-summary` (default `false`), `fail-on-error` (default `true`), and for [best-of-N](runs.md#best-of-n-attempts) `attempts` (`--attempts`), `attempt-models` and `attempt-checks` (one per line, each `--attempt-model`/`--attempt-check`) and `attempt-check-timeout`. The task reaches tinyagent on stdin (`-t -`) and every input through `env:`, so text like `$(...)` or backticks is never run by a shell. With `attempts`, which can't read the task from stdin, the task (at most 128 KiB) is passed as a single `-t` argument instead, still never shell-evaluated; `attempts` can't be combined with `review-base`, `review-commit` or `read-only`, and the `attempt-*` inputs need it.

**Outputs.** `status`, `submission`, `structured-output` (compact JSON, set only for a `Submitted` run with `output-schema`), `steps` and `cost-usd`, all from the final [`result` event](json-output.md#machine-readable-output); `exit-code` (always set once tinyagent ran); and `trajectory` and `events`, the paths of the `-o` file and the saved `--json` stream under `$RUNNER_TEMP`. With `attempts`, `status`, `submission`, `structured-output`, `steps` and `trajectory` are the winning attempt's (empty when none passed), `cost-usd` is the total over all attempts, and `attempts-winner` (the attempt number), `attempts-winner-branch` and `attempts-report` (the `.attempts.json` report) are set. They are not uploaded; add an artifact step if you want them. Each value is written to `$GITHUB_OUTPUT` behind a random delimiter, and the step fails instead of setting a value that contains it, so a submission can't forge other outputs. While tinyagent runs, workflow commands (`::add-mask::`, `::error::` ...) in its log output are disabled.

**Failing the step.** A non-zero [exit status](subcommands.md#exit-status) (1, 3 `OutputTruncated`, 4 `NeedsInput`) fails the step unless `fail-on-error: false`, in which case later steps can read `exit-code` and `status`. A usage error (exit 2), a bad input, or an install failure (unsupported runner, missing asset, checksum mismatch) always fails the step, before any model request.

**Job summary.** `job-summary: true` appends the [`--transcript`](runs.md#view-a-run-as-a-transcript) rendering to the job summary, also when the run failed. It is fenced and redacted but still model-authored. Over GitHub's 1 MiB step-summary limit, a one-line notice is appended instead.

**Requirements.** `jq` (preinstalled on GitHub-hosted Linux and macOS runners) and `bash`. Windows runners are not supported.

### Untrusted input

Issue bodies, PR titles, diffs and commit messages can carry prompt injection. The action keeps them out of the shell, but it can't stop the model from following instructions in them, so limit what a hijacked run can reach:

- tinyagent and its commands can read anything in their environment, including the provider API key, unless a [command environment](sandbox.md#command-environment) policy excludes it. Use a config such as:

  ```yaml
  # .github/tinyagent/ci.yaml
  command_env:
    inherit: core
    exclude_secrets: true
  ```

- Prefer `review-base`/`review-commit` or `read-only: true`, which have no command or write tools. Add `sandbox: true` for runs that do execute commands. On Linux it needs Landlock (kernel 6.2+, 6.7+ to deny TCP), and a network allowlist also needs unprivileged user namespaces, which Ubuntu 23.10+ can restrict with AppArmor. This hasn't been checked on GitHub-hosted runners; tinyagent fails closed and says what is missing if the runner can't enforce it.
- When the task is untrusted text, such as an issue body, set `lockdown: true` and `untrusted-task: true`. The run then starts [tainted](permissions.md#untrusted-content-lockdown): commands and MCP tools are refused, and `web_fetch` only reaches `untrusted_content.lockdown.web_fetch_allowed_domains`, while file tools, edits and the submission still work. Review runs are tainted by their diff once lockdown is on, unless `review` is left out of `untrusted_content.sources`.
- Set minimal `permissions:` and don't put `GITHUB_TOKEN` in the action's `env:`; the action never passes it on itself.
- Don't run on `pull_request_target` (or `workflow_run`) with a checkout of the PR's code.
- Treat every output and the job summary as untrusted: pass them to later steps through `env:`, never `${{ }}` inside `run:`.

See GitHub's [Secure use reference](https://docs.github.com/en/actions/reference/security/secure-use).
