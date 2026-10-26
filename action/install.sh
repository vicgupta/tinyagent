#!/usr/bin/env bash
# Install step of the tinyagent GitHub Action (action.yml): resolve the
# tinyagent version, install that release with ../install.sh (which checks
# SHA256SUMS) into a fresh directory under RUNNER_TEMP, and set the `path`
# output. Inputs arrive as environment variables only.
set -euo pipefail

fail() {
	echo "::error::tinyagent action: $*"
	exit 1
}

if [ -n "$INPUT_TINYAGENT_PATH" ]; then
	[ -z "$INPUT_VERSION" ] || fail "set version or tinyagent-path, not both"
	[ -f "$INPUT_TINYAGENT_PATH" ] && [ -x "$INPUT_TINYAGENT_PATH" ] ||
		fail "tinyagent-path is not an executable file"
	bin=$(cd "$(dirname "$INPUT_TINYAGENT_PATH")" && pwd)/$(basename "$INPUT_TINYAGENT_PATH")
	echo "Using $("$bin" --version) at $bin"
	echo "path=$bin" >>"$GITHUB_OUTPUT"
	exit 0
fi

case $RUNNER_OS in
	Linux | macOS) ;;
	*) fail "unsupported runner OS $RUNNER_OS; the action supports Linux and macOS runners" ;;
esac
case $RUNNER_ARCH in
	X64 | ARM64) ;;
	*) fail "unsupported runner architecture $RUNNER_ARCH; the action supports X64 and ARM64" ;;
esac

semver='^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'
version=$INPUT_VERSION
if [ -z "$version" ]; then
	[[ $ACTION_REF =~ $semver ]] ||
		fail "the action ref is not a vX.Y.Z tag, so set the version input (vX.Y.Z or latest) or tinyagent-path"
	version=$ACTION_REF
elif [ "$version" != latest ] && ! [[ $version =~ $semver ]]; then
	fail "version must be vX.Y.Z or latest"
fi

dir=$(mktemp -d "${RUNNER_TEMP:-/tmp}/tinyagent-bin.XXXXXX")
TINYAGENT_VERSION=$version TINYAGENT_INSTALL_DIR=$dir sh "$GITHUB_ACTION_PATH/install.sh" ||
	fail "installing tinyagent $version failed (see the message above)"
echo "path=$dir/tinyagent" >>"$GITHUB_OUTPUT"
