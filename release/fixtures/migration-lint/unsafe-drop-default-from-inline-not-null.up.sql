-- fixture (F4, Codex round 7): a column added NOT NULL with an inline DEFAULT, then losing that
-- default via DROP DEFAULT (or SET DEFAULT NULL), breaks older inserts immediately — no later
-- SET NOT NULL is needed to trigger this, since the column was already NOT NULL from the ADD.
ALTER TABLE accounts ADD COLUMN active boolean NOT NULL DEFAULT true;
ALTER TABLE accounts ALTER COLUMN active DROP DEFAULT;
