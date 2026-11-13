-- Digests become daily editions read in the app: one per local day, with
-- each item's section, plus open tracking for the library.
ALTER TABLE digests ADD COLUMN day TEXT; -- local date, YYYY-MM-DD
CREATE UNIQUE INDEX digests_day ON digests (day) WHERE day IS NOT NULL;
ALTER TABLE digest_items ADD COLUMN section TEXT NOT NULL DEFAULT '';

ALTER TABLE items ADD COLUMN opened_at INTEGER;                -- last opened from Prashna
ALTER TABLE items ADD COLUMN open_count INTEGER NOT NULL DEFAULT 0;
CREATE INDEX items_opened ON items (opened_at) WHERE opened_at IS NOT NULL;
