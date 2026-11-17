-- The rip's running time, measured when its encode is checked, for
-- blurip encode --stats (speed against real time). 0 = not known: encodes
-- finished before this was added.
ALTER TABLE encodes ADD COLUMN length_seconds REAL NOT NULL DEFAULT 0;
