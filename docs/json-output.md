# Output, events and telemetry

### Machine-readable output

`--json` makes stdout a UTF-8 JSONL event stream, with one complete JSON object and newline per event. It can be combined with `--no-stream`, `--read-only`, `--resume`, and `-o` (which still writes the trajectory JSON to its file). In this mode, streamed assistant text arrives as `assistant.delta` events while the model generates (unless `--no-stream`), live bash output is represented by events, and `--verbose` has no effect because tool calls and results are already events; the human summary and warnings remain on stderr. Setup failures before a run starts may leave stdout empty. With `--update`, stdout carries `update.step` and `update.result` events instead (see [Updating](subcommands.md#updating)).

Every event has a `type`. Event names and fields in schema version 1 are:

| Type | Fields |
|---|---|
| `run.started` | `schema_version` (currently `1`), `model`, canonical `root`, `read_only`, `resumed`, `follow_up` (`true`, only for a follow-up run), `attachments` (`index`, `name`, `mime_type`, `bytes`, `sha256` per [`--image`](long-runs.md#attaching-images-to-the-task) file, never the data; only with `--image`), `reasoning_effort` (only when set), `fallback_models` (only when a [fallback chain](providers.md#model-fallback) is configured), `openai_api` (the resolved `responses` or `chat`, only for the `openai` provider), `profile` (the name only; only with [`--profile`](config.md#profiles)), `command_env` (`inherit`, `exclude_secrets`, sorted `passed` names; only with a [command environment](sandbox.md#command-environment) policy), `redact_secrets` (`enabled`, `patterns`, sorted variable `names`; only with [secret redaction](permissions.md#redacting-secrets-from-tool-output) on), `file_access` (`deny_read`, `deny_write`; only with [file access](permissions.md#restricting-file-access) rules), `web_fetch` (`allowed_domains`; only with [web fetch](long-runs.md#web-fetch) on), `lsp` (`servers`, the configured names; only with [language servers](long-runs.md#language-servers-lsp) on), `scoped_agents_md_enabled` and `scoped_agents_md` (scoped files already in context, `[]` for a fresh run; only with [`--scoped-agents-md`](project-context.md#project-instructions-agentsmd) on), `additional_dirs` (`path`, `mode`; only with [additional directories](permissions.md#additional-directories)), `worktree` (`path`, `branch`, `base_commit`, `repo_root`; only with [`--worktree`](runs.md#worktrees)), `review` (`target`, `ref`, `base_commit`, `head_commit`, `commit`, `files_changed`, `diff_bytes`, `diff_bytes_sent`, `truncated`, `hidden_paths`; only with a [review flag](runs.md#reviewing-changes)), `otel` (`enabled`, `endpoint_host`, `protocol`, `trace_id`; only with [`--otel`](#opentelemetry-export), never headers), `lockdown` (`enabled`, `sources`, `web_fetch_allowed_domains`, `tainted`, and `commands`: `policy` `refused` or `sandboxed`, plus `network` `denied` or `proxy` and, behind the proxy, `allowed_domains`; only with [lockdown](permissions.md#untrusted-content-lockdown) on) |
| `model.request` | `step` (the step the request belongs to), `model` (the provider/model that served it, after any fallback), `duration_ms`, and either `input_tokens`, `output_tokens`, `cache_read_tokens`, `cache_write_tokens` or `error`; one per model request of the agent loop (compaction summaries excluded), emitted before that response's `assistant.message` |
| `assistant.message` | `text` (complete assistant message) |
| `assistant.delta` | `text` (one streamed chunk of assistant text, raw as the model sent it, including any submit marker); emitted as chunks arrive, before that response's `model.request` and `assistant.message`, whose `text` is authoritative. Not emitted with `--no-stream`, and only on the `--json` stream (not `--log-db` or `--otel`) |
| `tool.call` | `id`, `name`, `arguments` (decoded JSON value, or a string if malformed) |
| `tool.result` | `id`, `name`, `output` (same configured truncation as tool output), `is_error`; with [tool output files](long-runs.md#tool-output-files), a saved result also has `output_file` (path relative to `--cwd`) and `output_bytes`; with [secret redaction](permissions.md#redacting-secrets-from-tool-output), `redactions` (count, only when > 0) |
| `hook` | `event` (`pre_tool`, `post_tool`, or `stop`), `tool_call_id`, `tool_name`, `command`, `decision` (`allow`, `deny`, `feedback`, `continue`, or `error`), `duration_ms`; tool hooks are emitted between the call's `tool.call` and `tool.result`, and `stop` events have no tool fields |
| `plan.updated` | `step`, `version`, `items` (`id`, `text`, `status`), `note`, `diff` |
| `checkpoint.saved` | `step`, `path`, `pending` (tool calls without results yet) |
| `resume.interrupted_call` | `id`, `name` of a call that was in flight when the previous process stopped |
| `resume.process_lost` | `id`, `pid`, `cmd` of a background process from the previous invocation |
| `compaction` | `step`, `reason`, `stage`, `tokens_before`, `tokens_after`, `elided_messages`, `summarized_messages` |
| `workspace.snapshot` | `id`, `step` (0 = baseline), `kind` (`baseline`, `step`, `final`, or `pre-rewind`), `time`, `files`, `bytes`, `skipped_ignored`, `skipped_large`, `large_paths` (up to 20, when any), `store`, `duration_ms`; only with [snapshots](undo.md#rewind-workspace-changes) on. Paths and counts only, never file contents |
| `rewind.plan` / `rewind.result` | `--rewind --json` only: the target snapshot, each `{op, path}` change and counts; then `restored`, `not_restored`, `pre_rewind` and `error` (on a partial restore) |
| `warning` / `error` | `message` |
| `sandbox.network_denied` | `host`, `port`, `reason`; the sandbox allowlist proxy refused a request from a command (see [Network allowlist](sandbox.md#network-allowlist)) |
| `web_fetch` | `url`, `final_url`, `status`, `content_type`, `bytes`, `truncated`, `duration_ms`, and `denied_reason` (for a refused URL) or `error` (for a failed fetch); one per fetch attempt, emitted before its `tool.result` (see [Web fetch](long-runs.md#web-fetch)) |
| `memory.loaded` | `path` (the store file), `loaded` (entry count), `ids` (loaded entry ids, newest first), `omitted` (active entries left out to stay within `memory.max_bytes`); once after `run.started` for a fresh run with [project memory](memory.md#project-memory) on. A resumed or follow-up run of a checkpoint that had memory on emits it too, describing the replayed checkpoint snapshot (the store is not re-read), with `from_checkpoint: true`. Never includes entry text |
| `handoff.loaded` | `path`, `found` (whether the file existed), `bytes` (its size), `truncated` (cut to `handoff.max_bytes`); once after `run.started` for a fresh run with [session handoff](long-runs.md#session-handoff) on. Never includes the file text |
| `handoff.startup` | `command`, `exit_code` (-1 on timeout), `timed_out`, `duration_ms`; after `handoff.loaded` when `handoff.startup_command` ran. Never includes its output |
| `handoff.saved` | `path`, `bytes`, `status` (the run's final status); once before `result` when the handoff file was rewritten |
| `ratchet.loaded` | `path` (the rules file), `loaded` (active rule count), `ids` (active rule ids), `expired` (ids of expired rules that were skipped), `enforced` (active rules added as permission deny rules); once after `run.started` for a fresh run when `.tinyagent/ratchet.toml` exists and `--no-ratchet` isn't set. See [Project rules](ratchet.md). Never includes rule text |
| `budget.warning` | `step`, `usage` (what was used, as text), `tokens`, `token_limit`, `cost_usd`, `cost_limit`; once per run when a token or cost budget reaches 80% (see [Token and cost budgets](runs.md#token-and-cost-budgets)) |
| `verify.reprompt` | `reprompt`, `max_reprompts`; the model gave a final answer with unverified changes and was sent back to verify (see [Verify before stop](verify.md#verify-before-stop)) |
| `verify.check` | `index` (1-based position in `verify_on_stop.checks`), `command`, `passed`; one per [computational check](verify.md#computational-checks) tinyagent runs when the model finishes with unverified changes, emitted before any `verify.reprompt` |
| `verify.review` | `findings` (the review text), or `error` when the request failed; the [inferential review](verify.md#inferential-review) that runs once after every computational check passed, report only |
| `verify.unverified` | `reason`, `reprompts`; the run finishes with unverified changes |
| `memory.saved` | `op` (`add`, `replace` or `forget`), `id`, `status` (`active`, `pending` or `forgotten`), `redacted`, `untrusted_context`; one per successful `remember` write, emitted before its `tool.result`. Never includes entry text |
| `lsp.server` | `name`, `state` (`started`, `unavailable`, `crashed` or `stopped`), `reason` (for `unavailable` and `crashed`); one per language-server state change (see [Language servers (LSP)](long-runs.md#language-servers-lsp)) |
| `instructions.loaded` | `path`, `scope_dir` (both relative to `--cwd`), `bytes` (file size), `truncated`, `tool`, `tool_call_id`, `step`; a scoped `AGENTS.md` was appended to that call's result, emitted before its `tool.result` (only with [`--scoped-agents-md`](project-context.md#project-instructions-agentsmd)) |
| `instructions.skipped` | `path`, `scope_dir`, `reason` (`file_access`, `outside_root`, `modified`, `read_error` or `cap`), `tool`, `tool_call_id`, `step`, and `error` for `read_error`; a scoped `AGENTS.md` that applied was not delivered |
| `provider.retry` | `attempt` (the upcoming attempt number), `reason` (`http_<status>` or `network`), `backoff_ms`; only emitted when a model request is retried |
| `model.fallback` | `step`, `from`, `to`, `reason` (`http_<status>` or `network`), `attempts`; the run switched to the next [fallback model](providers.md#model-fallback) after `from` used up its retries |
| `taint` | `source` (`web_fetch`, `mcp`, `review`, `untrusted_task` or `resume`), `step`, and `tool_call_id`, `tool`, `host` (the `web_fetch` host only) and `resumed` when they apply; one per new taint source of a run with [lockdown](permissions.md#untrusted-content-lockdown) on. Never includes content or URLs |
| `lockdown.denied` | `channel` (`command`, `web_fetch` or `mcp`), `tool`, `tool_call_id`, `step`, and `host` for `web_fetch`; one per call lockdown refused after taint |
| `result` | `status` (for example `Submitted`, `Completed`, `StepLimitExceeded`, `CostLimitExceeded`, `TokenLimitExceeded`, `TimeLimitExceeded`, `NeedsInput`; see [Exit status](subcommands.md#exit-status)), `submission` (the question for `NeedsInput`), cumulative `steps`, `structured_output` (only with `--output-schema` and status `Submitted`), `question` (`tool_call_id`, `question`, `options` when given; only for status `NeedsInput`, see [Asking for input](runs.md#asking-for-input)), `input_tokens`, `output_tokens`, `cache_read_tokens`, `cache_write_tokens`, `cost_usd`, invocation `duration_ms`, and `tainted` and `lockdown_denials` (a count; only with [lockdown](permissions.md#untrusted-content-lockdown) on), `unverified` (`true` only when the run finished with unverified changes) and `verify_reprompts` (only when > 0; see [Verify before stop](verify.md#verify-before-stop)) |

When the run starts, the final event is `result`, including for model errors, interruptions, and limit stops. Exit codes are unchanged. `--resume` reports cumulative trajectory accounting; `duration_ms` covers only the current invocation. Event streams can contain prompts, code, and tool output, so handle them with the same care as trajectory files. Credentials and environment dumps are not included.

Consumers should check `schema_version` on `run.started`. Event names and field meanings are stable within a schema version; an incompatible change will increment the version. New optional fields and event types may be added without changing it. The JSONL stream is intended for scripting and CI integrations:

```sh
tinyagent --json -t "fix the failing test" --cwd . \
  | jq -c 'select(.type == "result") | {status, submission, cost_usd}'
```

tinyagent takes no positional arguments: quote the task and pass it with `-t`. A stray word such as `tinyagent "fix the tests"` or `tinyagent -t "..." typo --step-limit 5` is an error (exit 2), rather than silently dropping the flags after it.

The limit flags must be >= 0, and `--cost-limit` must also be finite. A typo such as `--step-limit -1` or `--cost-limit NaN` is an error (exit 2) instead of quietly running without the cap. An explicit 0 means unlimited and overrides the config file's limit.

Environment: `OPENAI_API_KEY`, `OPENAI_BASE_URL` (any OpenAI-compatible endpoint), `ANTHROPIC_API_KEY`, `ANTHROPIC_BASE_URL`, `OPENROUTER_API_KEY`, `OLLAMA_HOST`, `CODEX_HOME`.

### Structured output

`--output-schema schema.json` makes the run end with one JSON object that conforms to a JSON Schema, for scripts and CI:

```sh
tinyagent --review-base main --output-schema review.schema.json > review.json
```

The schema file (at most 64 KiB) must be a JSON object with `"type": "object"` at the root and is checked before any provider call; a bad schema exits 2. The model is given a `submit_result` tool whose parameters are the schema, and the run finishes only when it calls that tool, alone in its message, with arguments that validate. Invalid arguments, mixing `submit_result` with other tool calls, the completion marker, or a turn with no tool call are answered with the validation errors or a reminder; after 3 consecutive invalid attempts the run ends with status `InvalidOutput`. Hooks never run for `submit_result`.

On success the status is `Submitted`, stdout contains only the compact JSON object and a newline, and the summary, streamed text, and `-v` output go to stderr. With `--json`, the object is the `structured_output` field of the `result` event instead. Any other outcome exits 1 with nothing on stdout. Trajectories (`-o`) record `structured_output`, `output_schema`, and `output_schema_digest`; `submission` holds the compact JSON string. Runs started with `--output-schema` cannot be resumed, and `--resume` with `--output-schema` exits 2.

Supported keywords (the same subset as MCP tool schemas): `type`, `properties`, `required`, `additionalProperties: false`, `items`, `enum`, `const`, `minimum`, `maximum`, `exclusiveMinimum`, `exclusiveMaximum`, `multipleOf`, `minLength`, `maxLength`, `pattern` (Go regexp syntax), `minItems`, `maxItems`, `uniqueItems`, `minProperties`, `maxProperties`. `format` is accepted but not enforced. Providers also see the schema, but tinyagent validates locally, so results never depend on provider-side strict modes.
### Event log database

`--log-db` (or `log_db.enabled: true` in config) records every event the `--json` stream would contain in a local SQLite file, whether or not `--json` is also on. It is off by default; nothing is created unless enabled. The database lives at `<cwd>/.tinyagent/tinyagent.db` unless `--log-db=path` or `log_db.path` says otherwise (a flag path is relative to the shell's directory, a config path to `--cwd`; `~/` is expanded). Add `.tinyagent/` to your `.gitignore`. The file is created with mode 0600, uses WAL mode with a busy timeout so concurrent runs can share it, and needs no cgo (pure-Go `modernc.org/sqlite`).

Tables: `runs` (one row per invocation: model, provider, cwd, status, steps, token and cost totals, duration, version), `events` (`run_id`, `seq`, `step`, `ts`, `type`, JSON `payload`), `tool_calls` (tool name, arguments, result summary, duration, `is_error`), `subagents` (one row per [delegate](long-runs.md#subagents) or [worker](long-runs.md#workers) subagent: `kind`, `subagent_id`, `tool_call_id`, `parent_step`, `label`, `model`, `status`, `steps`, token and cost totals, `started_at`, `ended_at`, `duration_ms`; workers also record `branch`, `base_commit`, `commit_sha` (empty when the worker changed nothing), `files_changed` and `applied`, set to `1` when `apply_worker` applies the change), `attempt_groups` (one row per [`--attempts`](runs.md#best-of-n-attempts) invocation: `id`, `cwd`, `attempts_requested`, `attempts_run`, `winner`, `winner_branch`, `stop_reason`, `total_cost_usd`, `total_wall_time_ms`, `trace_id`, `started_at`, `ended_at`; each attempt's own `runs` row carries `attempts_id` and `attempt`), and `schema_migrations`. Timestamps are Unix milliseconds.

```sh
sqlite3 .tinyagent/tinyagent.db \
  "SELECT tool, count(*), avg(duration_ms) FROM tool_calls GROUP BY tool"
```

At the default `level: metadata` no prompt, message, argument, or output bodies are stored: those fields are replaced with their lengths, and the task is stored only as a SHA-256 hash and length. `level: full` stores bodies, after redacting values of environment variables named `*_KEY`, `*_TOKEN`, `*_SECRET`, or `*_PASSWORD`, secrets set in MCP or hook `env`, and common token shapes; payloads over `max_payload_bytes` are truncated. Old runs are pruned when a run starts (`max_days`, `max_runs`; `0` keeps everything). Database errors never fail a run: tinyagent prints one warning and continues without the log. The trajectory JSON (`-o`) is unchanged and remains the resume format.

### OpenTelemetry export

`--otel` (or `otel.enabled: true` in config) sends each invocation to an OpenTelemetry backend (an OpenTelemetry Collector, Jaeger, Grafana Tempo, Honeycomb, Datadog, ...) as one trace over OTLP/HTTP. It is off by default, and the standard `OTEL_*` variables alone never turn it on, so a CI job that sets them for other tools exports nothing until you opt in. Spans are built from the same events as `--json` and `--log-db`, so the three always agree.

| Span | Kind | Attributes |
|---|---|---|
| `invoke_agent tinyagent` (root) | INTERNAL | `gen_ai.operation.name=invoke_agent`, `gen_ai.agent.name`, `gen_ai.request.model`, `gen_ai.provider.name`, `tinyagent.model`, `tinyagent.status`, `tinyagent.steps`, `gen_ai.usage.input_tokens`/`output_tokens`/`cache_read.input_tokens`/`cache_creation.input_tokens`, `tinyagent.cost_usd`, `tinyagent.duration_ms` (the `result` event's values), `tinyagent.resumed`, `tinyagent.read_only`, `tinyagent.version` |
| `tinyagent attempts` (root of an [`--attempts`](runs.md#best-of-n-attempts) run) | INTERNAL | `tinyagent.attempts.id`, `tinyagent.attempts.count`, `tinyagent.attempts.run`, `tinyagent.attempts.winner`, `tinyagent.attempts.stop_reason`, `tinyagent.cost_usd`, `tinyagent.duration_ms`; ERROR when no attempt passed. Each attempt's `invoke_agent tinyagent` span is its child and adds `tinyagent.attempts.id`, `tinyagent.attempt.index` and `tinyagent.attempts.count` |
| `chat {model}` | CLIENT | `gen_ai.operation.name=chat`, `gen_ai.request.model`, `gen_ai.response.model`, `gen_ai.provider.name`, per-request token usage, `tinyagent.step`; `provider.retry` and `model.fallback` span events; `error.type=model_error` for a failed request |
| `execute_tool {name}` | INTERNAL | `gen_ai.operation.name=execute_tool`, `gen_ai.tool.name`, `gen_ai.tool.call.id` (the `tool.call` `id`), `tinyagent.output_bytes`, `tinyagent.redactions`, `error.type=tool_error` when the result is an error |
| `hook {event}` | INTERNAL | `tinyagent.hook.event`, `tinyagent.hook.decision`, `gen_ai.tool.call.id`; a child of its tool's span (`stop` hooks: of the root) |
| `invoke_agent delegate` | INTERNAL | a [subagent](long-runs.md#subagents), child of its `execute_tool delegate` span; the subagent's model and tool spans nest under it |
| `invoke_agent worker` | INTERNAL | a [worker](long-runs.md#workers), child of its `execute_tool worker` span (concurrent workers each get their own); `tinyagent.subagent.id`, `tinyagent.subagent.kind=worker`, `tinyagent.worker.branch`, `tinyagent.worker.base_commit`, and when it finishes `tinyagent.worker.commit` (only if it changed files) and `tinyagent.worker.files_changed`; the worktree path is not exported. `apply_worker` adds a `worker.applied` span event to its tool span |

Other events (`compaction`, `checkpoint.saved`, `plan.updated`, `sandbox.network_denied`, `web_fetch`, `warning`, `error`, ...) become span events on the open tool span or the agent span, with the fields `--log-db` stores at its default level. The root span's status is ERROR for every final status except `Submitted`, `Completed` and `NeedsInput`. Each invocation (including `--resume` and follow-ups, and each [ACP](integrations.md#editor-integration-acp) prompt) is its own trace. When `TRACEPARENT` (and `TRACESTATE`) holds a valid W3C trace context, the root span becomes its child, so the run shows up inside your CI pipeline's trace; a malformed value is ignored with one warning.

**Content is off by default**: no task, assistant text, tool arguments or tool output is exported, only the metadata above (span-event body fields become `<field>_len`). `otel.content: true` adds `tinyagent.task`, `tinyagent.assistant.text`, `gen_ai.tool.call.arguments` and `gen_ai.tool.call.result`, each first redacted like the event log (secret-named environment variables, configured secrets, the export headers and token shapes become `[REDACTED]`) and cut to `max_attribute_bytes`.

```yaml
otel:
  enabled: false            # or --otel
  endpoint: ""              # OTLP/HTTP base URL; /v1/traces is appended
  protocol: http/protobuf   # or http/json; grpc is not supported (exit 2)
  headers_env: []           # names of env vars holding k=v,k2=v2 header lists
  timeout: 10s              # bound on each export, retries included, and on the final flush
  service_name: tinyagent   # resource service.name
  content: false            # add redacted prompt, assistant, argument and output text
  max_attribute_bytes: 4096 # cap per content attribute
```

Empty settings fall back to the standard variables: the endpoint to `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` (used as is), then `OTEL_EXPORTER_OTLP_ENDPOINT` (with `/v1/traces` appended), then `http://localhost:4318/v1/traces`; the protocol to `OTEL_EXPORTER_OTLP_TRACES_PROTOCOL`, then `OTEL_EXPORTER_OTLP_PROTOCOL`; the timeout to `OTEL_EXPORTER_OTLP_TRACES_TIMEOUT`, then `OTEL_EXPORTER_OTLP_TIMEOUT` (milliseconds); the service name to `OTEL_SERVICE_NAME`, then `service.name` in `OTEL_RESOURCE_ATTRIBUTES` (whose other entries become resource attributes). Headers come from `OTEL_EXPORTER_OTLP_HEADERS`, `OTEL_EXPORTER_OTLP_TRACES_HEADERS` and the `headers_env` variables, never from the config file, and their values are never printed or recorded. Bad settings (unknown keys, an invalid `timeout`, `grpc`, a malformed header list, an unset `headers_env` variable) exit 2 before any model request. `-v` prints the endpoint host and protocol.

Spans are buffered in memory and sent in batches, at the latest when the run ends, including after Ctrl-C. `429`, `502`, `503`, `504` and network errors are retried with jittered exponential back-off (honoring `Retry-After`) within `timeout`. Export problems never change the exit code, stdout, the `result` event or the trajectory: they produce exactly one `warning` (stderr, and a `warning` event with `--json`) giving the number of spans not delivered, and the run waits at most `timeout` at exit. The exporter runs in tinyagent's own process, so `--sandbox` network rules don't apply to it. Use `https` for any endpoint that is not on localhost.

To try it locally, run an OpenTelemetry Collector (or Jaeger, which accepts OTLP) on port 4318 and point tinyagent at it:

```sh
docker run --rm -p 16686:16686 -p 4318:4318 jaegertracing/all-in-one
tinyagent --otel -t "fix the failing test"     # then open http://localhost:16686
```

Span and attribute names follow the OpenTelemetry GenAI semantic conventions, which are still in Development status; names may change in a later release to follow them.

### Streaming

Assistant text streams as it arrives (OpenAI chat and Responses, Anthropic, Codex, Ollama and configured providers; ACP sends `agent_message_chunk`). With `--json`, each chunk is an `assistant.delta` event (`{"text": ...}`) that comes before the step's `model.request` and `assistant.message`; `assistant.message` stays the full, authoritative text. Deltas go only to the `--json` stream, not to `--log-db` or `--otel`. `assistant.delta` is an optional event type, so `schema_version` stays 1. `--no-stream` waits for whole responses and sends no deltas.

If a server ignores `stream: true` and returns a plain JSON body, the OpenAI chat, Responses and Anthropic clients read it as a non-streamed response: the text is reported once and tool calls are kept.
