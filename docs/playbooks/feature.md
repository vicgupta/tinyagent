---
name: feature
intent: Build a new feature, option, command or endpoint that adds behavior, with tests and docs.
match: [add, feature, implement, support, new, option, command, endpoint, build]
skills: []
---

Fit the feature into the code that is already there.

1. Write down what the feature does, its inputs and outputs, and what is out of scope.
2. Read the code around where it will live and follow its existing patterns and names.
3. Sketch the change: which files, which new functions, which existing ones get small hooks.
4. Implement it in small steps, building and running tests as you go.
5. Add tests for the main path, the edge cases and the error cases.
6. Update the docs, help text and examples that a user would read.
7. Summarize what was added, how to use it, and anything left for later.

## Done when

The feature works as described, it has tests, the docs mention it, and the build and tests pass.
