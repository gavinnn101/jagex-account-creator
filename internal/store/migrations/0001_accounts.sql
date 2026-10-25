-- One row for each account whose password the attempt sent.
-- An empty TEXT value or a NULL JSON value is a field that the JSONL record omits.
CREATE TABLE accounts (
    id INTEGER PRIMARY KEY,
    email TEXT NOT NULL UNIQUE,
    username TEXT NOT NULL,
    password TEXT NOT NULL,
    totp_secret TEXT NOT NULL DEFAULT '',
    backup_codes TEXT CHECK (json_type(backup_codes) = 'array'),
    -- Fixed-width UTC, so the text sorts in time order.
    created_at TEXT NOT NULL,
    proxy TEXT NOT NULL DEFAULT '',
    browser_bytes_sent INTEGER NOT NULL DEFAULT 0,
    browser_bytes_received INTEGER NOT NULL DEFAULT 0,
    http_bytes_sent INTEGER NOT NULL DEFAULT 0,
    http_bytes_received INTEGER NOT NULL DEFAULT 0,
    unconfirmed INTEGER NOT NULL DEFAULT 0 CHECK (unconfirmed IN (0, 1)),
    -- The keys of an imported line that the record does not know, in line order.
    extra TEXT CHECK (json_type(extra) = 'object')
) STRICT;

PRAGMA user_version = 1;
