# Changelog

## Unreleased

### Features

- Add opt-in file access rules (`file_access.deny_read` / `deny_write` config) with gitignore-style patterns relative to `--cwd`. `read_file`, `view_image`, `write_file`, `edit_file` and `apply_patch` refuse denied paths (checked through symlinks), `list_files` and `grep` skip them and report only a count of hidden paths, and `deny_read` paths are also write-denied. Rules are config-only, recorded in trajectories and `run.started`, and do not restrict `bash`, `start_process`, hooks or MCP servers.
- Add opt-in workspace snapshots (`--snapshots`, `snapshots` config) and `--rewind <trajectory> --cwd <dir> [--to start|<step>|<id>] [--dry-run]`, which restores files modified, created or deleted during a run. Snapshots are taken at the start, before each tool batch that can change files, and at the end, in a local content-addressed store under `.tinyagent/snapshots`; gitignored and oversized files are left alone, and every rewind first saves a `pre-rewind` snapshot so it can be undone.
- Add an opt-in command environment policy (`command_env` config, `--command-env all|core|none`) that controls which environment variables `bash` and `start_process` commands inherit: a trimmed `core` or empty base, `exclude_secrets` (secret-named variables and tinyagent's provider keys), `exclude`/`pass` globs and fixed `set` values. The default still inherits everything.
- Define named model providers in config (`providers`) with an `api` (`chat`, `responses` or `anthropic`), `base_url`, an API key read from an environment variable, extra headers, and per-model price and context-window metadata, selected with `-m <id>/<model>`. Provider config errors and missing key variables exit 2 before any request; keys and header values are never recorded; `run.started` and trajectories record `provider_api` and `provider_base_url`, and `--resume` refuses a changed provider.
- Add `--max-output-tokens` (config `max_output_tokens`) to set the per-request output-token budget for Anthropic, OpenAI, OpenRouter and Ollama.
- Add a sandbox network allowlist: `sandbox_allowed_domains` (or the repeatable `--sandbox-allow-domain`) lets `--sandbox` commands reach only the listed hosts over HTTP(S) through a tinyagent-run filtering proxy that refuses loopback, private and metadata addresses. On Linux each command runs in its own network namespace (needs unprivileged user namespaces; fails closed otherwise). Refusals are reported as `sandbox.network_denied` events and in trajectories.
- Continue a finished (`Submitted`/`Completed`) run with a follow-up task: `--resume <trajectory> -t "<follow-up>"` sends the saved conversation plus a follow-up notice and the new instruction, keeps step/cost totals cumulative, and records `follow_up` in the resume provenance and `run.started`.
- Discover Agent Skills (`SKILL.md`) from `.agents/skills` in the project and `~/.agents/skills`, list them in the system prompt, and load them on demand with a new `skill` tool (opt-in via `--skills` or `skills: true`).
- Run user-configured `pre_tool` and `post_tool` hook commands around tool calls to deny calls or append feedback.
- Add `stop` hooks that check a run before it finishes successfully and can send the model back to work, capped by `hooks.max_stop_continuations`, with the new `VerificationFailed` status.
- Add `--output-schema` to end runs with a validated JSON result on stdout (or `structured_output` in `--json`), with the new `InvalidOutput` status.
- Add an opt-in SQLite event log (`--log-db`, `log_db` config) that records runs, events, and tool calls mirroring `--json`, with metadata-only default, redaction, and retention.
- Emit a `provider.retry` event in `--json` output when a model request is retried.
- Add an opt-in `apply_patch` tool (`--apply-patch`, `apply_patch: true`) that applies multi-file, multi-hunk patches in OpenAI's `*** Begin Patch` format (add, update, rename, delete), checking every operation before any write.
- Add opt-in image support (`--images`, `images.enabled`, `max_image_bytes`): a `view_image` tool and MCP image pass-through for Anthropic and Codex, with a text placeholder for Chat Completions providers.
- Preserve Anthropic thinking blocks and Codex reasoning items across tool calls, and add a `reasoning_effort` config key and `--reasoning-effort` flag.
- Add a Responses API transport to the `openai` provider so tool calls work with reasoning on current OpenAI models, with encrypted reasoning replay and cached-token pricing. The new `openai_api` config key and `--openai-api` flag choose `auto` (default), `responses`, or `chat`.
- Add opt-in tool output files (`--tool-output-files`, `tool_output_files` config): tool results over `max_output_chars` are saved in full to a per-run file under `.tinyagent/tool-output`, and the model gets a bounded preview with a notice naming the path to page with `read_file` and `grep`. `tool.result` events, `post_tool` hook input, and trajectories record `output_file`; old runs are pruned by `tool_output_files.max_runs` (default 20).

### Fixes

- Responses cut off at the output-token limit are no longer treated as finished: their tool calls are not run, the model is asked to continue in smaller pieces, and after `max_truncations` (default 3) in a row the run ends with the new `OutputTruncated` status (exit 3).
- The event log now redacts the values of environment variables that hooks forward by name (including the new stop hooks).

### Changed

- Configs that use the new `providers` key fail to load on older tinyagent versions, which reject unknown keys.
- The `openai` provider now uses the Responses API against `api.openai.com` (when `OPENAI_BASE_URL` is unset or points there). Set `openai_api: chat` to keep Chat Completions. Custom `OPENAI_BASE_URL` endpoints keep Chat Completions by default.

## v0.4.0 - 2026-10-24

This release adds controls and integrations for longer, more scriptable agent runs, along with stronger file and tool handling.

### Breaking Changes

- None.

### Features

- Resume validated unfinished runs from saved trajectory checkpoints.
- Compact long conversations automatically as they approach the model context limit.
- Add background process tools and optional live output streaming for long-running commands.
- Allow explicitly selected tools from configured local MCP servers.
- Add read-only mode and configurable default models.
- Emit opt-in JSONL run events for scripting and CI integrations.
- Add per-call bash timeouts and optional surrounding lines, literal matching, and case-insensitive search to `grep`.

### Fixes

- Reject binary files in `read_file` with a clear error.
- Reject invalid step and cost limits, and warn when a cost limit cannot be enforced for an unpriced model.

### Other

- Enforce Go formatting in CI and run tests without using the test cache.
