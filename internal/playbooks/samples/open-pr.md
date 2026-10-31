---
name: open-pr
intent: Prepare a branch and open a pull request with a clear title and description, after checks pass.
match: [pr, pull, request, open, branch, push, review, submit]
skills: []
---

Get the change ready for a reviewer, then open the pull request.

1. Check the working tree: commit only the files that belong to this change, and never build output or secrets.
2. Make sure the branch is based on the latest main; merge main in if it moved (don't rebase a pushed branch).
3. Run the formatter, the build, the linters and the tests, and fix what fails.
4. Write commit messages that say what changed and why.
5. Push the branch without force.
6. Open the pull request with a short title and a description: what changed, why, how it was tested, and any risk.
7. Report the pull request link.

## Done when

The pull request is open against main, its description explains the change and testing, and the local checks passed before pushing.
