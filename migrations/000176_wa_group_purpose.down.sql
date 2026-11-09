DROP INDEX IF EXISTS idx_wa_groups_tenant_purpose;
ALTER TABLE wa_groups DROP COLUMN purpose_set_at;
ALTER TABLE wa_groups DROP COLUMN purpose_set_by;
ALTER TABLE wa_groups DROP COLUMN purpose;
