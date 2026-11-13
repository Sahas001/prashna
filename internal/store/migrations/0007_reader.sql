-- In-app reader: the cleaned-up text of a piece, kept so it can be read
-- offline, plus the reader's highlights and notes on it.
CREATE TABLE articles (
    item_id    TEXT PRIMARY KEY REFERENCES items (id) ON DELETE CASCADE,
    html       TEXT NOT NULL,              -- sanitized by internal/reader
    byline     TEXT NOT NULL DEFAULT '',
    site       TEXT NOT NULL DEFAULT '',
    words      INTEGER NOT NULL DEFAULT 0,
    fetched_at INTEGER NOT NULL
);

-- A highlight is a span of the article's text: character offsets into the
-- text of the stored html, plus the quoted words themselves so it survives
-- (and can be re-found) if the text is ever fetched again.
CREATE TABLE highlights (
    id         INTEGER PRIMARY KEY,
    item_id    TEXT NOT NULL REFERENCES items (id) ON DELETE CASCADE,
    start_pos  INTEGER NOT NULL,
    end_pos    INTEGER NOT NULL,
    quote      TEXT NOT NULL,
    note       TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL
);
CREATE INDEX highlights_item ON highlights (item_id, start_pos);
CREATE INDEX highlights_created ON highlights (created_at);

-- One free-form note per piece.
CREATE TABLE item_notes (
    item_id    TEXT PRIMARY KEY REFERENCES items (id) ON DELETE CASCADE,
    body       TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);
