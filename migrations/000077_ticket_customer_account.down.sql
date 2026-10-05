DROP INDEX IF EXISTS tickets_customer_account_idx;
ALTER TABLE tickets DROP CONSTRAINT IF EXISTS tickets_customer_account_fk;
ALTER TABLE tickets DROP COLUMN IF EXISTS customer_account_id;
