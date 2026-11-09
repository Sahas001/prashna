CREATE TABLE items (
    id             TEXT PRIMARY KEY,
    type           TEXT NOT NULL CHECK (type IN ('paper', 'article', 'news', 'book')),
    title          TEXT NOT NULL,
    title_key      TEXT NOT NULL DEFAULT '', -- item.DedupeKey; '' when too short to dedupe on
    url            TEXT NOT NULL,
    source         TEXT NOT NULL,
    summary        TEXT NOT NULL DEFAULT '',
    authors        TEXT NOT NULL DEFAULT '[]', -- JSON array
    tags           TEXT NOT NULL DEFAULT '[]', -- JSON array
    meta           TEXT NOT NULL DEFAULT '{}', -- JSON object
    published      INTEGER NOT NULL,           -- unix seconds
    fetched_at     INTEGER NOT NULL,           -- unix seconds
    score          REAL NOT NULL DEFAULT 0,
    sig_keyword    REAL NOT NULL DEFAULT 0,
    sig_similarity REAL NOT NULL DEFAULT 0,
    sig_recency    REAL NOT NULL DEFAULT 0,
    feedback       INTEGER NOT NULL DEFAULT 0 CHECK (feedback IN (-1, 0, 1)),
    feedback_at    INTEGER,
    digested_at    INTEGER
);

CREATE INDEX items_title_key ON items (title_key) WHERE title_key != '';
CREATE INDEX items_fetched_at ON items (fetched_at);
CREATE INDEX items_score ON items (score DESC);
CREATE INDEX items_feedback ON items (feedback) WHERE feedback != 0;
