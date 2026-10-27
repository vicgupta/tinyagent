# Skill: run-tests
> Intent: Run the Go test suite and fix failing tests.

**Inputs**
- validate-diff
- repo_path

**Success check:** `go test -count=1 ./...` passes.
