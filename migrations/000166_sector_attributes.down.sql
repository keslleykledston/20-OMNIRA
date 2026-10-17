SET LOCAL lock_timeout = '5s';
-- Rolling back drops the descriptions, colours and supervisors that were set (informational data, no other table reads them): export them first if they matter.
ALTER TABLE sectors DROP COLUMN IF EXISTS supervisor_user_id, DROP COLUMN IF EXISTS color, DROP COLUMN IF EXISTS description;
