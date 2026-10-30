# Providers and models

### ChatGPT subscription (Codex)

The `codex` provider uses your ChatGPT subscription instead of an API key. Sign in once with the [Codex CLI](https://github.com/openai/codex) (`codex login`, choose ChatGPT). tinyagent reads the tokens it saves in `$CODEX_HOME/auth.json` (default `~/.codex/auth.json`), refreshes them when they expire and writes them back, so Codex stays signed in too. Requests go to the Codex backend's Responses API. Usage counts against your plan's limits rather than costing money, so cost shows as $0 and `--cost-limit` has no effect. Only models your plan offers in Codex work (the ones the Codex CLI's model picker shows); others fail with "model is not supported".

### Named providers

`providers` defines extra endpoints you can select with `-m <id>/<model>`, such as an internal gateway, a self-hosted server or another OpenAI-compatible service, without setting `OPENAI_BASE_URL`. Everything after the first `/` is sent to the endpoint as the model name, so `-m gateway/vendor/model-x` sends `vendor/model-x`.

```yaml
providers:
  - id: gateway
    api: chat                  # chat (POST <base_url>/chat/completions), responses (POST <base_url>/responses), or anthropic (POST <base_url>/v1/messages)
    base_url: https://llm.internal.example.com/v1
    api_key_env: GATEWAY_API_KEY   # optional; sent as Authorization: Bearer (x-api-key for anthropic)
    headers: {X-Team: core}        # optional static, non-secret headers
    env_headers: {X-Tenant: GATEWAY_TENANT}   # optional headers whose values come from environment variables
    models:                        # optional per-model metadata
      vendor/model-x:
        input_price_per_mtok: 2
        output_price_per_mtok: 8
        cache_read_multiplier: 0.1 # optional; price of cached input relative to input
        context_window: 128000     # used for compaction
```

- `id` must be unique, contain no `/`, and not be a built-in provider (`openai`, `openrouter`, `anthropic`, `ollama`, `codex`); built-in names always mean the built-in provider. `api` must be `chat`, `responses` or `anthropic`, and `base_url` an absolute `http` or `https` URL without credentials. Prices must be non-negative and `context_window` positive. A config that breaks these rules is rejected before any request, naming the file and the `providers[i]` entry, with exit status 2. A misspelled key is rejected like any other unknown config key.
- Secrets stay in the environment. `api_key_env` and `env_headers` name variables; tinyagent reads their values at startup and exits 2, before any request, if one is unset or empty. Static `headers` may not carry credentials (`Authorization`, `x-api-key`, `api-key`). Key and header values never appear in stderr, `-v`, `--json`, the trajectory or the event log database, which also redacts them if they show up in tool output. Without `api_key_env`, no key is sent.
- Metadata matches the model name exactly. Top-level `input_price_per_mtok`, `output_price_per_mtok` and `context_window` still win over provider metadata. A model with no price behaves like an unknown built-in model: it costs $0 and, with a positive `--cost-limit`, prints the "no pricing available" warning. A `cache_read_multiplier` without prices refines the built-in price for that model name, if there is one.
- `-v` prints `provider: <id> (api <api>, base_url <url>)`. A plain `http` base URL to a host other than localhost or a loopback address prints a warning, because requests and any key travel unencrypted. `run.started` in `--json` and the trajectory record `provider_api` and `provider_base_url` (never headers or keys); built-in providers leave them out.
- `--resume` of a trajectory that used a named provider needs the same provider defined with the same `api` and `base_url`; otherwise it stops before any request and tells you what changed.
- `openai_api`, `--openai-api` and `OPENAI_BASE_URL` apply only to the built-in `openai` provider. Configs with `providers` fail to load on tinyagent versions without this feature, since older versions reject the unknown key.

### OpenAI transport

The `openai` provider talks to OpenAI over the Responses API (`POST /v1/responses`) or Chat Completions (`POST /v1/chat/completions`). `openai_api` (config) or `--openai-api` (flag, which wins) picks one:

| Value | Transport |
|---|---|
| `auto` (default) | Responses when `OPENAI_BASE_URL` is unset or is `https://api.openai.com/v1` (a trailing `/` is ignored); Chat Completions for any other base URL, so OpenAI-compatible servers keep working |
| `responses` | Always the Responses API, at `OPENAI_BASE_URL` if set |
| `chat` | Always Chat Completions, as before this setting existed |

Newer OpenAI models only support tool calls together with reasoning on the Responses API. On Responses, tinyagent sends `store: false` (nothing is kept server-side), a `prompt_cache_key` stable for the run, flat function tools, and `reasoning: {effort}` when an effort is set. Reasoning items that come back with `encrypted_content` are kept in the assistant message (and the trajectory) and sent back before that turn's tool calls, so the model keeps its reasoning across tool calls. Cached input tokens (`input_tokens_details.cached_tokens`) are billed at the cache-read price. Streaming, `--no-stream`, retries, compaction, `--output-schema`, and `--resume` work the same on either transport.

The setting only applies to the `openai` provider; `openrouter`, `ollama`, and the others ignore it. An unknown value exits 2 before any request, and `-v` prints the resolved transport. The resolved transport is recorded as `openai_api` in trajectories and in `run.started`, and `--resume` keeps it, since reasoning items only replay on the transport that produced them (a different `--openai-api` is refused, like `--model`); a checkpoint from before this setting resolves like a new run. If a Chat Completions request fails with a 400 that asks for the Responses API, tinyagent suggests `openai_api: responses`.

### Reasoning

Reasoning models return state that must be sent back during tool use. tinyagent keeps it for the run and replays it unchanged:

- `anthropic`: when a response contains `thinking` or `redacted_thinking` blocks (thinking is on by default on current Claude models), all of its content blocks, including signatures, are sent back complete and in their original order in the same assistant message. Streaming and `--no-stream` behave the same.
- `codex`: Responses API `reasoning` items that carry `encrypted_content` are sent back in `input` right before the function calls and text of the same response. Items without `encrypted_content` are dropped, because with `store: false` they cannot be referenced.

`reasoning_effort` (config) or `--reasoning-effort` (flag, which wins) chooses how much the model reasons. When it is unset, nothing is added to requests and the model's default applies. When set, it maps to:

| Provider | Request field |
|---|---|
| `codex` | `reasoning.effort` |
| `openai` | `reasoning.effort` on the Responses API; `reasoning_effort` on Chat Completions (see [OpenAI transport](#openai-transport)) |
| `openrouter` | `reasoning.effort` |
| `anthropic` | `output_config.effort` (thinking is not turned on explicitly) |
| `ollama` | not supported; exits 2 |

An unknown value exits 2 before any request. Which values a model accepts varies; a provider rejection is reported like any other model error and is not retried. The effort is recorded as `reasoning_effort` in trajectories and is kept on `--resume` (a different `--reasoning-effort` is refused, like `--model`). Higher effort means more output tokens, which providers bill and which count toward `--cost-limit`; on Anthropic, thinking also counts toward the request's `max_tokens` (8192 unless `--max-output-tokens` is set). Changing the effort invalidates provider prompt caches.

Reasoning state is stored in each assistant message's `extra` (`anthropic_content`, and `codex_reasoning` for Responses API reasoning items from both `codex` and `openai`) in trajectories and checkpoints, so `--resume` replays it. It may include thinking text and encrypted reasoning blobs; treat trajectory files as private. Reasoning state is not shown in the terminal or in `--json` events.

### Model fallback

A fallback chain lets a run survive a provider outage. It is off by default. List up to 3 models in `fallback_models` (config) or with repeated `--fallback-model` flags, which replace the config list; `--no-fallback` clears it:

```yaml
model: openai/gpt-5
fallback_models: [anthropic/claude-sonnet-4-5, ollama/qwen3-coder]
```

When a model request still fails after its retries with `429`, a `5xx` status or a network error, tinyagent switches to the next model in the list, resends the same conversation, and stays on that model for the rest of the invocation. Every other error (`400`, `401`, `403`, `404`, context overflow, a failure in the middle of a streamed response, an interrupted run) is reported as before and does not switch models. When the last model also fails this way, the run ends with `Error` and a message naming every model tried and why.

Each switch prints `model fallback: <from> -> <to> (<reason>) at step <n>` on stderr, emits a [`model.fallback`](json-output.md#machine-readable-output) event, and is recorded in trajectories under `model_switches` (`step`, `from`, `to`, `reason`), along with `fallback_models` and `active_model` (the model serving the run when it was saved). `model` stays the primary model, and with a chain configured each assistant message records the model that produced it in `extra.model`. The final summary line notes the switch, for example `, 1 model fallback (now anthropic/claude-sonnet-4-5)`.

Every entry is checked before the first request: an unknown provider, a missing API key, an entry equal to the primary model, a duplicate, or more than 3 entries exits 2. `reasoning_effort` and `max_output_tokens` apply to a fallback only when it uses the same provider as the primary. Provider-specific reasoning state (Anthropic thinking blocks, encrypted Responses reasoning) is never sent to a different provider. Prices come from each model's own pricing, and `--cost-limit` warns about every unpriced model in the chain.

**Your conversation goes to the fallback provider.** After a switch, the whole history so far (task, files the model read, tool output) is sent to the next provider in the list, so only list providers you are willing to share that data with.

`--resume` uses the chain from the current config and flags, not the saved one, and the resumed run starts on the primary model. Subagents (`delegate`) don't fall back. Older tinyagent versions reject a config file containing the `fallback_models` key.
