DROP INDEX IF EXISTS contacts_tenant_whatsapp_name_idx;
DROP TRIGGER IF EXISTS contacts_effective_name_trg ON contacts;
DROP FUNCTION IF EXISTS contacts_effective_name();
ALTER TABLE contacts DROP COLUMN whatsapp_name, DROP COLUMN alias;
