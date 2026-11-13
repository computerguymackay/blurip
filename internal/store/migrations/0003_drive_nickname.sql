-- A name the user chooses for each drive ("Blu-ray drive", "Drive 1"), shown
-- everywhere the drive is mentioned so the right tray gets opened by hand.
ALTER TABLE drives ADD COLUMN nickname TEXT NOT NULL DEFAULT '';
