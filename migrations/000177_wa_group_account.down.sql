DROP INDEX IF EXISTS idx_wa_groups_tenant_account;
ALTER TABLE wa_groups DROP COLUMN account_set_at;
ALTER TABLE wa_groups DROP COLUMN account_set_by;
ALTER TABLE wa_groups DROP CONSTRAINT wa_groups_account_fk;
ALTER TABLE wa_groups DROP COLUMN account_id;
