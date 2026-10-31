-- Model routing: one row per role, task and model with the run's usage
-- (the result event's usage_by_role; only when helpers or children ran).
CREATE TABLE run_usage (
	id                 INTEGER PRIMARY KEY AUTOINCREMENT,
	run_id             TEXT NOT NULL,
	role               TEXT NOT NULL,
	task               TEXT NOT NULL,
	model              TEXT NOT NULL,
	requests           INTEGER NOT NULL,
	input_tokens       INTEGER NOT NULL,
	output_tokens      INTEGER NOT NULL,
	cache_read_tokens  INTEGER NOT NULL,
	cache_write_tokens INTEGER NOT NULL,
	cost_usd           REAL NOT NULL
);
CREATE INDEX run_usage_run ON run_usage (run_id);
