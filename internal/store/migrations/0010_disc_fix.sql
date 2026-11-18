-- The disc left in each drive after blurip handled it (its eject failed,
-- or ejecting is off), so a restart doesn't treat it as newly inserted.
-- Cleared when the drive is seen empty or a disc is ejected.
ALTER TABLE drives ADD COLUMN left_disc_id INTEGER REFERENCES discs(id);

-- What a job changed besides ripping (fixing a box set disc: episodes
-- renamed, deleted or renumbered).
ALTER TABLE jobs ADD COLUMN note TEXT NOT NULL DEFAULT '';
