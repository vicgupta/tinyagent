# Skill: plan-tasks
> Intent: Break a spec into small, ordered tasks before writing any code.

**Inputs**
- spec
- repo_path

**Success check:** every task has acceptance criteria and a verification step, in dependency order.

Write each task in this shape, so the workflow's spec gate can check it:

```markdown
Task 1: <short title>
<what to change, and which task it depends on, if any>
Acceptance criteria:
- <an observable outcome, such as what a command prints or returns>
Verify: `<one shell command that checks this task, for example go test ./...>`
```

Every task needs at least one acceptance-criteria bullet and a `Verify:` line with a single command in backticks whose program exists in the repository or on PATH.
