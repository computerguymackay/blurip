-- A box set title holding several episodes (one long title with a chapter
-- range per episode): its disc_episodes row is the first of them, and
-- chapters lists each episode's range ("1-5,6-10,..."); '' = one episode.
ALTER TABLE disc_episodes ADD COLUMN chapters TEXT NOT NULL DEFAULT '';

-- An encode of part of a rip: HandBrake's chapter range ("6-10"); '' = all.
ALTER TABLE encodes ADD COLUMN chapters TEXT NOT NULL DEFAULT '';
