-- Learned ranking: the model's like-probability per item and the
-- features that pushed it up, for "why" explanations.
ALTER TABLE items ADD COLUMN sig_learned REAL NOT NULL DEFAULT 0;
ALTER TABLE items ADD COLUMN learned_why TEXT NOT NULL DEFAULT '[]'; -- JSON array
