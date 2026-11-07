# Install

tinyagent is one static binary. These are the supported ways to install it. The short form is also in the [README](../README.md#install).

## macOS and Linux

```bash
curl -fsSL https://raw.githubusercontent.com/vicgupta/tinyagent/main/install.sh | sh
```

The script downloads the latest release for your OS and CPU, checks it against `SHA256SUMS`, and installs it to `/usr/local/bin` when that directory is writable, otherwise to `~/.local/bin`.

```bash
# Install a specific version
curl -fsSL https://raw.githubusercontent.com/vicgupta/tinyagent/main/install.sh | TINYAGENT_VERSION=v0.1.1 sh

# Install somewhere else
curl -fsSL https://raw.githubusercontent.com/vicgupta/tinyagent/main/install.sh | TINYAGENT_INSTALL_DIR=~/bin sh
```

## Windows

### WinGet

```powershell
winget install VicGupta.TinyAgent
```

The package identifier is `VicGupta.TinyAgent`. WinGet matches the identifier without regard to case, so `winget install vicgupta.tinyagent` selects the same package. The canonical form to use in docs and scripts is `VicGupta.TinyAgent`.

WinGet installs a zip and puts a portable `tinyagent` command on your PATH. The GitHub release assets it uses are:

| Asset | Architecture | Archive contents |
|---|---|---|
| `tinyagent-windows-amd64.zip` | x64 | `tinyagent.exe` |
| `tinyagent-windows-arm64.zip` | arm64 | `tinyagent.exe` |

Each zip has that one file at the archive root. The WinGet manifest declares installer type `zip`, nested installer type `portable`, and portable command alias `tinyagent`.

The package appears in WinGet after Microsoft merges the first submission to the [Windows Package Manager community repository](https://github.com/microsoft/winget-pkgs). Publishing steps for maintainers are in [Releasing](release.md).

### Direct download

Download `tinyagent-windows-amd64.zip` (or `tinyagent-windows-arm64.zip`) from the [latest release](https://github.com/vicgupta/tinyagent/releases/latest), extract `tinyagent.exe`, and put it in a folder on your `PATH`. The release does not include a bare Windows `.exe`.

`tinyagent --update` on Windows downloads that zip, checks the zip against `SHA256SUMS`, extracts `tinyagent.exe`, and replaces the running binary. The download bar counts the zip. When a release has no zip (older tags published `tinyagent-windows-<arch>.exe` only), `--update` installs that executable instead.

The `bash` tool needs `bash` on your `PATH`, for example from Git for Windows.

## With Go

```bash
go install github.com/vicgupta/tinyagent/cmd/tinyagent@latest
```

## Checksums

Unix and macOS binaries, the Windows zips, and `SHA256SUMS` are on the [releases page](https://github.com/vicgupta/tinyagent/releases). Verify with:

```bash
shasum -a 256 -c SHA256SUMS --ignore-missing
```

`SHA256SUMS` lists `tinyagent-windows-amd64.zip` and `tinyagent-windows-arm64.zip`. It does not list a bare Windows `.exe`. On macOS, a binary downloaded with a browser is quarantined; clear it once with `xattr -d com.apple.quarantine tinyagent`.

## Updating

```bash
tinyagent --update
```

This replaces the binary in place with the latest release, after checking its checksum. On Windows the checked file is `tinyagent-windows-<arch>.zip`, and the installed bytes are the `tinyagent.exe` inside it. When the install directory is not writable, run it with `sudo`. Each step is printed to stderr. See [`--update`](subcommands.md#updating).
