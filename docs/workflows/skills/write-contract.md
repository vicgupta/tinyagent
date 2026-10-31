# Skill: write-contract
> Intent: Record a surface's public API, invariants and extension rules in one file.

**Inputs**
- surface
- repo_path
- contracts_dir

**Success check:** `<contracts_dir>/<surface>.md` exists, starts with the contract header, and every statement in it matches the current code.

A contract doc is the stable reference for one part of the repository that keeps getting built on, such as a component library, an internal API or a plugin interface. It describes what is true today, not what to build next: it is not a spec (the task you are given) and not a skill (instructions in `docs/workflows/skills/`). Never name it `AGENTS.md` or write it into an `AGENTS.md` file.

1. Work out the surface's name (a short lowercase slug, such as `tools-executor`) and which paths or packages it covers. If `contracts_dir` is not provided, use `docs/contracts`.
2. Read the code that defines the surface: its exported types and functions, how callers use it, and its tests. Don't guess; every statement must be checkable against the code.
3. If `<contracts_dir>/<surface>.md` already exists, update it in place: keep what is still true, fix what changed, remove what no longer exists.
4. Write the file in exactly this shape:

```markdown
# Contract: <surface>
Covers: <comma-separated paths or packages>
Last verified: <current commit hash, from git rev-parse --short HEAD, or today's date as YYYY-MM-DD>

## Public API
<the exported types, functions, flags or endpoints other code relies on, one bullet each with a one-line purpose>

## Invariants
<what must stay true: ordering, error behavior, concurrency, compatibility guarantees>

## How to extend
<the steps for adding to the surface the way the existing code does, including which tests to add>
```

Change no code in this stage. End with the path of the contract doc you wrote and whether it was new or updated.
