#!/bin/sh
# Install the latest tinyagent release:
#   curl -fsSL https://raw.githubusercontent.com/vicgupta/tinyagent/main/install.sh | sh
#
# Environment:
#   TINYAGENT_VERSION      release tag to install (default: latest)
#   TINYAGENT_INSTALL_DIR  target directory (default: /usr/local/bin if writable, else ~/.local/bin)
set -eu

REPO=vicgupta/tinyagent
VERSION=${TINYAGENT_VERSION:-latest}

fail() { echo "install: $*" >&2; exit 1; }

case $(uname -s) in
	Darwin) os=darwin ;;
	Linux) os=linux ;;
	*) fail "unsupported OS $(uname -s); download a binary from https://github.com/$REPO/releases" ;;
esac
case $(uname -m) in
	x86_64 | amd64) arch=amd64 ;;
	arm64 | aarch64) arch=arm64 ;;
	*) fail "unsupported CPU $(uname -m)" ;;
esac
asset=tinyagent-$os-$arch

if [ "$VERSION" = latest ]; then
	base=https://github.com/$REPO/releases/latest/download
else
	base=https://github.com/$REPO/releases/download/$VERSION
fi

if command -v curl >/dev/null 2>&1; then
	fetch() { curl -fsSL -o "$2" "$1"; }
elif command -v wget >/dev/null 2>&1; then
	fetch() { wget -qO "$2" "$1"; }
else
	fail "need curl or wget"
fi

if command -v sha256sum >/dev/null 2>&1; then
	sha256() { sha256sum "$1" | cut -d' ' -f1; }
elif command -v shasum >/dev/null 2>&1; then
	sha256() { shasum -a 256 "$1" | cut -d' ' -f1; }
else
	fail "need sha256sum or shasum"
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "Downloading $asset ($VERSION)..."
fetch "$base/$asset" "$tmp/tinyagent" || fail "download failed: $base/$asset"
fetch "$base/SHA256SUMS" "$tmp/SHA256SUMS" || fail "download failed: $base/SHA256SUMS"

want=$(awk -v f="$asset" '$2 == f { print $1 }' "$tmp/SHA256SUMS")
[ -n "$want" ] || fail "no checksum for $asset in SHA256SUMS"
[ "$(sha256 "$tmp/tinyagent")" = "$want" ] || fail "checksum mismatch for $asset; not installing"

dir=${TINYAGENT_INSTALL_DIR:-}
if [ -z "$dir" ]; then
	if [ -w /usr/local/bin ]; then
		dir=/usr/local/bin
	else
		dir=$HOME/.local/bin
	fi
fi
mkdir -p "$dir"
chmod +x "$tmp/tinyagent"
mv "$tmp/tinyagent" "$dir/tinyagent"

echo "Installed $("$dir/tinyagent" --version) to $dir/tinyagent"
case ":$PATH:" in
	*":$dir:"*) ;;
	*) echo "Note: $dir is not on your PATH. Add it with: export PATH=\"$dir:\$PATH\"" ;;
esac
