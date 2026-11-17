-- The encode queue's order, changeable with blurip encode -U (move,
-- group). Entries start in the order they were added.
ALTER TABLE encodes ADD COLUMN position INTEGER NOT NULL DEFAULT 0;
UPDATE encodes SET position = id;
CREATE INDEX encodes_queue ON encodes(status, position, id);
