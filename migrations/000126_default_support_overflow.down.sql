-- Removes the pointer and the function only. The queues, sectors, calendar and memberships it created are ordinary company data by now
-- (people may have been assigned, tuned or removed), so they stay; delete them from the screens if they are unwanted.
ALTER TABLE tenants DROP CONSTRAINT IF EXISTS tenants_default_overflow_queue_fk;
ALTER TABLE tenants DROP COLUMN IF EXISTS default_overflow_queue_id;
DROP FUNCTION IF EXISTS seed_tenant_sector_defaults(UUID);
