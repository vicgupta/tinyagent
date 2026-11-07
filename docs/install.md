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

Download `tinyagent-windows-amd64.exe` (or `tinyagent-windows-arm64.exe`) from the [latest release](https://github.com/vicgupta/tinyagent/releases/latest), rename it to `tinyagent.exe`, and put it in a folder on your `PATH`.

`tinyagent --update` downloads that same bare executable (`tinyagent-windows-<arch>.exe`), checks it against `SHA256SUMS`, and replaces the binary in place. It does not download the zip.

The `bash` tool needs `bash` on your `PATH`, for example from Git for Windows.

## With Go

```bash
go install github.com/vicgupta/tinyagent/cmd/tinyagent@latest
```

## Checksums

Binaries for every platform, the Windows zips, and `SHA256SUMS` are on the [releases page](https://github.com/vicgupta/tinyagent/releases). Verify with:

```bash
shasum -a 256 -c SHA256SUMS --ignore-missing
```

`SHA256SUMS` lists both the bare executables and the Windows zips. On macOS, a binary downloaded with a browser is quarantined; clear it once with `xattr -d com.apple.quarantine tinyagent`.

## Updating

```bash
tinyagent --update
```

This replaces the binary in place with the latest release, after checking its checksum. When the install directory is not writable, run it with `sudo`. Each step is printed to stderr. See [`--update`](subcommands.md#updating).
