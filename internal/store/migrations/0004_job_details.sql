-- How each disc was handled: the drive mode, which drives blurip was using,
-- how many rips ran at the same time, and where the time went.
ALTER TABLE jobs ADD COLUMN drive_mode   TEXT    NOT NULL DEFAULT '';  -- single | sequential | parallel
ALTER TABLE jobs ADD COLUMN drives_used  TEXT    NOT NULL DEFAULT '';  -- e.g. "DVD drive (sr0), Blu-ray drive (sr1)"
ALTER TABLE jobs ADD COLUMN rips_at_once INTEGER NOT NULL DEFAULT 0;   -- most rips running at once during this one; 0 = never ripped
ALTER TABLE jobs ADD COLUMN read_seconds REAL    NOT NULL DEFAULT 0;   -- reading the disc
ALTER TABLE jobs ADD COLUMN wait_seconds REAL    NOT NULL DEFAULT 0;   -- waiting for another drive's rip to finish
ALTER TABLE jobs ADD COLUMN rip_seconds  REAL    NOT NULL DEFAULT 0;   -- ripping and moving into place
