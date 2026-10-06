DROP INDEX IF EXISTS messages_system_idempotency_uq;
ALTER TABLE conversations DROP CONSTRAINT IF EXISTS conversations_active_customer_account_fk;
ALTER TABLE conversations DROP COLUMN IF EXISTS active_customer_account_id;
ALTER TABLE conversations DROP COLUMN IF EXISTS automation_mode;
