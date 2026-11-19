-- What happens to an entry's rip once it is encoded: '' = as set up
-- (delete_after_encode), 'keep' or 'delete'. Changed with blurip encode -U,
-- e.g. to keep a rip for trying another preset.
ALTER TABLE encodes ADD COLUMN rip_after TEXT NOT NULL DEFAULT '';
