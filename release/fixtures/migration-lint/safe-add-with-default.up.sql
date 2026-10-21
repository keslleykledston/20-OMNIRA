-- fixture (F4): a new NOT NULL column backfilled via a DEFAULT in the SAME statement — safe
-- under expand_only because an old release's INSERT omitting this column still gets a value.
ALTER TABLE widgets ADD COLUMN priority INTEGER NOT NULL DEFAULT 0;
ALTER TABLE widgets ADD COLUMN category TEXT DEFAULT 'uncategorized';
ALTER TABLE widgets ALTER COLUMN category SET NOT NULL;
