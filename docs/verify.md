# Verify before stop

An opt-in guard that keeps a run from finishing on untested changes: when files changed since the last successful test or build command, the model is sent back to verify before its final answer is accepted. Off by default.

```bash
tinyagent --verify-on-stop -t "fix the failing test in pkg/foo"
tinyagent --verify-on-stop --verify-command "go test ./..." --max-verify-reprompts 3 -t "..."
```

| Flag | Config | Default | |
|---|---|---|---|
| `--verify-on-stop` | `verify_on_stop.enabled` | off | Turn the guard on (`--verify-on-stop=false` turns it off) |
| `--verify-command <cmd>` | `verify_on_stop.commands` | common test/build commands | A `bash` command that counts as verification; repeatable, replaces the config list |
| | `verify_on_stop.patterns` | none | Regular expressions; a matching `bash` command counts as verification |
| `--max-verify-reprompts <n>` | `verify_on_stop.max_reprompts` | 2 | How many times the model is sent back before the run is marked unverified (1 to 10) |

## How it works

### Verify before stop

With `verify_on_stop.enabled` (or `--verify-on-stop`), a run that changed files does not finish on an unverified final answer. A successful `write_file`, `edit_file` or `apply_patch` call, or a `bash` command that is not a verification command, marks changes pending. A `bash` command counts as verification when it is one of `commands` (alone or followed by arguments) or matches one of `patterns`; when both lists are empty, common test and build commands count (`go test`, `go build`, `go vet`, `cargo test`, `npm test`, `make`, `pytest` and similar, at the start of the command or after `&&`, `;` or `||`). A verification that exits 0 clears the pending changes; one that fails leaves them pending, and the model sees its output as a normal tool result.

When the model gives a final answer (with or without the completion marker) while changes are pending, it gets a fixed user message asking it to verify, naming the configured commands, and the run continues as normal steps, so step, cost and time limits still apply. Tool calls in a submitting message are not run and get an error result saying so. After `max_reprompts` re-prompts, or straight away when `bash` cannot run (denied by the permission policy, a hook or lockdown, or unavailable as in read-only mode), the run finishes with its normal status and is marked unverified: the summary line ends with `, unverified changes`, the `--json` result has `"unverified": true`, and the trajectory has `unverified: true`. `verify_reprompts` counts the re-prompts in both. The guard runs before stop hooks, does not apply to subagent runs or `--output-schema` runs, and keeps its state across `--resume`.

## Where it shows up

- **Summary line:** ends with `, unverified changes`.
- **`--json`:** `verify.reprompt` (`reprompt`, `max_reprompts`) each time the model is sent back, `verify.unverified` (`reason`, `reprompts`) when the run ends unverified, and the `result` event's `unverified` and `verify_reprompts` fields; see [JSON output](json-output.md#machine-readable-output).
- **Trajectory (`-o`):** `unverified: true` and `verify_reprompts`.

The config block is shown in the [example config](config.md#config).
