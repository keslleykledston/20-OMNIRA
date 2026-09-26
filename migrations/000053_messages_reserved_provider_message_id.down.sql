DROP INDEX IF EXISTS messages_reserved_provider_connection_id_uq;
ALTER TABLE messages DROP COLUMN IF EXISTS reserved_provider_message_id;
