-- The name the user gave a disc whose own name looked like a code
-- ("SBK0X7AJ"). Used instead of discs.name when set.
ALTER TABLE discs ADD COLUMN given_name TEXT NOT NULL DEFAULT '';
