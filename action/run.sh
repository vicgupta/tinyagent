#!/usr/bin/env bash
# Run step of the tinyagent GitHub Action (action.yml): build tinyagent's argv
# from the inputs, run it with the task on stdin, set the step outputs from
# the final `result` event, and optionally append the transcript to the job
# summary. Inputs arrive as environment variables only and are never echoed
# or evaluated.
set -euo pipefail

usage() {
	echo "::error::tinyagent action: $*"
	echo "exit-code=2" >>"$GITHUB_OUTPUT"
	exit 2
}

bool() { # bool <input name> <value>
	case $2 in
		true | false) ;;
		*) usage "$1 must be true or false" ;;
	esac
}
bool read-only "$INPUT_READ_ONLY"
bool sandbox "$INPUT_SANDBOX"
bool job-summary "$INPUT_JOB_SUMMARY"
bool fail-on-error "$INPUT_FAIL_ON_ERROR"
command -v jq >/dev/null 2>&1 || usage "jq is required to read tinyagent's result; install it before this step"

if [ -n "$INPUT_TASK" ] && [ -n "$INPUT_TASK_FILE" ]; then
	usage "set task or task-file, not both"
fi
review=
[ -z "$INPUT_REVIEW_BASE$INPUT_REVIEW_COMMIT" ] || review=1

out=$(mktemp -d "${RUNNER_TEMP:-/tmp}/tinyagent.XXXXXX")
traj=$out/trajectory.json
events=$out/events.jsonl
args=(--json -o "$traj")
[ -z "$INPUT_MODEL" ] || args+=(-m "$INPUT_MODEL")
[ -z "$INPUT_PROFILE" ] || args+=(--profile "$INPUT_PROFILE")
[ -z "$INPUT_CONFIG" ] || args+=(-c "$INPUT_CONFIG")
[ -z "$INPUT_WORKING_DIRECTORY" ] || args+=(--cwd "$INPUT_WORKING_DIRECTORY")
[ -z "$INPUT_REVIEW_BASE" ] || args+=(--review-base "$INPUT_REVIEW_BASE")
[ -z "$INPUT_REVIEW_COMMIT" ] || args+=(--review-commit "$INPUT_REVIEW_COMMIT")
[ "$INPUT_READ_ONLY" = false ] || args+=(--read-only)
[ "$INPUT_SANDBOX" = false ] || args+=(--sandbox)
[ -z "$INPUT_OUTPUT_SCHEMA" ] || args+=(--output-schema "$INPUT_OUTPUT_SCHEMA")
while IFS= read -r line || [ -n "$line" ]; do
	[ -z "$line" ] || args+=("$line")
done <<<"$INPUT_ARGS"

# The task goes to tinyagent on stdin (`-t -`), never through the shell.
if [ -n "$INPUT_TASK_FILE" ]; then
	[ -f "$INPUT_TASK_FILE" ] || usage "task-file is not a file"
	args+=(-t -)
	exec 3<"$INPUT_TASK_FILE"
elif [ -n "$INPUT_TASK" ]; then
	args+=(-t -)
	exec 3< <(printf '%s' "$INPUT_TASK")
elif [ -z "$review" ]; then
	usage "set task, task-file, review-base or review-commit"
else
	exec 3</dev/null
fi

{
	echo "trajectory=$traj"
	echo "events=$events"
} >>"$GITHUB_OUTPUT"

# Stop the runner from acting on workflow commands (::add-mask::, ::error::
# ...) in tinyagent's model-influenced log output.
token=$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')
echo "::stop-commands::$token"
code=0
"$TINYAGENT_BIN" "${args[@]}" <&3 >"$events" || code=$?
exec 3<&-

# set_output writes a value of any shape to GITHUB_OUTPUT behind a random
# delimiter, and refuses a value that contains the delimiter.
set_output() { # set_output <name> <file>
	local delim
	delim=ghadelimiter_$(od -An -N16 -tx1 /dev/urandom | tr -d ' \n')
	if grep -qF "$delim" "$2"; then
		echo "::$token::"
		usage "the $1 output contains its random delimiter; refusing to set it"
	fi
	{
		printf '%s<<%s\n' "$1" "$delim"
		cat "$2"
		printf '\n%s\n' "$delim"
	} >>"$GITHUB_OUTPUT"
}

result=$out/result.json
{ jq -c 'select(.type? == "result")' "$events" 2>/dev/null || true; } | tail -n 1 >"$result"
value=$out/value
for field in status:'.status // ""' \
	submission:'.submission // ""' \
	structured-output:'if .status == "Submitted" and has("structured_output") then .structured_output | tojson else "" end' \
	steps:'.steps // ""' \
	cost-usd:'.cost_usd // ""'; do
	if [ -s "$result" ]; then
		jq -j "${field#*:}" "$result" >"$value"
	else
		: >"$value"
	fi
	set_output "${field%%:*}" "$value"
done
echo "exit-code=$code" >>"$GITHUB_OUTPUT"

if [ "$INPUT_JOB_SUMMARY" = true ] && [ -s "$traj" ] && [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
	md=$out/transcript.md
	if "$TINYAGENT_BIN" --transcript "$traj" >"$md"; then
		size=$(wc -c <"$md" | tr -d ' ')
		if [ "$size" -le 1048576 ]; then
			cat "$md" >>"$GITHUB_STEP_SUMMARY"
		else
			echo "tinyagent: the transcript ($size bytes) exceeds the 1 MiB job summary limit and was not added; the trajectory is $traj." >>"$GITHUB_STEP_SUMMARY"
		fi
	else
		echo "tinyagent: rendering the transcript failed." >>"$GITHUB_STEP_SUMMARY"
	fi
fi
echo "::$token::"

if [ "$code" -eq 2 ] || { [ "$code" -ne 0 ] && [ "$INPUT_FAIL_ON_ERROR" = true ]; }; then
	echo "::error::tinyagent exited with status $code"
	exit "$code"
fi
