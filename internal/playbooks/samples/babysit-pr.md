---
name: babysit-pr
intent: Watch an open pull request until it can merge - keep it up to date with main, resolve conflicts, fix failing CI - then merge it when everything is green.
match: [babysit, watch, shepherd, land, merge, ci, green, checks, pr]
skills: []
---

Keep one pull request moving until it merges, or stop and say why it can't.
Never force-push, never rebase, never use an admin override. Try to fix red CI at most 3 times.

1. Read the pull request: its branch, the checks, the review state, and whether it has conflicts.
2. If main moved, merge main into the branch with a merge commit. Resolve conflicts by keeping what both sides meant; never drop changes from main. Run the build and tests, then push without force.
3. If a check fails, read its log, fix the cause on the branch, run the checks locally and push. Count this as one try; after 3 tries, stop and report.
4. If a reviewer requested changes, stop and report them; don't merge over them.
5. Wait for the checks to finish, then read the state again.
6. Merge only when every check is green, the merge state is clean and no changes are requested. Use a merge commit (not squash or rebase) and never an admin override. Don't ask for a confirmation first.
7. Otherwise stop and report what is blocking the merge and what you tried.

## Done when

The pull request is merged with a merge commit after all checks passed, or the run stopped and reported exactly what blocks it.
