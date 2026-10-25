-- One row for each proxy of the list that the runs use.
CREATE TABLE proxies (
    id INTEGER PRIMARY KEY,
    -- The proxy.Proxy HostPort form: host:port, or socks5://host:port. attempts.proxy holds the same form.
    host_port TEXT NOT NULL UNIQUE,
    -- Both empty for a proxy with no credentials.
    username TEXT NOT NULL DEFAULT '',
    password TEXT NOT NULL DEFAULT '',
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    -- Fixed-width UTC, so the text sorts in time order.
    added_at TEXT NOT NULL
) STRICT;

-- The health of each proxy reads this index.
CREATE INDEX attempts_proxy ON attempts (proxy, id);

PRAGMA user_version = 3;
