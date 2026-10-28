-- fixture (F4, Codex round 3): an inline NOT NULL on a newly added column with no DEFAULT is a
-- contract the moment it runs — breaks an older release's INSERT that omits this column, even
-- on a table that happens to be empty today.
ALTER TABLE conversations ADD COLUMN required_code text NOT NULL;
