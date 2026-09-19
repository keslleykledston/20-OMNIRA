DROP INDEX IF EXISTS tickets_active_conversation_uq;
DROP INDEX IF EXISTS conversations_open_contact_channel_uq;
DROP INDEX IF EXISTS messages_provider_connection_id_uq;
CREATE UNIQUE INDEX messages_provider_id_uq ON messages(tenant_id, provider_message_id)
  WHERE provider_message_id <> '';
ALTER TABLE messages DROP CONSTRAINT IF EXISTS messages_channel_connection_tenant_fk;
ALTER TABLE messages DROP COLUMN IF EXISTS channel_connection_id;
