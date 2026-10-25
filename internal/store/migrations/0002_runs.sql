-- One row for each run of the full mode or of serve.
-- Times are fixed-width UTC, so the text sorts in time order.
CREATE TABLE runs (
    id INTEGER PRIMARY KEY,
    source TEXT NOT NULL,
    started_at TEXT NOT NULL,
    -- NULL while the run is in progress.
    ended_at TEXT,
    -- NULL on a run that ended with no error.
    error TEXT,
    -- The config of the run, with no IMAP password.
    config TEXT NOT NULL CHECK (json_type(config) = 'object')
) STRICT;

-- One row for each attempt that ended. A NULL TEXT value is a field that the record line omits.
CREATE TABLE attempts (
    id INTEGER PRIMARY KEY,
    run_id INTEGER NOT NULL REFERENCES runs (id),
    worker INTEGER NOT NULL,
    -- NULL when the attempt saved no account.
    account_id INTEGER UNIQUE REFERENCES accounts (id),
    started_at TEXT NOT NULL,
    duration_ms INTEGER NOT NULL,
    proxy TEXT,
    category TEXT,
    error TEXT,
    failed_step TEXT,
    failed_phase TEXT,
    -- The steps in order, each with its name and its duration_ms.
    steps TEXT NOT NULL CHECK (json_type(steps) = 'array'),
    browser_bytes_sent INTEGER NOT NULL DEFAULT 0,
    browser_bytes_received INTEGER NOT NULL DEFAULT 0,
    http_bytes_sent INTEGER NOT NULL DEFAULT 0,
    http_bytes_received INTEGER NOT NULL DEFAULT 0,
    -- A failed attempt has both a category and an error. A success has neither.
    CHECK ((category IS NULL) = (error IS NULL))
) STRICT;

-- The attempts page of a run and the counts of a run read this index.
CREATE INDEX attempts_run ON attempts (run_id, id);

PRAGMA user_version = 2;
