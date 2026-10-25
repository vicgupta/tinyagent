# Changelog

## Unreleased

### Features

- Discover Agent Skills (`SKILL.md`) from `.agents/skills` in the project and `~/.agents/skills`, list them in the system prompt, and load them on demand with a new `skill` tool (opt-in via `--skills` or `skills: true`).
- Run user-configured `pre_tool` and `post_tool` hook commands around tool calls to deny calls or append feedback.
- Add `stop` hooks that check a run before it finishes successfully and can send the model back to work, capped by `hooks.max_stop_continuations`, with the new `VerificationFailed` status.
- Add `--output-schema` to end runs with a validated JSON result on stdout (or `structured_output` in `--json`), with the new `InvalidOutput` status.
- Add an opt-in SQLite event log (`--log-db`, `log_db` config) that records runs, events, and tool calls mirroring `--json`, with metadata-only default, redaction, and retention.
- Emit a `provider.retry` event in `--json` output when a model request is retried.
- Add opt-in image support (`--images`, `images.enabled`, `max_image_bytes`): a `view_image` tool and MCP image pass-through for Anthropic and Codex, with a text placeholder for Chat Completions providers.
- Preserve Anthropic thinking blocks and Codex reasoning items across tool calls, and add a `reasoning_effort` config key and `--reasoning-effort` flag.
- Add a Responses API transport to the `openai` provider so tool calls work with reasoning on current OpenAI models, with encrypted reasoning replay and cached-token pricing. The new `openai_api` config key and `--openai-api` flag choose `auto` (default), `responses`, or `chat`.

### Fixes

- The event log now redacts the values of environment variables that hooks forward by name (including the new stop hooks).

### Changed

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
