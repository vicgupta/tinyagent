# Troubleshooting

### Malformed tool calls

A tool call is a *format error* when its arguments aren't valid JSON or not a JSON object, its tool name isn't offered, or its arguments miss a field the tool's schema marks `required` or give a field the wrong JSON type (integers may still be sent as numeric strings). The tool isn't run; the model gets a short correction instead, such as `format error: read_file is missing required field "path". Call one of: read_file, ... with arguments matching its schema.`, and the run continues. Errors from a well-formed call that runs (a command exiting 1, a missing file) are not format errors, and in `--read-only` mode a non-read-only built-in keeps its read-only denial.

A step whose tool calls are all format errors counts toward a consecutive limit; any step with at least one well-formed call resets it. After `--max-format-errors` (config `max_format_errors`, default 3) such steps in a row, the run ends with `RepeatedFormatError`, an empty submission, and a non-zero exit. Format-error steps are ordinary model calls, so they count toward `--step-limit` and `--cost-limit`, which are checked first. A response with no tool calls still ends the run as `Completed`.

The summary line shows the count (`..., 3 format errors: <last reason>`). With `--json`, a malformed call's `tool.result` event has `"format_error": true` and the `result` event has `format_errors`. With `-o`, the trajectory records `format_errors`, `consecutive_format_errors`, `format_error_messages` (indexes of the correction messages) and `last_format_error`, and keeps the original arguments. The consecutive count is restored on `--resume`, and a `RepeatedFormatError` checkpoint can be resumed.

### Output truncation

When a response stops because it hit the output-token limit (Anthropic `stop_reason` `max_tokens` or `model_context_window_exceeded`, Chat Completions `finish_reason` `length`, Responses/Codex `incomplete` with reason `max_output_tokens`), tinyagent treats it as cut off rather than finished. None of its tool calls run, not even ones that look complete, and no hooks run for them; each gets the result `error: [not run: your response was cut off at the output-token limit ...]`, and the model is asked to continue in smaller pieces (for example, write a large file in several edits). A cut-off response never ends the run as `Submitted` or `Completed`, and does not count as a format error or an invalid structured result. Invalid partial tool-call JSON is replaced by `{}` when the conversation is sent back to the provider.

After `max_truncations` (default 3) cut-off responses in a row, the run ends with `OutputTruncated` and exits 3; any complete response resets the count. Raise the budget with `--max-output-tokens` (config `max_output_tokens`; sent as `max_tokens` on Anthropic and Ollama, `max_completion_tokens` on OpenAI/OpenRouter Chat Completions, `max_output_tokens` on the OpenAI Responses transport; `codex` exits 2). The budget may be changed on `--resume`, so an `OutputTruncated` checkpoint can be resumed with a larger one.

A warning goes to stderr, the summary line shows `..., N truncated responses`, `--json` (and `--log-db`) emit a `model.truncated` event with `step`, `reason`, `max_output_tokens`, `tool_calls_dropped` and `consecutive`, the dropped calls' `tool.result` events have `"truncated": true`, and the `result` event and trajectory record `truncations` (trajectories also `consecutive_truncations` and `max_output_tokens`).

## Troubleshooting

When a run fails on a common setup problem, tinyagent prints a `hint:` line under the error. The error itself is kept, and hints name environment variables without printing their values.

A mistyped flag exits 2 with one line naming up to three close flags, such as `error: unknown flag --sandbxo; did you mean --sandbox? (see tinyagent --help)`, instead of the full usage.

To check your setup before a run, use `tinyagent --check` with the same `-m`, `-c`, `--cwd`, fallback and limit flags you'd pass to a run. It sends no model request and writes nothing. It prints which config file, model, provider, fallbacks, working directory and limits a run would use, and where each came from, then `ok` (exit 0). A problem a run would hit, such as an unknown provider, a bad config key or a missing API key, is printed the same way a run prints it, with its `hint:`, and exits 2. Key values are never printed, only whether the variable is set.

```
$ tinyagent --check -m anthropic/claude-sonnet-4
config:    none (built-in defaults)
model:     anthropic/claude-sonnet-4 (from -m)
provider:  anthropic, ANTHROPIC_API_KEY is set
fallback:  none
cwd:       /home/me/project (default)
limits:    steps unlimited, cost unlimited, time unlimited
ok
```

| Symptom | Fix |
|---|---|
| `HTTP 401` or `403` from openai, openrouter or anthropic | Set the key: `export OPENAI_API_KEY=...` (or `OPENROUTER_API_KEY`, `ANTHROPIC_API_KEY`). If it's already set, check that it's current. |
| `HTTP 404` from openai, openrouter or anthropic | Check the model name after the `/`. |
| `connection refused` with `ollama/...` | Start Ollama with `ollama serve`, or set `OLLAMA_HOST` to where it runs. |
| `HTTP 404` with `ollama/...` | Pull the model: `ollama pull <model>`. |
| `every model in the fallback chain failed` | Each listed model used up its retries; the message names each one and why. Check the providers' status pages, or add a model from another provider to `fallback_models`. |
| An editor says the ACP agent failed to start or exited | Use the full path to `tinyagent` in the agent's `command`, put the API key in the agent's `env` (editors don't read your shell profile), and check the editor's agent log, which shows tinyagent's stderr. |
| `note: user config ... is no longer read` | tinyagent now reads only `-c` or `./tinyagent.yaml` in `--cwd`. Run `tinyagent init --migrate` in the project to copy the old file there, or pass it with `-c`. See [Config](config.md#config). |
| `warning: ignoring mcp_servers, hooks, ... from ./tinyagent.yaml` | A project config can't launch programs or loosen security settings by default. Read the file, then pass `--trust-config` if you trust the repository, or move those settings to a `-c` file. See [Trusting a project config](config.md#trusting-a-project-config). |
| Settings in `tinyagent.yaml` seem ignored | `--check` prints the `config:` line; the file must be in `--cwd` itself (parent directories aren't searched), and flags, `$AGENT_MODEL` and `--profile` outrank it. |
| `codex: no login` | Run `codex login` and choose ChatGPT. |
| `model is not supported` with `codex/...` | Use a model the Codex CLI's model picker shows for your plan. |
