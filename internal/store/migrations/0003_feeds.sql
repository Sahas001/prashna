-- Per-feed HTTP cache validators and health, so RSS fetches can use
-- conditional GET and the web UI can show broken feeds.
CREATE TABLE feed_state (
    url             TEXT PRIMARY KEY,
    title           TEXT NOT NULL DEFAULT '',
    etag            TEXT NOT NULL DEFAULT '',
    last_modified   TEXT NOT NULL DEFAULT '',
    last_fetched_at INTEGER,          -- unix seconds, any outcome
    last_ok_at      INTEGER,          -- unix seconds, last 200/304
    last_error      TEXT NOT NULL DEFAULT '',
    last_items      INTEGER NOT NULL DEFAULT 0
);
