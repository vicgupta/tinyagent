# Skill: validate-diff
> Intent: Make the working-tree diff gofmt, build and vet clean with no build artifacts.

**Inputs**
- generate-code
- repo_path

**Success check:** `gofmt -l .` prints nothing, `go build ./...` and `go vet ./...` pass, and `git status` shows no binaries or coverage files.
