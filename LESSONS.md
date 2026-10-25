# Lessons Learned

Notes captured after each work session. These are hard-won, concrete lessons for future turns.

---

## 2026-10-25 — tinyagent rebase/merge session

### Git rebase & conflict resolution

- **Preserve *both* semantic behaviors when both sides edit the same function.** Rebasing `#93` (providers) onto main surfaced that `loadConfigError` (new) had replaced `configFail` (old) but dropped the `config.InvalidError` → exit-2 branch. The correct merge delegated to `configFail` for the non-provider case, keeping *both* exit-2 paths (`ErrInvalidProvider` and `InvalidError`).

- **"Both sides added a function at the same spot" conflicts share a common tail (closing braces).** When `checkCommandEnv` and `validateProviders` were both inserted at the same location, git kept one copy of the closing `}\n}\n}\n\treturn nil\n}` as shared context. You must give *each* function its own braces — don't let one function's tail close the other.

- **Don't blindly take "theirs" when a commit mixes a refactor with a regression.** `#91` changed `self` → `helperPath()` (fine, a rename) but *also* changed `bashPath()` → `"bash"` (a bug). Evaluate each sub-change separately. `bashPath()` exists specifically so the sandbox helper finds bash even when `command_env` strips `PATH`.

- **Check for duplicate test helpers after a rebase.** Two `_test.go` files each defined `func runCLI(...)`, producing a `redeclared` error git can't see (textual merge is per-file, not cross-file). Only `go vet`/`go test` catch it.

### Cross-platform / build tags

- **macOS `go test` does NOT compile `//go:build linux` files.** A Linux-only bug (the `"bash"` vs `bashPath()` regression) passed locally on macOS and only failed in CI. Whenever you touch build-tagged files, verify with `GOOS=linux go build ./...` and `GOOS=linux go vet ./...` before pushing.

### Golden/snapshot tests

- **Regenerate exact-output expectations from the real binary.** For a `--help` usage test, don't hand-compute column alignment — run `go run ./cmd/<x> --help`, capture stdout, and paste it into the expected string. The alignment is derived from `max(len(flag))`, so hand-counting spaces is error-prone.

### Tooling

- **A naive string-replace conflict resolver drops newlines.** My first Python resolver joined lines (e.g. `\t"slices"\t"strings"`) because replacement strings omitted trailing newlines. Prefer the `edit` tool with exact context, or slice reusing the file's own bytes and include trailing `\n`.

- **After resolving, always re-run the full suite** — `gofmt -l`, `go build`, `go vet`, `go test ./...`. `gofmt -l` catches missing blank lines/trailing newlines; `go vet` catches cross-file redeclarations.

### GitHub community / moderation

- **Recognize coordinated promotional campaigns.** A single account (`gjwang`) filed ~86 issues in 6 days across ~40 repos, all promoting one product (BlockVectra), leaving a "use our free endpoint" comment. Signal: new org (created days earlier) + high issue velocity + same pitch everywhere. Confirm with `gh api search/issues?q=author:X+created:...`.

- **"Limit to prior contributors"** (repo Settings → Moderation → Interaction limits) blocks non-contributors from commenting/opening issues. It's **temporary** (max 6 months), so re-apply. Set via `gh api --method PUT repos/O/R/interaction-limits -f limit=contributors_only -f expiry=six_months`.

### Concurrency

- **An autonomous pipeline may be working on the same repo in parallel.** The "amf" pipeline merged #102 and closed #88/#98 while I was mid-rebase. Re-check `origin/main` immediately before force-pushing — `main` may have moved (a `compare` showing `behind_by > 0` means you need another rebase).

---

## 2026-10-25 — PR review triage (overlapping PRs)

### Merge-conflict diagnosis

- **Two open PRs touching the same files will always fight.** PRs #105 (snapshots/`--rewind`) and #106 (file access rules) both touch `README.md`, `CHANGELOG.md`, `cmd/tinyagent/main.go`, `internal/agent/loop.go`, `internal/config/config.go`, `internal/trajectory/trajectory.go`. Merge them serially, never in parallel.

- **Use `gh pr view --json mergeable,mergeStateStatus` before touching anything.** It reports `CLEAN`/`MERGEABLE` (#106) vs `DIRTY`/`CONFLICTING` (#105) with no local checkout — cheaper than a manual merge.

- **`git merge-tree $(git merge-base origin/main origin/<br>) origin/main origin/<br>` piped to `grep "changed in both"` lists the exact conflicting files** without dirtying the working tree. Follow with `grep -A4` + `grep -oE '...\.(go|md)$'` to extract the filenames.

- **`git merge-base` explains *why* one branch conflicts.** #105 branched off main at `b2a238f` (before #100 sandbox-allowlist landed); #106 branched off current `8b127da`. Being one commit behind main — when that commit touches the same files — is enough to produce a 7-file conflict.

### Review workflow to minimize conflicts

- **Rebase-before-approve.** Every PR must be rebased onto the *current* main (post-previous-merge) before review/merge. #105 skipped this and now carries a "Merge remote-tracking branch" commit whose body still contains `# Conflicts:` residue.

- **Merge the narrower PR first, then rebase the larger one onto it.** #106 (file access, +955/−23) is clean and narrow; merge it, then rebase #105 (snapshots, +2375) onto the new main and resolve the remaining shared-file conflicts in one shot.

- **Prefer rebase over merge commits** so history stays linear and you don't get nested conflict-marker residue in commit message bodies.

- **Encode a repeated triage workflow as a committed prompt file** (`REVIEWPR.md`) instead of re-deriving it each session: the diagnostic commands and merge-order rules stay in one place and survive context resets.

---

## Template for future turns

<!-- Append new dated sections above. Each lesson: one line, concrete, actionable. -->
