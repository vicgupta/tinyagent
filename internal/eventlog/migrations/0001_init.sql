CREATE TABLE runs (
	id                 TEXT PRIMARY KEY,
	started_at         INTEGER NOT NULL, -- unix milliseconds
	ended_at           INTEGER,
	cwd                TEXT,
	model              TEXT,
	provider           TEXT,
	task               TEXT,    -- only at level full
	task_sha256        TEXT,
	task_len           INTEGER,
	level              TEXT NOT NULL,
	resumed            INTEGER NOT NULL DEFAULT 0,
	status             TEXT,
	steps              INTEGER,
	input_tokens       INTEGER,
	output_tokens      INTEGER,
	cache_read_tokens  INTEGER,
	cache_write_tokens INTEGER,
	cost_usd           REAL,
	duration_ms        INTEGER,
	tinyagent_version  TEXT,
	schema_version     INTEGER NOT NULL
);
CREATE INDEX runs_started_at ON runs (started_at);

CREATE TABLE events (
	id      INTEGER PRIMARY KEY AUTOINCREMENT,
	run_id  TEXT NOT NULL,
	seq     INTEGER NOT NULL,
	step    INTEGER NOT NULL,
	ts      INTEGER NOT NULL, -- unix milliseconds
	type    TEXT NOT NULL,
	payload TEXT NOT NULL     -- JSON object
);
CREATE INDEX events_run_step ON events (run_id, step);
CREATE INDEX events_type ON events (type);

CREATE TABLE tool_calls (
	id             INTEGER PRIMARY KEY AUTOINCREMENT,
	run_id         TEXT NOT NULL,
	step           INTEGER NOT NULL,
	call_id        TEXT,
	tool           TEXT,
	args           TEXT,    -- only at level full
	result_summary TEXT,
	duration_ms    INTEGER,
	exit_code      INTEGER, -- reserved; not reported by tools yet
	is_error       INTEGER,
	started_at     INTEGER NOT NULL
);
CREATE INDEX tool_calls_run ON tool_calls (run_id, step);
CREATE INDEX tool_calls_tool ON tool_calls (tool);
