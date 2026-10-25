-- One row for each named run config that the run form saves.
CREATE TABLE presets (
    id INTEGER PRIMARY KEY,
    name TEXT NOT NULL UNIQUE CHECK (name <> ''),
    -- The config in the form of runs.config, with no IMAP password.
    config TEXT NOT NULL CHECK (json_type(config) = 'object'),
    -- Fixed-width UTC, so the text sorts in time order.
    saved_at TEXT NOT NULL
) STRICT;

PRAGMA user_version = 4;
