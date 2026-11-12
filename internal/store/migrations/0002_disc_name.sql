-- Human-readable disc name (e.g. "Wild Card") for history listings.
ALTER TABLE discs ADD COLUMN name TEXT NOT NULL DEFAULT '';
