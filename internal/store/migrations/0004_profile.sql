-- The interest profile, feeds and book follows move from YAML into the
-- database so the web UI can edit them. YAML values are imported once.
CREATE TABLE interests (
    id         INTEGER PRIMARY KEY,
    term       TEXT NOT NULL UNIQUE COLLATE NOCASE,
    weight     REAL NOT NULL,
    created_at INTEGER NOT NULL
);

CREATE TABLE feeds (
    id         INTEGER PRIMARY KEY,
    url        TEXT NOT NULL UNIQUE,
    name       TEXT NOT NULL DEFAULT '',
    tags       TEXT NOT NULL DEFAULT '[]', -- JSON array
    type       TEXT NOT NULL DEFAULT 'article' CHECK (type IN ('article', 'news', 'paper')),
    enabled    INTEGER NOT NULL DEFAULT 1,
    created_at INTEGER NOT NULL
);

CREATE TABLE book_follows (
    id         INTEGER PRIMARY KEY,
    kind       TEXT NOT NULL CHECK (kind IN ('author', 'subject')),
    name       TEXT NOT NULL COLLATE NOCASE,
    created_at INTEGER NOT NULL,
    UNIQUE (kind, name)
);

-- Reading list.
ALTER TABLE items ADD COLUMN saved_at INTEGER;
ALTER TABLE items ADD COLUMN read_at INTEGER;
CREATE INDEX items_saved ON items (saved_at) WHERE saved_at IS NOT NULL;
