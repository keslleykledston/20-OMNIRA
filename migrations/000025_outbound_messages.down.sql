-- Undo only what 000025 created.
DROP INDEX IF EXISTS messages_outbound_idempotency_uq;
ALTER TABLE messages DROP COLUMN IF EXISTS failure_reason;
ALTER TABLE messages DROP COLUMN IF EXISTS sent_by_user_id;
ALTER TABLE messages DROP COLUMN IF EXISTS request_hash;
ALTER TABLE messages DROP COLUMN IF EXISTS idempotency_key;
