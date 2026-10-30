# Planner

### Plan tool

`--plan` (or `plan: {enabled: true}`) adds an `update_plan` tool, off by default. The model sends the whole list each time: up to 50 items, each `{id, text, status}` with status `pending`, `in_progress`, `done`, or `blocked`, at most one `in_progress`, plus an optional `note`. The plan lives in the trajectory (`plan`, with every change in `plan_history`), not in the message history: before each request a `[Current plan]` block is pinned after the system prompt, so it survives compaction (the summarizer also sees it) and `--resume`. Each update emits `plan.updated`, and `--plan-file plan.md` mirrors the current plan as a Markdown checklist after each save (the checkpoint stays the source of truth). The tool is available with `--read-only`, since it touches nothing in the workspace.

A checkpoint records whether the plan tool was enabled, and `--resume` keeps that setting: passing `--plan` to resume a run that started without it is an error, and the config's `plan.enabled` is ignored on resume. `--plan-file` requires the plan tool.

### Plan first

`--plan-first` (config `plan.require_first: true`, or `tinyagent planner --first`) implies `--plan` and makes the plan come before any change: a workspace-changing tool call is refused until the model has called `update_plan`, and a `plan.required` event is emitted. Read-only tools, `ask_user` and `submit` are never held back. Until a plan exists, the pinned `[Current plan]` block asks for one. It is off by default so trivial tasks are not slowed down. `--plan-first --plan=false` is a usage error.

Steps can also be marked `failed` (shown as `[-]`), alongside `pending`, `in_progress`, `done` and `blocked`. Each update prints `plan updated:` with the plan to stdout (not in `--json` mode).
