CREATE TABLE subagents (
	id            INTEGER PRIMARY KEY AUTOINCREMENT,
	run_id        TEXT NOT NULL,
	subagent_id   TEXT NOT NULL, -- e.g. d1 or w1
	kind          TEXT NOT NULL, -- delegate or worker
	tool_call_id  TEXT,
	parent_step   INTEGER,
	label         TEXT,
	model         TEXT,
	branch        TEXT,    -- workers only
	base_commit   TEXT,    -- workers only
	commit_sha    TEXT,    -- workers only, when they changed files
	files_changed INTEGER, -- workers only
	applied       INTEGER NOT NULL DEFAULT 0,
	status        TEXT,
	steps         INTEGER,
	input_tokens  INTEGER,
	output_tokens INTEGER,
	cost_usd      REAL,
	started_at    INTEGER NOT NULL, -- unix milliseconds
	ended_at      INTEGER,
	duration_ms   INTEGER
);
CREATE INDEX subagents_run ON subagents (run_id);
CREATE INDEX subagents_kind ON subagents (kind);
