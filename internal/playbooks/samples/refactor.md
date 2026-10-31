---
name: refactor
intent: Refactor, restructure, rename, simplify or clean up code without changing its behavior.
match: [refactor, restructure, rename, simplify, cleanup, clean, extract, tidy, duplicate]
skills: []
---

Behavior must not change; only the shape of the code does.

1. Name the goal of the refactor and the area it touches. Keep it narrow.
2. Check that tests cover the behavior you are about to move. Add characterization tests where they don't.
3. Run the tests before touching anything so you know the starting point is green.
4. Change the code in small, separate steps, running the tests after each one.
5. Don't fix bugs or add features on the way; note them for later instead.
6. Remove what the refactor made dead: old helpers, imports and comments.
7. Summarize the new structure and confirm that behavior is unchanged.

## Done when

The tests that passed before still pass, with no change in what the code does.
