DROP TRIGGER IF EXISTS contacts_kind_realtime_trg ON contacts;
DROP FUNCTION IF EXISTS contacts_kind_realtime();
DROP INDEX IF EXISTS idx_contacts_tenant_kind;
ALTER TABLE contacts DROP CONSTRAINT IF EXISTS contacts_kind_check;
ALTER TABLE contacts DROP COLUMN IF EXISTS kind;
