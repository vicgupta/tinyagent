-- Best-of-N (--attempts): each attempt's run row carries its group id and
-- attempt number; the parent records the group itself in attempt_groups.
ALTER TABLE runs ADD COLUMN attempts_id TEXT;
ALTER TABLE runs ADD COLUMN attempt INTEGER;
CREATE INDEX runs_attempts ON runs (attempts_id);
CREATE TABLE attempt_groups (
	id                 TEXT PRIMARY KEY,
	started_at         INTEGER NOT NULL, -- unix milliseconds
	ended_at           INTEGER,
	cwd                TEXT,
	attempts_requested INTEGER NOT NULL,
	attempts_run       INTEGER,
	winner             INTEGER, -- attempt number, NULL when none passed
	winner_branch      TEXT,
	stop_reason        TEXT,
	total_cost_usd     REAL,
	total_wall_time_ms INTEGER,
	trace_id           TEXT,    -- OpenTelemetry trace shared by the attempts
	tinyagent_version  TEXT
);
