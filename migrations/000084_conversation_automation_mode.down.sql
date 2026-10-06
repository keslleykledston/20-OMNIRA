DROP INDEX IF EXISTS messages_system_idempotency_uq;
ALTER TABLE conversations DROP COLUMN IF EXISTS automation_mode;
