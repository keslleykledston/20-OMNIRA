-- DATA LOSS: this drops every sector, calendar, exception, holiday and suspension and the links on queues and tenants. Rolling back the
-- code (images) does NOT need it: the old code ignores these columns and tables. Use it only to undo the schema in a throwaway database;
-- in production prefer a forward fix.
ALTER TABLE tenants DROP CONSTRAINT IF EXISTS tenants_default_calendar_fk;
ALTER TABLE tenants DROP COLUMN IF EXISTS default_calendar_id;
DROP INDEX IF EXISTS idx_queues_sector;
ALTER TABLE queues DROP CONSTRAINT IF EXISTS queues_sector_fk, DROP CONSTRAINT IF EXISTS queues_calendar_fk;
ALTER TABLE queues DROP COLUMN IF EXISTS sector_id, DROP COLUMN IF EXISTS calendar_id;
DROP TABLE IF EXISTS sectors;
DROP TABLE IF EXISTS calendar_suspensions;
DROP TABLE IF EXISTS calendar_holidays;
DROP TABLE IF EXISTS calendar_exceptions;
DROP TABLE IF EXISTS calendars;
