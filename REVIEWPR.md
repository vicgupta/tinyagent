# REVIEWPR — PR review prompt

You are reviewing pull requests in the `tinyagent` repository. Your job is to get changes reviewed, approved, and merged with **zero avoidable merge conflicts**.

Follow these steps in order. Prefer `gh` / `git` read-only checks over local checkouts wherever possible — they are faster and don't dirty the working tree.

## 1. Inventory the open PRs first

```bash
gh pr list --state open --json number,title,headRefName,baseRefName,mergeable,mergeStateStatus,additions,deletions,changedFiles
```

For each PR, record:
- `mergeStateStatus` (`CLEAN` vs `DIRTY`)
- `mergeable` (`MERGEABLE` vs `CONFLICTING`)
- its file list: `gh pr view <n> --json files -q '.files[].path'`

## 2. Find overlaps

Compute the shared files between every pair of open PRs:

```bash
comm -12 <(gh pr view <A> --json files -q '.files[].path' | sort) \
         <(gh pr view <B> --json files -q '.files[].path' | sort)
```

**Rule:** two PRs that share *any* file must be merged serially, never in parallel.

## 3. Diagnose *why* a PR conflicts — don't just note that it does

```bash
git fetch origin

# exact conflicting files, with no working-tree changes:
git merge-tree $(git merge-base origin/main origin/<branch>) origin/main origin/<branch> \
  | grep "changed in both"

# how far the branch has drifted from main:
git log --oneline $(git merge-base origin/main origin/<branch>)..origin/main
```

A branch that branched off main *before* a later-merged PR (that touches the same files) will conflict. `git merge-base` shows exactly which commit it is missing — one missing commit that touches shared files is enough to cause a multi-file conflict.

## 4. Pick the merge order

1. **Merge the narrower PR first** (smaller `additions`/`deletions`, already `CLEAN`/`MERGEABLE`).
2. **Rebase each remaining PR onto the *current* main** (post-previous-merge) before review.
3. Merge the next PR, then repeat.

This confines all conflict resolution to exactly one rebase per PR, and the final merge is clean.

## 5. Review each PR for

- **Rebase-before-approve.** The PR must be based on current `main`. Reject or send back any PR carrying a `Merge remote-tracking branch ...` commit — especially one whose commit body contains `# Conflicts:` residue — and ask the author to rebase instead.
- **Shared-file hygiene.** Changes to hot files (`cmd/tinyagent/main.go`, `internal/config/config.go`, `internal/agent/loop.go`, `internal/trajectory/trajectory.go`, `README.md`, `CHANGELOG.md`) should stay in their own regions so "both sides added content at the same spot" conflicts stay mechanical.
- **Full suite green:** `gofmt -l .`, `go build ./...`, `go vet ./...`, `go test -count=1 ./...`.
- **Linux build-tagged files:** run `GOOS=linux go build ./...` and `GOOS=linux go vet ./...` for anything touching `//go:build linux` files — macOS tests don't compile them.
- **No cross-file redeclarations** (e.g. duplicate `func runCLI` in two `_test.go` files). `go vet` / `go test` catch what a textual merge can't.

## Non-negotiable rules

- Never review/merge two overlapping PRs in parallel.
- Never approve a PR that isn't rebased onto current `main`.
- Prefer rebase over merge commits — keep history linear.
- When resolving, keep **both** semantic behaviors; never drop an exit/error path while taking one side.

## Known hot files (most collision-prone)

`README.md`, `CHANGELOG.md`, `cmd/tinyagent/main.go`, `internal/agent/loop.go`, `internal/config/config.go`, `internal/trajectory/trajectory.go`

## Output

For each review turn, report:
1. The open-PR inventory (mergeable state + shared-file overlaps).
2. The recommended merge order and why.
3. Which PR needs a rebase first, and against what.
4. Any merge-commit residue or drift found.
