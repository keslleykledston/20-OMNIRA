ALTER TABLE tenants
  DROP CONSTRAINT IF EXISTS tenants_wait_thresholds_chk,
  DROP COLUMN IF EXISTS wait_danger_minutes,
  DROP COLUMN IF EXISTS wait_warn_minutes;
