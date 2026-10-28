-- fixture (F4, Codex round 8): a nullable column later given a non-NULL default via
-- ALTER COLUMN ... SET DEFAULT (not only inline on ADD COLUMN), backfilled, then set NOT NULL,
-- is just as backward-compatible as an inline default — must not be flagged.
ALTER TABLE accounts ADD COLUMN active boolean;
ALTER TABLE accounts ALTER COLUMN active SET DEFAULT true;
UPDATE accounts SET active = true WHERE active IS NULL;
ALTER TABLE accounts ALTER COLUMN active SET NOT NULL;
