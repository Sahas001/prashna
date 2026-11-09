-- Small key/value store for server-generated state (e.g. the feedback link signing key).
CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- One row per sent digest; digest_items keeps what was in it, in order,
-- for the history view.
CREATE TABLE digests (
    id      INTEGER PRIMARY KEY,
    sent_at INTEGER NOT NULL, -- unix seconds
    subject TEXT NOT NULL
);

CREATE TABLE digest_items (
    digest_id INTEGER NOT NULL REFERENCES digests (id) ON DELETE CASCADE,
    item_id   TEXT NOT NULL REFERENCES items (id) ON DELETE CASCADE,
    position  INTEGER NOT NULL,
    PRIMARY KEY (digest_id, item_id)
);

CREATE INDEX digest_items_item ON digest_items (item_id);
CREATE INDEX items_undigested ON items (type, score DESC) WHERE digested_at IS NULL;
