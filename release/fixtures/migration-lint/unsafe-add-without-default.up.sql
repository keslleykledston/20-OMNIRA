-- fixture (F4): a new NOT NULL column with NO default — an old release's INSERT omitting it
-- fails after this migration even though it was "backfilled" for rows that already existed.
-- This is the shape of the real, already-shipped migrations/000104_hub_work_pools.up.sql.
ALTER TABLE widgets ADD COLUMN category TEXT;
UPDATE widgets SET category = 'default';
ALTER TABLE widgets ALTER COLUMN category SET NOT NULL;
