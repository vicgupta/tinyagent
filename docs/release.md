# Releasing

Maintainers publish tinyagent by pushing a tag. The `release` workflow in `.github/workflows/release.yml` builds every platform with `make dist`, uploads the artifacts (including `SHA256SUMS`) to a GitHub release, and then publishes WinGet when it can. These steps are also in [CONTRIBUTING.md](../CONTRIBUTING.md#releasing-maintainers).

```bash
git tag v0.x.y && git push origin v0.x.y
```

Tag only after the full suite is green on `main`. `install.sh` verifies checksums against `SHA256SUMS`.

## Artifacts

`make dist` needs the `zip` command. It cross-compiles:

`linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`, `windows/amd64`, `windows/arm64`.

Windows is uploaded as two files per architecture:

| File | Role |
|---|---|
| `tinyagent-windows-amd64.exe`, `tinyagent-windows-arm64.exe` | Bare executable. `tinyagent --update` and the direct Windows download use these names. |
| `tinyagent-windows-amd64.zip`, `tinyagent-windows-arm64.zip` | Zip whose only entry is `tinyagent.exe`. WinGet installs these. |

The zip exists because WinGet needs a zip or an installer at a stable URL, with a SHA-256. A Go `.exe` is not an Inno, NSIS, Burn, or MSI installer, so the bare executable stays for `--update` and the zip is the WinGet installer (`zip` + nested `portable`). `SHA256SUMS` includes both. URLs look like:

`https://github.com/vicgupta/tinyagent/releases/download/vX.Y.Z/tinyagent-windows-amd64.zip`

`make dist` reproduces the same file names locally. `dist/` is gitignored.

## WinGet

Package identifier: `VicGupta.TinyAgent`. The install command is `winget install VicGupta.TinyAgent`.

The `winget` job runs only after the `release` job has published the GitHub release. It uses [vedantmgoyal9/winget-releaser](https://github.com/vedantmgoyal9/winget-releaser) at `v2`, which runs [komac](https://github.com/russellbanks/komac) `update` and opens a pull request from a fork of [microsoft/winget-pkgs](https://github.com/microsoft/winget-pkgs). The job passes installer URLs that match `tinyagent-windows-.+\.zip$`, so the bare `.exe` files are not submitted.

The GitHub release succeeds in all of these cases. The WinGet job logs a notice and exits 0:

- `WINGET_GITHUB_TOKEN` is unset.
- The tag is not a stable `vX.Y.Z` (a release candidate such as `v0.20.0-rc.1` matches the `v*` trigger and is skipped).
- `manifests/v/VicGupta/TinyAgent` does not exist in winget-pkgs yet (the first submission has not merged).

An expired or rejected token is a real failure: the release is already on GitHub, and the WinGet job goes red so the secret can be fixed and the job re-run.

### One-time setup

Do this on the `vicgupta` account, which owns `vicgupta/tinyagent`.

1. Fork `microsoft/winget-pkgs` to `https://github.com/vicgupta/winget-pkgs`. Keep the fork name `winget-pkgs`. The action's `fork-user` defaults to the repository owner (`vicgupta`) and pushes the manifest branch to that fork.
2. Create a **classic** personal access token (GitHub → Settings → Developer settings → Personal access tokens → Tokens (classic)). Fine-grained tokens are not enough: komac can commit with one and then fails to open the pull request. Scopes:
   - `public_repo` (required) to read the fork and open a public pull request.
   - `workflow`, so syncing the fork can update workflow files from upstream. Without it, publishing fails intermittently with `does not have the correct permissions to execute UpdateRef` / `failed to sync upstream changes` until the fork is synced by hand in the browser.
3. On `vicgupta/tinyagent`, create an Actions repository secret named `WINGET_GITHUB_TOKEN` and paste the token. The workflow reads that secret and never prints it. A missing secret skips WinGet.

Choose an expiration you can rotate before it lapses. Replace the secret with a new token; the workflow file does not change.

### First submission

`winget-releaser` only updates a package that is already in winget-pkgs. It exits with an error if `komac list-versions` cannot find `VicGupta.TinyAgent`. The workflow treats that as "not published yet" and skips, so the first tag after this change does not fail the release.

Open the first pull request locally, after a release that contains the zips. komac prompts for installer fields, so run it in a terminal, not in GitHub Actions:

```bash
export GITHUB_TOKEN=...   # the same classic PAT; do not commit it
komac new VicGupta.TinyAgent \
  --version X.Y.Z \
  --urls \
    "https://github.com/vicgupta/tinyagent/releases/download/vX.Y.Z/tinyagent-windows-amd64.zip" \
    "https://github.com/vicgupta/tinyagent/releases/download/vX.Y.Z/tinyagent-windows-arm64.zip" \
  --publisher "Vic Gupta" \
  --publisher-url "https://github.com/vicgupta" \
  --package-name "TinyAgent" \
  --package-url "https://github.com/vicgupta/tinyagent" \
  --license "MIT" \
  --license-url "https://github.com/vicgupta/tinyagent/blob/vX.Y.Z/LICENSE" \
  --short-description "A minimal coding agent in a single static Go binary" \
  --moniker "tinyagent" \
  --release-notes-url "https://github.com/vicgupta/tinyagent/releases/tag/vX.Y.Z" \
  --submit
```

At the prompts:

- Select the nested file `tinyagent.exe` for each zip.
- Portable command alias: `tinyagent`. Do not skip this prompt. The alias is what WinGet puts on PATH, and later updates copy it.
- Installer type `zip`, nested installer type `portable`.
- amd64 zip → architecture x64; arm64 zip → architecture arm64.

**The first submission is reviewed manually by Microsoft.** After it is merged, the next stable tag opens the update pull request from the workflow with no further prompts. Later version bumps are usually checked by the winget-pkgs automation rather than a full manual review.

Releases are built with `make dist` and uploaded by `release.yml`. WinGet publishing is the `winget` job in that workflow.
